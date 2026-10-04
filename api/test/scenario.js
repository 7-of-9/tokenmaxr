// End-to-end API scenario, run through the harness over HTTP. The unit suite
// runs it on the in-memory store; the integration suite on real Azure tables.
import assert from 'node:assert/strict'
import { createInvite } from '../src/lib/enroll.js'
import { eventId } from '../src/lib/ids.js'
import {
  ACCT, HOMES, KFP, activity, batch, bigText, codexUsage, cursorUsage, geminiUsage, grokUsage, principal, prompt, usage,
} from './fixtures.js'

export const OWNER_IDS = 'github:test-owner'

export function client(baseUrl) {
  return async function call(method, path, { headers = {}, body } = {}) {
    const res = await fetch(baseUrl + path, {
      method,
      headers: { 'content-type': 'application/json', ...headers },
      body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
    })
    const text = await res.text()
    return { status: res.status, headers: res.headers, body: text ? JSON.parse(text) : null, raw: text }
  }
}

const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
const stranger = { 'x-ms-client-principal': principal('github', 'someone-else') }

// The harness ctx for this scenario: it reads usage right after each ingest,
// so every written day is recomputed at once instead of after 20 s.
export const SCENARIO_CTX = { rollupMinAgeMs: 0 }

export const TZ_DE = { iana: 'Europe/Berlin', windowsId: 'W. Europe Standard Time', country: 'DE', source: 'windows+region' }

