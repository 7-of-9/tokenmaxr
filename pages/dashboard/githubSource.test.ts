// The GitHub Pages adapter against fixtures shaped like the published files, and a parity check against the
// d0m1 API's own rollup. Run: node --experimental-strip-types --no-warnings pages/dashboard/githubSource.test.ts
import assert from 'node:assert/strict'
import test from 'node:test'
// @ts-expect-error -- plain JS module without types (the d0m1 API’s rollup).
import { aliasAccts, computeRollup } from '../../api/src/lib/rollup.js'
import { buildActivity } from '../../src/components/agents/activity.ts'
import { machineDays } from '../../src/components/agents/machines.ts'
import type { Provider, UsageResponse } from '../../src/components/agents/types.ts'
import {
  activeDayCount, buildCells, DEFAULT_VIEW, normaliseDays, promptModelsOf, summarise, type NormDay,
} from '../../src/components/agents/usage.ts'
import {
  buildUsage, githubSource, readUsageRows, SCHEMA1_COLS, type MachineFiles, type Published, type UsageFile,
} from './githubSource.ts'

const NOW = new Date('2026-09-30T12:00:00Z')
const A = 'm_aaaaaaaaaaaa'
const B = 'm_bbbbbbbbbbbb'
// Account hashes as collectors publish them: they must never reach the page.
const H1 = 'a_9f8e7d6c5b4a3f2e'
const H2 = 'a_0123456789abcdef'
const COLS2 = ['date', 'provider', 'source', 'model', 'acct', 'q', 'in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'events', 'prompts', 'promptsNoUsage', 'modelPrompts']

/** A schema 2 row from named values. */
function row2(values: Partial<Record<string, string | number>>) {
  return COLS2.map(c => values[c] ?? (['date', 'provider', 'source', 'model', 'acct', 'q'].includes(c) ? '' : 0))
}

// Machine A publishes schema 2 (in a shuffled column order, to prove rows are read by name).
const shuffled = [...COLS2].reverse()
const a2: UsageFile = {
  schema: 2, month: '2026-09', cols: shuffled,
  rows: [
    // 1 Sept: two Anthropic models, prompt records per model (one with no model), and estimated OpenAI tokens.
    row2({ date: '2026-09-01', provider: 'anthropic', source: 'claude-code', model: 'claude-x', acct: H1, in: 100, cacheW: 50, cacheW1h: 10, cacheR: 1000, out: 200, reasoning: 20, events: 3, modelPrompts: 3 }),
    row2({ date: '2026-09-01', provider: 'anthropic', source: 'claude-code', model: 'claude-y', acct: H1, in: 10, out: 5, events: 1, modelPrompts: 1 }),
    row2({ date: '2026-09-01', provider: 'anthropic', source: 'claude-code', model: '', acct: H1, prompts: 4, modelPrompts: 1 }),
    row2({ date: '2026-09-01', provider: 'openai', source: 'codex', model: 'gpt-z', acct: H2, q: 'e', in: 300, out: 30, events: 2 }),
    row2({ date: '2026-09-01', provider: 'openai', source: 'codex', model: '', acct: H2, prompts: 2 }),
    // 2 Sept: prompts only, none with token records.
    row2({ date: '2026-09-02', provider: 'anthropic', source: 'claude-code', model: '', acct: H1, prompts: 5, promptsNoUsage: 5 }),
    // 3 Sept: tokens, and one prompt whose tokens were never recorded.
    row2({ date: '2026-09-03', provider: 'anthropic', source: 'claude-code', model: 'claude-x', acct: H1, in: 50, out: 50, events: 1, modelPrompts: 2 }),
    row2({ date: '2026-09-03', provider: 'anthropic', source: 'claude-code', model: '', acct: H1, prompts: 3, promptsNoUsage: 1 }),
    // A provider this page does not know is skipped, as the API's rollup skips it.
    row2({ date: '2026-09-03', provider: 'acme', source: 'acme-cli', model: 'm', in: 999, events: 1 }),
  ].map(r => shuffled.map(c => r[COLS2.indexOf(c)])),
}
// Machine B still publishes schema 1: one file with "cols", one early file without.
const b1: UsageFile = {
  schema: 1, month: '2026-09', cols: SCHEMA1_COLS,
  rows: [
    ['2026-09-01', 'anthropic', 'claude-code', 'claude-x', H1, 7, 0, 0, 3, 1, 0],
    ['2026-09-01', 'anthropic', 'claude-code', '', H1, 0, 0, 0, 0, 0, 1],
  ],
}
const b0: UsageFile = {
  schema: 1, month: '2026-08',
  rows: [
    ['2026-08-20', 'anthropic', 'claude-code', 'claude-x', H1, 1000, 0, 0, 100, 2, 0],
    ['2026-08-20', 'anthropic', 'claude-code', '', H1, 0, 0, 0, 0, 0, 2],
    ['not-a-date', 'anthropic', 'claude-code', '', H1, 0, 0, 0, 0, 0, 9],
  ],
}
const machines = (): MachineFiles[] => [
  { id: A, meta: { id: A, label: 'studio', os: 'windows', collector: '0.4.0', updatedAt: '2026-09-30T11:58:00Z',
    cc: 'GB', firstSeenAt: '2026-09-01', lastEventAt: '2026-09-30T11:50:00Z' }, usage: [a2],
    accountUsage: { schema: 2, snapshots: [{ provider: 'anthropic', source: 'claude-code', acct: H1, date: '2026-09-01', totalTokens: 5000, observedAt: '2026-09-02T00:00:00Z' }] } },
  { id: B, meta: { id: B, label: 'laptop', os: 'darwin', collector: '0.3.3', updatedAt: '2026-09-29T08:00:00Z' }, usage: [b1, b0] },
]
const published = (): Published => ({ title: 'My usage', generatedAt: '2026-09-30T11:59:00Z', machines: machines() })

const FROM = '2026-08-01'
const TO = '2026-09-30'
const tokens = (b: { in: number; cacheW: number; cacheR: number; out: number }) => b.in + b.cacheW + b.cacheR + b.out

test('rows are read by column name, in any order, from schema 1 and schema 2 files', () => {
  const rows = readUsageRows(A, a2)
  const first = rows[0]
  assert.equal(first.model, 'claude-x')
  assert.deepEqual([first.in, first.cacheW, first.cacheW1h, first.cacheR, first.out, first.reasoning, first.events], [100, 50, 10, 1000, 200, 20, 3])
  assert.equal(rows.find(r => r.provider === 'openai' && r.model === 'gpt-z')?.estimated, true)
  assert.equal(rows.some(r => (r.provider as string) === 'acme'), false)
  const old = readUsageRows(B, b0)
  assert.equal(old.length, 2, 'a file without cols reads as schema 1; a malformed date is skipped')
  assert.deepEqual([old[0].in, old[0].out, old[0].events, old[0].cacheW1h, old[0].estimated], [1000, 100, 2, 0, false])
  assert.equal(old[0].promptsNoUsage, null)
  assert.equal(old[1].prompts, 2)
})

test('the fleet response carries the same nuance as d0m1.com/tokens', () => {
  const data = buildUsage(published(), NOW)
  const days = normaliseDays(data)
  assert.deepEqual(days.map(d => d.date), ['2026-08-20', '2026-09-01', '2026-09-02', '2026-09-03'])
  assert.equal(data.firstDate, '2026-08-20')
  assert.equal(data.generatedAt, '2026-09-30T11:59:00Z')

  const cells = buildCells(days, DEFAULT_VIEW)
  const summary = summarise(cells, FROM, TO)
  assert.equal(activeDayCount(days, DEFAULT_VIEW, FROM, TO), 3, 'the prompt-only day is not an active day')
  assert.equal(summary.prompts, 2 + 7 + 5 + 3, 'prompts on days without tokens still count in the period total')
  assert.equal(summary.activeDayPrompts, 2 + 7 + 3)
  assert.equal(summary.accounts, 2)
  assert.equal(summary.providers, 2)

  const sept1 = cells.get('2026-09-01')!
  assert.equal(sept1.exact, 1350 + 15 + 10)
  assert.equal(sept1.estimated, 330, 'estimated rows land in the estimated bucket')
  assert.equal(sept1.total, 1705)

  // An untracked cell: prompts with no recorded tokens draw as an outlined empty day.
  const sept2 = cells.get('2026-09-02')!
  assert.deepEqual([sept2.total, sept2.prompts, sept2.promptsNoUsage, sept2.recordedTokenDay], [0, 5, 5, false])
  assert.equal(cells.get('2026-09-03')!.promptsNoUsage, 1)

  const p1 = days[1].providers.anthropic!
  assert.deepEqual(Object.keys(p1.models.exact), ['claude-x', 'claude-y'])
  assert.equal(p1.models.exact['claude-x'].cacheW1h, 10)
  assert.equal(p1.exact.reasoning, 20)
  assert.equal(p1.exact.events, 5)
  assert.equal(p1.promptsByModel, undefined, 'a schema 1 machine on the same day leaves per-model prompts unknown')
  assert.equal(promptModelsOf(p1)['claude-x'], null)
  assert.deepEqual(days[1].providers.openai!.promptsByModel, {}, 'no prompt records: zero per model, not unknown')
  assert.deepEqual(days[3].providers.anthropic!.promptsByModel, { 'claude-x': 2 })
  assert.equal(sept1.modelPrompts!['openai|gpt-z'], 0)

  // Places: machine ids and countries (unknown is ZZ), tokens and prompts.
  assert.deepEqual(Object.keys(p1.byMachine), [A, B])
  assert.equal(tokens(p1.byMachine[B]), 10)
  assert.equal(p1.byMachine[A].prompts, 4)
  assert.deepEqual(Object.keys(p1.byCountry), ['GB', 'ZZ'])

  // Machines from meta: live from the newest event, last seen falls back to the last publish.
  assert.deepEqual(data.machines!.map(m => [m.id, m.label, m.cc, m.live, m.lastSeenAt, m.firstSeenAt]), [
    [B, 'laptop', '', false, '2026-09-29T08:00:00Z', '2026-08-20'],
    [A, 'studio', 'GB', true, '2026-09-30T11:50:00Z', '2026-09-01'],
  ])
  assert.deepEqual(data.totals, { accounts: 2, accountsByProvider: { anthropic: 1, openai: 1, xai: 0, cursor: 0, google: 0 }, machines: 2, machinesLive: 1 })
  assert.equal(data.lastIngestAt, '2026-09-30T11:58:00Z')
  assert.equal(data.accountUsagePending, 1, 'a total from a machine with no ledger waits for reconciliation; nothing is added')
  assert.equal(days.some(d => Object.values(d.providers).some(p => p!.exact.unattributed)), false)

  // The activity feed sees the unrecorded prompts and both machines.
  const months = buildActivity(days, DEFAULT_VIEW, data.machines!, FROM, TO)
  assert.equal(months[0].month, '2026-09')
  assert.equal(months[0].promptsNoUsage, 6)
  assert.deepEqual(months[0].machines.map(m => m.label).sort(), ['laptop', 'studio'])
})

test('account hashes become per-page aliases and never appear in the response', () => {
  const data = buildUsage(published(), NOW)
  const json = JSON.stringify(data)
  for (const hash of [H1, H2]) assert.equal(json.includes(hash), false)
  const days = normaliseDays(data)
  assert.deepEqual(days[1].providers.anthropic!.accts, ['acct2'])
  assert.deepEqual(days[1].providers.openai!.accts, ['acct1'], 'aliases follow hash order, as the API assigns them')
  assert.equal(json.includes('"cc":"GB"'), true)
})

test('one machine is rebuilt exactly from its own rows', async () => {
  const fixtures = files(published())
  const source = githubSource({ fetch: fakeFetch(fixtures), now: () => NOW })
  const data = await source.fetchUsage(new AbortController().signal, () => {})
  assert.deepEqual(data, buildUsage(published(), NOW))
  assert.equal(source.scopeMachine!(data, 'm_cccccccccccc'), null)
  assert.equal(source.scopeMachine!(structuredClone(data), A), null, 'a cached copy has no rows behind it')

  const scopedA = normaliseDays(source.scopeMachine!(data, A))
  assert.deepEqual(scopedA.map(d => d.date), ['2026-09-01', '2026-09-02', '2026-09-03'])
  const a1 = scopedA[0].providers.anthropic!
  assert.equal(tokens(a1.exact), 1365)
  assert.deepEqual(a1.promptsByModel, { 'claude-x': 3, 'claude-y': 1, unknown: 1 }, 'per-model prompts are exact for one schema 2 machine')
  assert.deepEqual(Object.keys(a1.byMachine), [A])
  assert.deepEqual(Object.keys(a1.byCountry), ['GB'])
  const cellsA = buildCells(scopedA, DEFAULT_VIEW)
  assert.equal(activeDayCount(scopedA, DEFAULT_VIEW, FROM, TO), 2)
  assert.equal(summarise(cellsA, FROM, TO).prompts, 6 + 5 + 3)
  assert.equal(cellsA.get('2026-09-02')!.promptsNoUsage, 5)

  const scopedB = normaliseDays(source.scopeMachine!(data, B))
  assert.deepEqual(scopedB.map(d => d.date), ['2026-08-20', '2026-09-01'])
  assert.equal(tokens(scopedB[1].providers.anthropic!.exact), 10)
  assert.equal(scopedB[1].providers.openai, undefined)

  // The page's own split (machines.ts) gets the same machine totals; only the exact rebuild keeps models.
  const split = machineDays(normaliseDays(data), A)
  assert.equal(buildCells(split.days, DEFAULT_VIEW).get('2026-09-01')!.total, buildCells(scopedA, DEFAULT_VIEW).get('2026-09-01')!.total)
  assert.equal(split.partial, true)
})

test('loading: relative paths, progress, a missing index and a failed file', async () => {
  const fixtures = files(published())
  const requested: string[] = []
  const phases: string[] = []
  const source = githubSource({ fetch: fakeFetch(fixtures, requested), now: () => NOW })
  await source.fetchUsage(new AbortController().signal, p => phases.push(p.phase))
  assert.equal(requested[0], 'data/index.json')
  assert.ok(requested.every(path => !path.startsWith('/')), 'relative URLs work under /<repository>/')
  assert.ok(requested.includes(`data/machines/${A}/account-usage.json`))
  assert.equal(phases.at(-1), 'processing')

  const meters = await source.fetchMeters()
  assert.deepEqual(meters.map(m => [m.machine, m.window]), [[A, 'week']])

  await assert.rejects(githubSource({ fetch: fakeFetch({}) }).fetchUsage(new AbortController().signal, () => {}), /Nothing has been published yet/)
  const broken = { ...fixtures }
  delete broken[`data/machines/${B}/usage-2026-08.json`]
  await assert.rejects(githubSource({ fetch: fakeFetch(broken) }).fetchUsage(new AbortController().signal, () => {}), /usage-2026-08\.json \(404\)/,
    'a listed file that fails stops the load (the page keeps its last good data) rather than undercounting')
  assert.equal(await source.fetchPromptCounts(new AbortController().signal), null)
})

test('a schema 1 index (the first template) cannot list account-usage.json: the page looks for it itself', async () => {
  const fixtures = files(published())
  const index = fixtures['data/index.json'] as { schema: number; machines: Array<{ id: string; files: string[] }> }
  const unlisted = { ...index, machines: index.machines.map(m => ({ ...m, files: m.files.filter(f => f !== 'account-usage.json') })) }
  const listed = await githubSource({ fetch: fakeFetch(fixtures), now: () => NOW }).fetchUsage(new AbortController().signal, () => {})

  // Old index: still found (and a 404 for a machine without one is not an error).
  const requested: string[] = []
  const old = await githubSource({ fetch: fakeFetch({ ...fixtures, 'data/index.json': { ...unlisted, schema: 1 } }, requested), now: () => NOW })
    .fetchUsage(new AbortController().signal, () => {})
  assert.ok(requested.includes(`data/machines/${A}/account-usage.json`) && requested.includes(`data/machines/${B}/account-usage.json`))
  assert.deepEqual(old.days, listed.days, 'the same account history as when it is listed')

  // New index: trusted, nothing unlisted is requested.
  const asked: string[] = []
  await githubSource({ fetch: fakeFetch({ ...fixtures, 'data/index.json': { ...unlisted, schema: 2 } }, asked), now: () => NOW })
    .fetchUsage(new AbortController().signal, () => {})
  assert.ok(!asked.some(p => p.endsWith('account-usage.json')))
})

// ---- Parity with the d0m1 API ----

interface Ev {
  kind: 'usage' | 'activity'
  id: string
  machine: string
  cc: string
  provider: Provider
  source: string
  ts: string
  tzOffsetMin: number
  acct: string
  model?: string
  q?: 'exact' | 'estimated'
  in?: number
  cacheW?: number
  cacheW1h?: number
  cacheR?: number
  out?: number
  reasoning?: number
  hasUsage?: boolean
}

/** Local calendar date, as both the server and the collector bucket events. */
const localDate = (e: Ev) => new Date(Date.parse(e.ts) + e.tzOffsetMin * 60_000).toISOString().slice(0, 10)

/** Server: activity sightings merge by id (hasUsage OR-merged), then computeRollup per local date. */
function serverResponse(events: Ev[]): UsageResponse {
  const merged = new Map<string, Ev>()
  for (const e of events) {
    const prev = merged.get(e.id)
    merged.set(e.id, prev && e.kind === 'activity' ? { ...prev, hasUsage: Boolean(prev.hasUsage || e.hasUsage) } : prev ?? e)
  }
  const byDate = new Map<string, Ev[]>()
  for (const e of merged.values()) byDate.set(localDate(e), [...(byDate.get(localDate(e)) ?? []), e])
  const days = [...byDate.keys()].sort().map(date => computeRollup(date, byDate.get(date)!.map(e => ({ calls: 0, ...e }))))
  return { generatedAt: NOW.toISOString(), lastIngestAt: null, days: aliasAccts(days), totals: { accounts: 0, accountsByProvider: {}, machines: 0, machinesLive: 0 } }
}

/** Collector: the rollup's daily rows (rollup.go) as each machine publishes them (ghpub.Files, schema 2). */
function collectorFiles(events: Ev[], prompts: Array<{ id: string; machine: string; provider: Provider; source: string; ts: string; tzOffsetMin: number; acct: string; model: string }>): Published {
  const cells = new Map<string, Record<string, number>>()
  const cell = (machine: string, date: string, e: { provider: string; source: string; acct: string }, model: string, q: string) => {
    const key = [machine, date, e.provider, e.source, model, e.acct, q].join('|')
    if (!cells.has(key)) cells.set(key, { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, events: 0, prompts: 0, promptsNoUsage: 0, modelPrompts: 0 })
    return cells.get(key)!
  }
  const seenUsage = new Set<string>()
  const activity = new Map<string, Ev>()
  for (const e of events) {
    if (e.kind === 'usage') {
      if (seenUsage.has(e.id)) continue
      seenUsage.add(e.id)
      const c = cell(e.machine, localDate(e), e, e.model ?? '', e.q === 'estimated' ? 'e' : '')
      for (const f of ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning'] as const) c[f] += e[f] ?? 0
      c.events++
    } else {
      const prev = activity.get(e.id)
      activity.set(e.id, prev ? { ...prev, hasUsage: Boolean(prev.hasUsage || e.hasUsage) } : e)
    }
  }
  for (const e of activity.values()) {
    const c = cell(e.machine, localDate(e), e, '', '')
    c.prompts++
    if (!e.hasUsage) c.promptsNoUsage++
  }
  const seenPrompts = new Set<string>()
  for (const p of prompts) {
    if (seenPrompts.has(p.id)) continue
    seenPrompts.add(p.id)
    cell(p.machine, new Date(Date.parse(p.ts) + p.tzOffsetMin * 60_000).toISOString().slice(0, 10), p, p.model, '').modelPrompts++
  }
  const byFile = new Map<string, unknown[][]>()
  for (const [key, c] of cells) {
    const [machine, date, provider, source, model, acct, q] = key.split('|')
    const file = `${machine}|${date.slice(0, 7)}`
    byFile.set(file, [...(byFile.get(file) ?? []), [date, provider, source, model, acct, q, c.in, c.cacheW, c.cacheW1h, c.cacheR, c.out, c.reasoning, c.events, c.prompts, c.promptsNoUsage, c.modelPrompts]])
  }
  const ids = [...new Set(events.map(e => e.machine))].sort()
  return {
    generatedAt: NOW.toISOString(),
    machines: ids.map(id => ({
      id, meta: { id, label: id, cc: events.find(e => e.machine === id)!.cc || undefined },
      usage: [...byFile].filter(([file]) => file.startsWith(`${id}|`)).map(([file, rows]) => ({ schema: 2, month: file.split('|')[1], cols: COLS2, rows })),
    })),
  }
}

function synthetic() {
  const events: Ev[] = []
  const prompts: Parameters<typeof collectorFiles>[1] = []
  const machinesCc: Array<[string, string]> = [[A, 'TH'], [B, '']]
  const models = ['claude-x', 'claude-y', '']
  let n = 0
  // Three weeks across a month boundary, two machines in different time zones (one east of UTC, one west).
  for (let day = 0; day < 21; day++) {
    for (const [mi, [machine, cc]] of machinesCc.entries()) {
      const tz = mi === 0 ? 420 : -300
      const quiet = (day + mi) % 5 === 0
      const promptOnly = (day + mi) % 7 === 3
      for (let k = 0; k < 4; k++) {
        const ts = new Date(Date.UTC(2026, 7, 25 + day, (k * 7 + day + mi * 3) % 24, 13 * k % 60)).toISOString()
        const provider: Provider = k === 3 ? 'openai' : 'anthropic'
        const source = provider === 'openai' ? 'codex' : 'claude-code'
        const acct = k === 2 ? H2 : H1
        if (quiet) continue
        const id = `ev${n++}`
        if (!promptOnly) {
          const usage: Ev = { kind: 'usage', id: `u${id}`, machine, cc, provider, source, ts, tzOffsetMin: tz, acct, model: provider === 'openai' ? 'gpt-z' : models[(day + k) % 3],
            q: (day + k) % 4 === 0 ? 'estimated' : 'exact', in: 100 + day * 7 + k, cacheW: 10 * k, cacheW1h: k, cacheR: 1000 + day, out: 50 + k, reasoning: k }
          events.push(usage, { ...usage }) // a re-read never counts twice
        }
        // Each prompt: seen first without usage; most are seen again once their tokens are recorded.
        const recorded = !promptOnly && (day + k) % 3 !== 0
        events.push({ kind: 'activity', id: `a${id}`, machine, cc, provider, source, ts, tzOffsetMin: tz, acct, hasUsage: false })
        if (recorded) events.push({ kind: 'activity', id: `a${id}`, machine, cc, provider, source, ts, tzOffsetMin: tz, acct, hasUsage: true })
        prompts.push({ id: `p${id}`, machine, provider, source, ts, tzOffsetMin: tz, acct, model: provider === 'openai' ? 'gpt-z' : models[(day + k) % 3] })
      }
    }
  }
  // A day with prompts on both machines and no token records at all.
  for (const [machine, cc] of machinesCc) {
    events.push({ kind: 'activity', id: `late-${machine}`, machine, cc, provider: 'anthropic', source: 'claude-code', ts: '2026-09-20T12:00:00Z', tzOffsetMin: 0, acct: H1, hasUsage: false })
  }
  return { events, prompts }
}

test('parity: the published rows give the API rollup’s days, prompts, unrecorded prompts and active days', () => {
  const { events, prompts } = synthetic()
  const server = normaliseDays(serverResponse(events))
  const pages = normaliseDays(buildUsage(collectorFiles(events, prompts), NOW))
  assert.ok(server.length > 15)
  assert.deepEqual(pages.map(d => d.date), server.map(d => d.date))
  const pick = (days: NormDay[]) => days.map(d => ({
    date: d.date,
    providers: Object.fromEntries(Object.entries(d.providers).map(([provider, p]) => [provider, {
      exact: p!.exact, estimated: p!.estimated, prompts: p!.prompts, promptsNoUsage: p!.promptsNoUsage,
      accts: p!.accts, models: p!.models, byMachine: p!.byMachine, byCountry: p!.byCountry,
    }])),
  }))
  assert.deepEqual(pick(pages), pick(server), 'every bucket, model, place and alias matches the API rollup')

  const from = server[0].date
  const to = server.at(-1)!.date
  for (const view of [DEFAULT_VIEW, { ...DEFAULT_VIEW, exactOnly: true }, { ...DEFAULT_VIEW, provider: 'openai' as const }, { ...DEFAULT_VIEW, metric: 'output' as const }]) {
    const s = summarise(buildCells(server, view), from, to)
    const p = summarise(buildCells(pages, view), from, to)
    assert.deepEqual(p, s)
    assert.equal(activeDayCount(pages, view, from, to), activeDayCount(server, view, from, to))
  }
  const serverCells = buildCells(server, DEFAULT_VIEW)
  const untracked = [...serverCells.values()].filter(c => c.total === 0 && c.promptsNoUsage > 0)
  assert.ok(untracked.length > 0, 'the fixture has prompt-only days')
  assert.ok([...serverCells.values()].some(c => c.promptsNoUsage > 0 && c.promptsNoUsage < c.prompts), 'and partly recorded days')
  assert.ok(activeDayCount(server, DEFAULT_VIEW, from, to) < serverCells.size)
  // Per-model prompt records: every prompt counted once, under its model.
  const recordsByDay = pages.reduce((sum, d) => sum + Object.values(d.providers).reduce((n, p) => n + Object.values(p!.promptsByModel ?? {}).reduce((a, b) => a + b, 0), 0), 0)
  assert.equal(recordsByDay, prompts.length)
})

// ---- Fixture plumbing ----

/** The repository's files as the Pages site serves them. */
function files(p: Published): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  const index = { schema: 1, title: p.title, generatedAt: p.generatedAt, machines: [] as Array<{ id: string; files: string[] }> }
  for (const m of p.machines) {
    const names: string[] = []
    const put = (name: string, body: unknown) => { names.push(name); out[`data/machines/${m.id}/${name}`] = body }
    if (m.meta) put('meta.json', m.meta)
    for (const u of m.usage) put(`usage-${u.month}.json`, u)
    if (m.accountUsage) put('account-usage.json', m.accountUsage)
    if (m.id === A) put('quota.json', { schema: 1, machine: A, meters: [{ provider: 'anthropic', source: 'claude-code', acct: H1, window: 'week', usedPercent: 40, observedAt: '2026-09-30T11:00:00Z' }] })
    index.machines.push({ id: m.id, files: names.sort() })
  }
  out['data/index.json'] = index
  return out
}

function fakeFetch(fixtures: Record<string, unknown>, requested: string[] = []): typeof fetch {
  return async (input: string | URL | Request) => {
    const path = String(input)
    requested.push(path)
    if (!(path in fixtures)) return new Response('not found', { status: 404 })
    return new Response(JSON.stringify(fixtures[path]), { headers: { 'content-type': 'application/json' } })
  }
}
