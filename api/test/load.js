#!/usr/bin/env node
// Synthetic backfill load through the local harness, to measure ingest
// throughput (events/s). Synthetic events only; no real usage is read.
//
//   node test/load.js                    random-prefix Azure tables, dropped afterwards
//   node test/load.js --memory           in-memory store (no Azure)
//   node test/load.js --days 30 --per-day 400 --machines 2 --parallel 48
//
// Each machine uploads its share of the stream in chronological order, one
// request at a time, in batches of up to 200 usage and 500 activity events,
// the way the collector drains its outbox during a first-run backfill.
import { randomBytes } from 'node:crypto'
import { createHarness } from './harness.js'
import { azureStore, dropPrefixed, memoryStore } from '../src/lib/tables.js'
import { createInvite } from '../src/lib/enroll.js'
import { handleUsage } from '../src/lib/usage.js'
import { resolveConnectionString } from '../scripts/storage-env.js'
import { client } from './scenario.js'
import { KFP, activity, batch, usage } from './fixtures.js'

const args = process.argv.slice(2)
const opt = (name, dflt) => {
  const i = args.indexOf(`--${name}`)
  return i >= 0 ? Number(args[i + 1]) : dflt
}
const DAYS = opt('days', 20)
const PER_DAY = opt('per-day', 400)
const MACHINES = opt('machines', 1)
// Storage calls in flight per request (ctx.parallel); undefined = the default.
const PARALLEL = opt('parallel', undefined)
const ACTIVITY_EVERY = 4

function stream() {
  const start = Date.parse('2026-06-01T00:00:00Z')
  const usageEvents = []
  const activityEvents = []
  for (let d = 0; d < DAYS; d++) {
    for (let i = 0; i < PER_DAY; i++) {
      const ts = new Date(start + d * 86400000 + Math.floor((i / PER_DAY) * 86000000)).toISOString()
      usageEvents.push(usage(`load-${d}-${i}`, { ts, in: 10 + i, cacheW: 100, cacheW1h: 40, cacheR: 1000 + i, out: 20 + (i % 7) }))
      if (i % ACTIVITY_EVERY === 0) activityEvents.push(activity(`load-${d}-${i}`, { ts }))
    }
  }
  return { usageEvents, activityEvents }
}

// Chronological batches of <= 200 usage plus the activity in the same time span.
function batches(usageEvents, activityEvents) {
  const out = []
  let a = 0
  for (let u = 0; u < usageEvents.length; u += 200) {
    const chunk = usageEvents.slice(u, u + 200)
    const until = chunk[chunk.length - 1].ts
    const acts = []
    while (a < activityEvents.length && activityEvents[a].ts <= until && acts.length < 500) acts.push(activityEvents[a++])
    out.push({ usage: chunk, activity: acts })
  }
  if (a < activityEvents.length) out.push({ usage: [], activity: activityEvents.slice(a) })
  return out
}

async function main() {
  const memory = args.includes('--memory')
  let store
  let connectionString
  let prefix = ''
  if (memory) {
    store = memoryStore()
  } else {
    connectionString = resolveConnectionString()
    prefix = 'tload' + randomBytes(4).toString('hex')
    store = azureStore({ connectionString, prefix })
  }
  const server = createHarness({ store, env: {}, quiet: true, parallel: PARALLEL })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const call = client(`http://127.0.0.1:${server.address().port}`)
  console.log(`store ${store.kind}${prefix ? ` prefix ${prefix}` : ''}: ${DAYS} days x ${PER_DAY} usage/day, ${MACHINES} machine(s), parallel ${PARALLEL ?? 'default'}`)
  try {
    const tokens = []
    for (let m = 0; m < MACHINES; m++) {
      const { invite } = await createInvite(store, new Date(), 'load')
      const r = await call('POST', '/api/enroll', { body: { invite, machineLabel: `load-${m}`, kFingerprint: KFP } })
      if (r.status !== 200) throw new Error(`enroll ${r.status}`)
      tokens.push(r.body.token)
    }
    const { usageEvents, activityEvents } = stream()
    const all = batches(usageEvents, activityEvents)
    const total = usageEvents.length + activityEvents.length
    // Round-robin the batches over the machines; each machine uploads serially.
    const queues = tokens.map(() => [])
    all.forEach((b, i) => queues[i % tokens.length].push(b))
    let requests = 0
    let retried = 0
    const t0 = performance.now()
    await Promise.all(queues.map(async (queue, m) => {
      for (let b of queue) {
        for (;;) {
          requests++
          const r = await call('POST', '/api/ingest', { headers: { 'x-d0m1-token': tokens[m] }, body: batch(b) })
          if (r.status !== 200) throw new Error(`ingest ${r.status}`)
          if (r.body.retry.length === 0) break
          retried += r.body.retry.length
          const again = new Set(r.body.retry)
          b = { usage: b.usage.filter((x) => again.has(x.id)), activity: b.activity.filter((x) => again.has(x.id)) }
        }
      }
    }))
    const ingestMs = performance.now() - t0
    const t1 = performance.now()
    const served = await call('GET', '/api/usage?days=all')
    const readMs = performance.now() - t1
    // What the page shows once every dirty day is past the 60 s grace period.
    const settled = (await handleUsage({ method: 'GET', headers: {}, query: { days: 'all' }, now: new Date(Date.now() + 61000) }, { store, parallel: PARALLEL })).jsonBody
    const outOf = (body) => (body.days ?? []).reduce((s, d) => s + (d.providers.anthropic?.exact?.out ?? 0), 0)
    const expectOut = usageEvents.reduce((s, x) => s + x.out, 0)
    console.log(JSON.stringify({
      events: total,
      requests,
      retried,
      ingestSeconds: +(ingestMs / 1000).toFixed(2),
      eventsPerSecond: Math.round(total / (ingestMs / 1000)),
      firstUsageReadMs: Math.round(readMs),
      daysServed: served.body.days?.length ?? 0,
      servedOutMatches: outOf(served.body) === expectOut,
      settledOutMatches: outOf(settled) === expectOut,
    }))
  } finally {
    server.close()
    if (!memory) {
      const dropped = await dropPrefixed(connectionString, prefix)
      console.log(`dropped ${dropped.length} tables/containers`)
    }
  }
}

main().catch((err) => {
  console.error(`load failed: ${err?.message || err}`)
  process.exitCode = 1
})