export async function runScenario(t, { call, store }) {
  let token
  let token2
  let machine1
  let machine2
  const tok = () => ({ 'x-d0m1-token': token })

  await t.test('enroll and invites', async () => {
    const inv = await createInvite(store, new Date(), 'test')
    assert.equal((await call('POST', '/api/enroll', { body: { invite: 'AAAAAAAAAAAAAAAAAAAAAAAA', machineLabel: 'x', kFingerprint: KFP } })).status, 403)
    const r = await call('POST', '/api/enroll', {
      body: { invite: inv.invite, machineLabel: 'test-machine', os: 'windows', arch: 'amd64', version: '0.0.0-test', kFingerprint: KFP },
    })
    assert.equal(r.status, 200, r.raw)
    assert.match(r.body.machineId, /^m_[0-9a-f]{16}$/)
    assert.match(r.body.token, /^[A-Za-z0-9_-]{43}$/)
    token = r.body.token
    machine1 = r.body.machineId
    // Single use.
    const again = await call('POST', '/api/enroll', { body: { invite: inv.invite, machineLabel: 'x', kFingerprint: KFP } })
    assert.equal(again.status, 403)
    // Machine-issued invite; a different fleet key or none at all is a 409.
    const mi = await call('POST', '/api/invite', { headers: tok() })
    assert.equal(mi.status, 200, mi.raw)
    assert.ok(Date.parse(mi.body.expiresAt) > Date.now())
    const wrongK = await call('POST', '/api/enroll', { body: { invite: mi.body.invite, machineLabel: 'x', kFingerprint: '0123456789abcdef' } })
    assert.equal(wrongK.status, 409)
    assert.match(wrongK.body.error, /tokenmaxr invite/)
    const noK = await call('POST', '/api/enroll', { body: { invite: mi.body.invite, machineLabel: 'x' } })
    assert.equal(noK.status, 409)
    // The mismatches did not consume the invite.
    const second = await call('POST', '/api/enroll', { body: { invite: mi.body.invite.toLowerCase(), machineLabel: 'test-machine-2', kFingerprint: KFP } })
    assert.equal(second.status, 200, second.raw)
    token2 = second.body.token
    machine2 = second.body.machineId
    // Invite endpoint auth.
    assert.equal((await call('POST', '/api/invite')).status, 401)
    assert.equal((await call('POST', '/api/invite', { headers: { 'x-d0m1-token': 'nope' } })).status, 401)
    assert.equal((await call('POST', '/api/invite', { headers: stranger })).status, 403)
    assert.equal((await call('POST', '/api/invite', { headers: owner })).status, 200)
  })

  await t.test('ingest auth and request errors', async () => {
    assert.equal((await call('POST', '/api/ingest', { body: batch({}) })).status, 401)
    assert.equal((await call('POST', '/api/ingest', { headers: { 'x-d0m1-token': 'x'.repeat(43) }, body: batch({}) })).status, 401)
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: { ...batch({}), v: 2 } })).status, 400)
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: '{nope' })).status, 400)
    const tooMany = Array.from({ length: 201 }, (_, i) => usage(`cap${i}`))
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: batch({ usage: tooMany }) })).status, 413)
  })

  const big = bigText(60000)
  const A = batch({
    usage: [
      usage('u1', { cacheW1h: 8 }),
      usage('u2', { in: 10, cacheW: 0, cacheR: 0, out: 5, ts: '2026-09-20T11:00:00Z' }),
      codexUsage('c1', { in: 200, cacheR: 0, cacheW: 0, out: 100, reasoning: 40 }),
      grokUsage('g1', { in: 70, cacheR: 0, cacheW: 0, out: 30 }),
      cursorUsage('k1', { in: 300, out: 40 }),
      geminiUsage('m1', { in: 120, cacheR: 800, out: 60, reasoning: 25 }),
    ],
    activity: [
      activity('a1'),
      activity('a2', { hasUsage: false, ts: '2026-09-20T12:00:00Z' }),
      activity('bad', { id: 'not-an-id' }),
    ],
    prompts: [
      prompt('p1', { text: 'synthetic prompt about widgets' }),
      prompt('p2', { text: big, ts: '2026-09-20T09:58:00Z' }),
      prompt('p3', { model: '', ts: '2026-09-20T09:57:00Z' }),
    ],
    heartbeat: { machineLabel: ' Studio\tPC\u0007 ', os: 'windows', arch: 'amd64', version: '0.0.0-test', sources: {}, checks: {}, tz: TZ_DE, homes: HOMES },
  })
  const allIds = [...A.usage, ...A.activity.slice(0, 2), ...A.prompts].map((x) => x.id)

  const getUsage = async () => {
    const r = await call('GET', '/api/usage?days=1500')
    assert.equal(r.status, 200, r.raw)
    return r
  }
  const dayOf = (u, date) => u.body.days.find((d) => d.date === date)
  let snapshot

  await t.test('ingest a batch', async () => {
    const r = await call('POST', '/api/ingest', { headers: tok(), body: A })
    assert.equal(r.status, 200, r.raw)
    assert.deepEqual(r.body.retry, [])
    for (const id of allIds) assert.ok(r.body.accepted.includes(id), `accepted ${id}`)
    assert.equal(r.body.rejected.length, 1)
    const u = await getUsage()
    const d = dayOf(u, '2026-09-20')
    assert.ok(d, 'day present')
    assert.equal(d.v, 3)
    const an = d.providers.anthropic
    assert.deepEqual(an.exact, { in: 110, cacheW: 20, cacheW1h: 8, cacheR: 1000, out: 55, reasoning: 0, calls: 2, events: 2 })
    assert.deepEqual(an.models.exact['claude-test-model'], { in: 110, cacheW: 20, cacheW1h: 8, cacheR: 1000, out: 55, reasoning: 0, calls: 2 })
    assert.equal(an.prompts, 2)
    assert.equal(an.promptsNoUsage, 1)
    // The heartbeat in the same request supplies the reporter's country.
    assert.deepEqual(an.byMachine, { [machine1]: { in: 110, cacheW: 20, cacheR: 1000, out: 55, prompts: 2 } })
    assert.deepEqual(an.byCountry, { DE: { in: 110, cacheW: 20, cacheR: 1000, out: 55, prompts: 2 } })
    assert.equal(d.providers.openai.exact.out, 100)
    assert.equal(d.providers.openai.exact.reasoning, 40)
    assert.equal(d.providers.xai.estimated.in, 70)
    assert.equal(d.providers.xai.exact.events, 0)
    assert.deepEqual(Object.keys(d.providers.xai.models.estimated), ['grok-test'])
    // Cursor: its own provider, no cache buckets.
    assert.deepEqual(Object.keys(d.providers).sort(), ['anthropic', 'cursor', 'google', 'openai', 'xai'])
    assert.deepEqual(d.providers.cursor.exact, { in: 300, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 40, reasoning: 0, calls: 1, events: 1 })
    assert.deepEqual(Object.keys(d.providers.cursor.models.exact), ['cursor-test-model'])
    assert.deepEqual(d.providers.cursor.byMachine, { [machine1]: { in: 300, cacheW: 0, cacheR: 0, out: 40, prompts: 0 } })
    // Gemini CLI: provider google, cached reads split out, thoughts inside out.
    assert.deepEqual(d.providers.google.exact, { in: 120, cacheW: 0, cacheW1h: 0, cacheR: 800, out: 60, reasoning: 25, calls: 1, events: 1 })
    assert.deepEqual(Object.keys(d.providers.google.models.exact), ['gemini-test-model'])
    assert.deepEqual(d.providers.google.byCountry, { DE: { in: 120, cacheW: 0, cacheR: 800, out: 60, prompts: 0 } })
    snapshot = JSON.stringify(u.body.days)
  })

  await t.test('re-ingesting the same batch changes nothing', async () => {
    for (let i = 0; i < 2; i++) {
      const r = await call('POST', '/api/ingest', { headers: tok(), body: A })
      assert.equal(r.status, 200, r.raw)
      assert.deepEqual(r.body.retry, [])
    }
    // Another machine (another country) sending the same content is also a
    // no-op: machine and cc belong to the first reporter.
    const other = { ...A, heartbeat: { ...A.heartbeat, machineLabel: 'test-machine-2', tz: { ...TZ_DE, iana: 'Europe/London', country: 'GB' } } }
    const r2 = await call('POST', '/api/ingest', { headers: { 'x-d0m1-token': token2 }, body: other })
    assert.equal(r2.status, 200, r2.raw)
    assert.equal(JSON.stringify((await getUsage()).body.days), snapshot)
  })

  await t.test('new events take the reporter and its current country', async () => {
    const r = await call('POST', '/api/ingest', {
      headers: { 'x-d0m1-token': token2 },
      body: batch({ usage: [grokUsage('g9', { in: 1, cacheR: 0, cacheW: 0, out: 2 })] }),
    })
    assert.equal(r.status, 200, r.raw)
    const x = dayOf(await getUsage(), '2026-09-20').providers.xai
    assert.deepEqual(x.byMachine, {
      [machine1]: { in: 70, cacheW: 0, cacheR: 0, out: 30, prompts: 0 },
      [machine2]: { in: 1, cacheW: 0, cacheR: 0, out: 2, prompts: 0 },
    })
    assert.deepEqual(x.byCountry, {
      GB: { in: 1, cacheW: 0, cacheR: 0, out: 2, prompts: 0 },
      DE: { in: 70, cacheW: 0, cacheR: 0, out: 30, prompts: 0 },
    })
  })

  await t.test('growth merges by max, pv rules apply', async () => {
    const r = await call('POST', '/api/ingest', {
      headers: tok(),
      body: batch({
        usage: [
          usage('u1', { out: 500, ts: '2026-09-20T10:00:03Z' }),
          usage('u2', { pv: 0, in: 99999, out: 99999 }),
          codexUsage('c1', { pv: 2, in: 150, cacheR: 0, cacheW: 0, out: 60, reasoning: 0 }),
        ],
      }),
    })
    assert.equal(r.status, 200, r.raw)
    const d = dayOf(await getUsage(), '2026-09-20')
    assert.equal(d.providers.anthropic.exact.out, 505)
    assert.equal(d.providers.anthropic.exact.in, 110)
    assert.equal(d.providers.openai.exact.in, 150)
    assert.equal(d.providers.openai.exact.out, 60)
    assert.equal(d.providers.openai.exact.reasoning, 0)
  })

  await t.test('midnight straddle stays pinned to the first-seen local date', async () => {
    const first = usage('s1', { ts: '2026-09-21T23:59:58Z', tzOffsetMin: 0, out: 7, in: 0, cacheW: 0, cacheR: 0 })
    const tz = usage('s2', { ts: '2026-09-21T23:30:00Z', tzOffsetMin: 60, out: 3, in: 0, cacheW: 0, cacheR: 0 })
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: batch({ usage: [first, tz] }) })).status, 200)
    const later = { ...first, ts: '2026-09-22T00:00:04Z', out: 9 }
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: batch({ usage: [later] }) })).status, 200)
    const u = await getUsage()
    assert.equal(dayOf(u, '2026-09-21').providers.anthropic.exact.out, 9)
    assert.equal(dayOf(u, '2026-09-22').providers.anthropic.exact.out, 3)
  })

  await t.test('usage totals, machines and privacy', async () => {
    const u = await getUsage()
    assert.match(u.headers.get('cache-control'), /public, max-age=60/)
    const { totals } = u.body
    assert.equal(totals.accounts, 5)
    assert.deepEqual(totals.accountsByProvider, { anthropic: 1, openai: 1, xai: 1, cursor: 1, google: 1 })
    assert.equal(totals.machines, 2)
    assert.equal(totals.machinesLive, 2)
    assert.ok(u.body.lastIngestAt)
    assert.equal(u.body.firstDate, '2026-09-20')
    assert.equal('ratios' in u.body, false)
    // Public machine list: cleaned labels and countries from the heartbeats.
    assert.deepEqual(u.body.machines.map(({ id, label, cc, os, live }) => ({ id, label, cc, os, live })), [
      { id: machine1, label: 'Studio PC', cc: 'DE', os: 'windows', live: true },
      { id: machine2, label: 'test-machine-2', cc: 'GB', os: 'windows', live: true },
    ])
    for (const m of u.body.machines) {
      assert.deepEqual(Object.keys(m).sort(), ['cc', 'firstSeenAt', 'id', 'label', 'lastSeenAt', 'live', 'os'])
      assert.ok(Date.parse(m.firstSeenAt) && Date.parse(m.lastSeenAt))
    }
    // Only the last 2 days: firstDate still reports the first data.
    const recent = await call('GET', '/api/usage?days=2')
    assert.equal(recent.body.firstDate, '2026-09-20')
    assert.ok(recent.body.days.every((d) => d.date >= '2026-09-26'))
    const all = await call('GET', '/api/usage?days=all')
    assert.equal(all.body.firstDate, '2026-09-20')
    assert.deepEqual(all.body.days, u.body.days)
    // The heartbeat's scan roots are stored on the machine row (v1.3) and
    // are never part of the public response.
    const rows = await store.table('machines').list('m')
    assert.deepEqual(JSON.parse(rows.find((m) => m.rowKey === machine1).homes), HOMES)
    const s = u.raw
    for (const secret of ['synthetic-label', 'synthetic prompt', '/synthetic/workspace', 'acctLabel', 'workspace', 'tokenHash', 'heartbeat', 'homes', 'Europe/Berlin', 'synthetic/home', 'wsl$', ...Object.values(ACCT)]) {
      assert.ok(!s.includes(secret), `usage leaks ${secret}`)
    }
    assert.ok(!/a_[0-9a-f]{16}/.test(s))
  })

  await t.test('prompts: owner only, decrypt, filter, full text', async () => {
    assert.equal((await call('GET', '/api/prompts?month=2026-09')).status, 401)
    assert.equal((await call('GET', '/api/prompts?month=2026-09', { headers: tok() })).status, 401)
    assert.equal((await call('GET', '/api/prompts?month=2026-09', { headers: stranger })).status, 403)
    assert.equal((await call('GET', `/api/prompts/${A.prompts[0].id}?month=2026-09`, { headers: stranger })).status, 403)

    // Model fill: p3 arrived with no model; a later sighting supplies it.
    const fill = batch({ prompts: [prompt('p3', { model: 'filled-model', ts: '2026-09-20T09:57:00Z', text: 'ignored rewrite' })] })
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: fill })).status, 200)

    // Oldest first by default; order=desc lists newest first.
    const asc = await call('GET', '/api/prompts?month=2026-09', { headers: owner })
    assert.equal(asc.body.order, 'asc')
    assert.deepEqual(asc.body.items.map((i) => i.id), [A.prompts[2].id, A.prompts[1].id, A.prompts[0].id])
    assert.equal((await call('GET', '/api/prompts?month=2026-09&order=sideways', { headers: owner })).status, 400)
    const r = await call('GET', '/api/prompts?month=2026-09&order=desc', { headers: owner })
    assert.equal(r.status, 200, r.raw)
    assert.equal(r.body.order, 'desc')
    assert.equal(r.body.month, '2026-09')
    assert.deepEqual(r.body.months, ['2026-09'])
    assert.equal(r.body.items.length, 3)
    assert.deepEqual(r.body.items.map((i) => i.id), [A.prompts[0].id, A.prompts[1].id, A.prompts[2].id])
    const [p1, p2, p3] = r.body.items
    assert.equal(p1.text, 'synthetic prompt about widgets')
    assert.equal(p1.truncated, false)
    assert.equal(p1.acctLabel, 'synthetic-label')
    assert.equal(p1.workspace, '/synthetic/workspace')
    assert.equal(p1.machine, 'test-machine')
    assert.equal(p2.truncated, true)
    assert.ok(Buffer.byteLength(p2.text) <= 4096)
    assert.ok(big.startsWith(p2.text))
    assert.equal(p3.model, 'filled-model')
    assert.equal(p3.text, 'synthetic prompt p3', 'enc is never rewritten')

    const q = await call('GET', '/api/prompts?month=2026-09&q=WIDGETS', { headers: owner })
    assert.deepEqual(q.body.items.map((i) => i.id), [A.prompts[0].id])
    assert.equal((await call('GET', '/api/prompts?month=2026-09&provider=openai', { headers: owner })).body.items.length, 0)
    assert.equal((await call('GET', '/api/prompts?month=2026-09&model=filled-model', { headers: owner })).body.items.length, 1)
    assert.equal((await call('GET', '/api/prompts?month=2026-09&workspace=nowhere', { headers: owner })).body.items.length, 0)

    // Pagination.
    const pg1 = await call('GET', '/api/prompts?month=2026-09&limit=2&order=desc', { headers: owner })
    assert.equal(pg1.body.items.length, 2)
    assert.ok(pg1.body.cursor)
    const pg2 = await call('GET', `/api/prompts?month=2026-09&limit=2&order=desc&cursor=${pg1.body.cursor}`, { headers: owner })
    assert.deepEqual(pg2.body.items.map((i) => i.id), [A.prompts[2].id])
    assert.equal(pg2.body.cursor, null)
    // A cursor only continues the order it was issued for.
    assert.equal((await call('GET', `/api/prompts?month=2026-09&limit=2&cursor=${pg1.body.cursor}`, { headers: owner })).status, 400)
    const up1 = await call('GET', '/api/prompts?month=2026-09&limit=2', { headers: owner })
    const up2 = await call('GET', `/api/prompts?month=2026-09&limit=2&cursor=${up1.body.cursor}`, { headers: owner })
    assert.deepEqual([...up1.body.items, ...up2.body.items].map((i) => i.id), [A.prompts[2].id, A.prompts[1].id, A.prompts[0].id])

    // Full text from the blob.
    const full = await call('GET', `/api/prompts/${A.prompts[1].id}?month=2026-09`, { headers: owner })
    assert.equal(full.status, 200, full.raw)
    assert.equal(full.body.text, big)
    const noMonth = await call('GET', `/api/prompts/${A.prompts[0].id}`, { headers: owner })
    assert.equal(noMonth.body.text, 'synthetic prompt about widgets')
    assert.equal((await call('GET', `/api/prompts/${'0'.repeat(32)}?month=2026-09`, { headers: owner })).status, 404)
    assert.equal((await call('GET', '/api/prompts/xyz', { headers: owner })).status, 400)
  })

  await t.test('prompts: all months by default, facets, exact filters', async () => {
    // Two older months (2026-09 keeps its three rows for the storage checks).
    const W = 'C:\\Users\\Synth\\src\\alpha'
    const more = [
      prompt('m1', { ts: '2026-08-10T10:00:00Z', workspace: W, acctQ: 'recorded' }),
      prompt('m2', { ts: '2026-08-11T10:00:00Z', workspace: 'C:\\Users\\Synth\\SRC\\alpha' }),
      prompt('m3', { ts: '2026-08-12T10:00:00Z', workspace: W + '\\collector' }),
      prompt('m4', { ts: '2026-07-01T10:00:00Z', workspace: 'C--Users-Synth-src-alpha', acctLabel: '' }),
      prompt('m5', { ts: '2026-07-02T10:00:00Z', workspace: 'home-synth-src-beta', acct: '', acctLabel: '', machine: 'test-machine-2' }),
      prompt('m6', {
        id: eventId('prompt', 'openai', 'codex', 'm6'), provider: 'openai', source: 'codex', model: 'gpt-test',
        acct: ACCT.openai, acctLabel: '', ts: '2026-07-03T10:00:00Z', workspace: W,
      }),
      prompt('m7', {
        id: eventId('prompt', 'openai', 'codex', 'm7'), provider: 'openai', source: 'codex', model: 'gpt-test',
        acct: ACCT.openai, acctLabel: 'synthetic-openai-label', ts: '2026-08-13T10:00:00Z', workspace: W,
      }),
    ]
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: batch({ prompts: more }) })).status, 200)
    const ids = (r) => r.body.items.map((i) => i.id)

    for (const path of ['/api/prompts?order=desc', '/api/prompts?month=all&order=desc']) {
      const r = await call('GET', path, { headers: owner })
      assert.equal(r.status, 200, r.raw)
      assert.equal(r.body.month, 'all')
      assert.equal(r.body.total, 10)
      assert.deepEqual(r.body.months, ['2026-09', '2026-08', '2026-07'])
      const ts = r.body.items.map((i) => i.ts)
      assert.deepEqual(ts, [...ts].sort().reverse(), 'newest first across months')
      assert.equal(r.body.items[r.body.items.length - 1].id, more[3].id)
      assert.equal(r.body.facets, undefined)
    }
    const oldest = await call('GET', '/api/prompts', { headers: owner })
    const ats = oldest.body.items.map((i) => i.ts)
    assert.deepEqual(ats, [...ats].sort(), 'oldest first across months by default')
    assert.equal(oldest.body.items[0].id, more[3].id)
    assert.equal((await call('GET', '/api/prompts?month=2026-8', { headers: owner })).status, 400)
    // Pagination across months, in both orders: no gaps, no repeats, and one
    // order is the exact reverse of the other.
    const walk = async (order) => {
      const seen = []
      let cursor = null
      do {
        const r = await call('GET', `/api/prompts?limit=3&order=${order}${cursor ? `&cursor=${cursor}` : ''}`, { headers: owner })
        assert.equal(r.body.total, 10)
        seen.push(...ids(r))
        cursor = r.body.cursor
      } while (cursor)
      assert.equal(seen.length, 10)
      assert.equal(new Set(seen).size, 10)
      return seen
    }
    assert.deepEqual(await walk('asc'), (await walk('desc')).reverse())

    // Facets over the whole archive.
    assert.equal((await call('GET', '/api/prompts/facets')).status, 401)
    assert.equal((await call('GET', '/api/prompts/facets', { headers: stranger })).status, 403)
    const f = await call('GET', '/api/prompts/facets', { headers: owner })
    assert.equal(f.status, 200, f.raw)
    assert.match(f.headers.get('cache-control'), /private, no-store/)
    assert.equal(f.body.total, 10)
    assert.deepEqual(f.body.months, [{ month: '2026-09', count: 3 }, { month: '2026-08', count: 4 }, { month: '2026-07', count: 3 }])
    assert.deepEqual(f.body.providers, [{ provider: 'anthropic', count: 8 }, { provider: 'openai', count: 2 }])
    // Legacy prompts (no acctQ) with a label count as timeline; without one,
    // the candidate account groups as "probably <label>" (SPEC "Accounts").
    assert.deepEqual(f.body.accounts.map(({ acct, provider, label, quality, count, unattributed, probable }) => [acct, provider, label, quality, count, unattributed, probable]), [
      [`${ACCT.anthropic}~timeline`, 'anthropic', 'synthetic-label', 'timeline', 5, false, undefined],
      [`${ACCT.anthropic}~recorded`, 'anthropic', 'synthetic-label', 'recorded', 1, false, undefined],
      [`unknown:anthropic:${ACCT.anthropic}`, 'anthropic', '', '', 1, true, 'synthetic-label'],
      ['unknown:anthropic', 'anthropic', '', '', 1, true, undefined],
      [`${ACCT.openai}~timeline`, 'openai', 'synthetic-openai-label', 'timeline', 1, false, undefined],
      [`unknown:openai:${ACCT.openai}`, 'openai', '', '', 1, true, 'synthetic-openai-label'],
    ])
    const ws = Object.fromEntries(f.body.workspaces.map((w) => [w.label, w]))
    assert.deepEqual(Object.keys(ws).sort(), ['alpha', 'beta', 'workspace'])
    assert.equal(ws.alpha.count, 6)
    assert.equal(ws.alpha.key, 'c:\\users\\synth\\src\\alpha')
    assert.equal(ws.alpha.path, W)
    assert.equal(ws.alpha.paths.length, 4)
    assert.equal(ws.beta.path, '/home/synth/src/beta')
    assert.equal(ws.workspace.count, 3)
    assert.deepEqual(f.body.machines, [{ value: 'test-machine', count: 9 }, { value: 'test-machine-2', count: 1 }])
    assert.equal(f.body.models.find((m) => m.value === 'gpt-test').count, 2)
    // The same facets inline on request.
    const inline = await call('GET', '/api/prompts?facets=1&limit=1', { headers: owner })
    assert.deepEqual(inline.body.facets, (({ generatedAt, undecryptable, ...rest }) => rest)(f.body))

    // Exact filters by facet key.
    const q = async (qs) => (await call('GET', `/api/prompts?${qs}`, { headers: owner })).body
    const alpha = await q(`workspace=${encodeURIComponent(ws.alpha.key)}`)
    assert.equal(alpha.total, 6)
    assert.ok(alpha.items.every((i) => i.workspaceKey === ws.alpha.key && i.workspaceLabel === 'alpha'))
    assert.equal((await q(`workspace=${encodeURIComponent(ws.alpha.key)}&month=2026-08`)).total, 4)
    assert.equal((await q(`workspace=${encodeURIComponent(W + '\\collector')}`)).total, 1, 'a raw path still matches exactly')
    assert.deepEqual((await q('acct=unknown:anthropic')).items.map((i) => i.id), [more[4].id])
    const probable = (await q(`acct=${encodeURIComponent(`unknown:anthropic:${ACCT.anthropic}`)}`)).items
    assert.deepEqual(probable.map((i) => [i.id, i.acctQ, i.acctLabel, i.acctProbable]), [[more[3].id, 'inferred', '', 'synthetic-label']])
    const recorded = (await q(`acct=${encodeURIComponent(`${ACCT.anthropic}~recorded`)}`)).items
    assert.deepEqual(recorded.map((i) => [i.id, i.acctQ, i.acctLabel]), [[more[0].id, 'recorded', 'synthetic-label']])
    assert.equal((await q(`acct=${encodeURIComponent(`${ACCT.openai}~timeline`)}`)).total, 1)
    assert.equal((await q('machine=test-machine-2')).total, 1)
    assert.equal((await q('machine=test-machine-')).total, 0, 'machine is exact')
    assert.equal((await q(`provider=openai&acct=${encodeURIComponent(`unknown:openai:${ACCT.openai}`)}`)).items[0].acct, `unknown:openai:${ACCT.openai}`)

    // A better attribution re-sent later upgrades the stored row in place
    // (re-encrypted account and label, plaintext acctQ); a worse one does not.
    const up = { ...more[3], acctQ: 'bounded', acctLabel: 'synthetic-label' }
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: batch({ prompts: [up] }) })).status, 200)
    const down = { ...more[3], acctQ: 'lineage', acct: ACCT.openai, acctLabel: '' }
    assert.equal((await call('POST', '/api/ingest', { headers: tok(), body: batch({ prompts: [down] }) })).status, 200)
    const upgraded = (await q(`acct=${encodeURIComponent(`${ACCT.anthropic}~bounded`)}`)).items
    assert.deepEqual(upgraded.map((i) => [i.id, i.acctQ, i.acctLabel, i.text]), [[more[3].id, 'bounded', 'synthetic-label', 'synthetic prompt m4']])
    assert.equal((await q(`acct=${encodeURIComponent(`unknown:anthropic:${ACCT.anthropic}`)}`)).total, 0)
    assert.equal((await q('')).total, 10)
  })
}
