// Account history on the GitHub dashboard: the port against the d0m1 API's own reconciliation, and the whole path
// from the collector's published files (usage rows + account-usage.json) to the page against the API's response
// for the same events. Run: node --experimental-strip-types --no-warnings pages/dashboard/accountUsage.test.ts
import assert from 'node:assert/strict'
import test from 'node:test'
// @ts-expect-error -- plain JS module without types (the d0m1 API's account history).
import { accountLedger, reconcileAccountUsage as serverReconcile } from '../../api/src/lib/account-usage.js'
// @ts-expect-error -- plain JS module without types (the d0m1 API's rollup).
import { aliasAccts, computeRollup, hasData } from '../../api/src/lib/rollup.js'
import type { Provider, RollupDay } from '../../src/components/agents/types.ts'
import { addAssignedHistory, attributeAccountHistory } from '../../src/components/agents/attribution.ts'
import { activeDayCount, buildCells, DEFAULT_VIEW, normaliseDays, placeTotals, summarise, type NormDay } from '../../src/components/agents/usage.ts'
import { reconcileAccountUsage, surroundingDates, datesBetween, type AccountSnapshot, type LedgerDay } from './accountUsage.ts'
import { buildUsage, readLedger, readSnapshots, type MachineFiles, type Published } from './githubSource.ts'

const NOW = new Date('2026-09-30T12:00:00Z')
const TODAY = '2026-09-30'
const A = 'm_aaaaaaaaaaaa'
const B = 'm_bbbbbbbbbbbb'
const C = 'm_cccccccccccc'
// Account hashes as collectors publish them: they must never reach the page.
const H1 = 'a_1111111111111111'
const H2 = 'a_2222222222222222'
const COLS2 = ['date', 'provider', 'source', 'model', 'acct', 'q', 'in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'events', 'prompts', 'promptsNoUsage', 'modelPrompts']
const LEDGER_COLS = ['date', 'provider', 'source', 'acct', 'tokens']
// The collector and the API: attribution of at least this quality keeps its account in the ledger.
const LABELLED = new Set(['recorded', 'session', 'timeline', 'bounded'])

interface Ev {
  id: string
  machine: string
  cc: string
  provider: Provider
  source: string
  ts: string
  tzOffsetMin: number
  acct: string
  acctQ: string
  model: string
  q: 'exact' | 'estimated'
  in: number
  cacheW: number
  cacheR: number
  out: number
}

const localDate = (e: Ev) => new Date(Date.parse(e.ts) + e.tzOffsetMin * 60_000).toISOString().slice(0, 10)
const utcDate = (e: Ev) => e.ts.slice(0, 10)
const sum = (e: Ev) => e.in + e.cacheW + e.cacheR + e.out
const snap = (date: string, acct: string, totalTokens: number, observedAt: string): AccountSnapshot =>
  ({ provider: 'openai', source: 'codex', acct, date, totalTokens, observedAt })

/** Codex events on two machines on opposite sides of UTC, so local dates and UTC days differ. */
function events(): Ev[] {
  const out: Ev[] = []
  let n = 0
  const zones: Array<[string, string, number]> = [[A, 'JP', 540], [B, '', -420]]
  for (let day = 0; day < 21; day++) {
    for (const [mi, [machine, cc, tz]] of zones.entries()) {
      if ((day + mi) % 6 === 0) continue // quiet days
      for (let k = 0; k < 3; k++) {
        const ts = new Date(Date.UTC(2026, 8, 10 + day, (k * 9 + day * 5 + mi * 7) % 24, 7 * k)).toISOString()
        if (Date.parse(ts) > NOW.getTime()) continue
        // H2 is the account with history; H1 has a few calls; some calls carry only a weak candidate (uncertain).
        const acct = k === 2 && day % 4 === 0 ? H1 : H2
        const acctQ = (day + k) % 5 === 0 ? 'inferred' : mi === 0 ? 'recorded' : 'bounded'
        out.push({ id: `u${n++}`, machine, cc, provider: 'openai', source: 'codex', ts, tzOffsetMin: tz, acct, acctQ,
          model: k === 1 ? 'gpt-y' : 'gpt-z', q: (day + k) % 7 === 0 ? 'estimated' : 'exact',
          in: 1000 + day * 31 + k, cacheW: 0, cacheR: 5000 + day * 7, out: 300 + k * 11 })
      }
    }
  }
  // Another provider on the same days: never part of the codex reconciliation.
  out.push({ id: 'claude-1', machine: A, cc: 'JP', provider: 'anthropic', source: 'claude-code', ts: '2026-09-15T03:00:00Z', tzOffsetMin: 540,
    acct: H1, acctQ: 'recorded', model: 'claude-x', q: 'exact', in: 50, cacheW: 5, cacheR: 500, out: 20 })
  return out
}

/** The UTC-day local tokens per account the API's ledger holds (the same sum both sides compute). */
function ledgerTotals(evs: Ev[]) {
  const totals = new Map<string, number>()
  for (const e of evs) {
    const key = `${utcDate(e)}|${e.source}|${LABELLED.has(e.acctQ) ? e.acct : ''}`
    totals.set(key, (totals.get(key) ?? 0) + sum(e))
  }
  return totals
}

/**
 * Provider account totals for H2 (two machines report them, at different times) and H1:
 *  - H2's closed history has cloud usage on top of the local tokens on some days, one day with no local tokens at
 *    all, and equal totals on others. Today's total lags the ongoing local calls (its own window: no conflict).
 *  - H1's closed history conflicts: one day reports far less than was recorded locally, another more.
 */
function snapshots(evs: Ev[]): { a: AccountSnapshot[]; b: AccountSnapshot[] } {
  const local = ledgerTotals(evs)
  const a: AccountSnapshot[] = []
  const b: AccountSnapshot[] = []
  for (const date of datesBetween('2026-09-08', TODAY)) {
    const matched = local.get(`${date}|codex|${H2}`) ?? 0
    const uncertain = local.get(`${date}|codex|`) ?? 0
    const extra = date === TODAY ? -500 : date.endsWith('2') ? 0 : 12_345 + date.charCodeAt(9) * 100
    a.push(snap(date, H2, Math.max(0, matched + uncertain + extra), '2026-09-30T10:00:00.000Z'))
    // Machine B read some days an hour later, with corrected (lower) totals: the newest reading wins.
    if (date >= '2026-09-20' && date < TODAY) b.push(snap(date, H2, matched + uncertain + Math.max(0, extra - 1000), '2026-09-30T11:00:00.000Z'))
    // And an older reading of a few days, which never wins.
    if (date < '2026-09-12') b.push(snap(date, H2, 1, '2026-09-29T00:00:00.000Z'))
  }
  const h1Days = [...local.keys()].filter(k => k.endsWith(`|codex|${H1}`)).map(k => k.slice(0, 10)).sort()
  a.push(snap(h1Days[0], H1, 10, '2026-09-30T10:00:00.000Z'))
  a.push(snap(h1Days[1], H1, (local.get(`${h1Days[1]}|codex|${H1}`) ?? 0) + 50_000, '2026-09-30T10:00:00.000Z'))
  return { a, b }
}

/** GET /api/usage for these events and totals: rollups per local date with their ledgers, the dates ingest covers, reconciliation, aliases. */
function serverResponse(evs: Ev[], snaps: AccountSnapshot[]) {
  const byDate = new Map<string, Ev[]>()
  for (const e of evs) byDate.set(localDate(e), [...(byDate.get(localDate(e)) ?? []), e])
  const toServer = (e: Ev) => ({ kind: 'usage', calls: 0, ...e })
  const days = new Map<string, LedgerDay>([...byDate].map(([date, list]) =>
    [date, { ...computeRollup(date, list.map(toServer)), accountLedger: accountLedger(list.map(toServer)) }]))
  // Ingest recomputes every date around each account window, so each has a ledger (empty when nothing ran).
  const dates = snaps.map(s => s.date).sort()
  for (const date of datesBetween(surroundingDates(dates[0])[0], surroundingDates(dates.at(-1)!)[2])) {
    if (!days.has(date)) days.set(date, { ...computeRollup(date, []), accountLedger: accountLedger([]) })
  }
  // The accounts table keeps one row per day, source and account: upsertAccountUsage's newest-wins rule.
  const stored = new Map<string, AccountSnapshot>()
  for (const s of snaps) {
    const key = `${s.date}|${s.source}|${s.acct}`
    const row = stored.get(key)
    if (row && row.observedAt > s.observedAt) continue
    if (row && row.observedAt === s.observedAt && row.totalTokens >= s.totalTokens) continue
    stored.set(key, s)
  }
  const reconciled = serverReconcile([...days.values()].sort((x, y) => x.date.localeCompare(y.date)), [...stored.values()], NOW)
  return { days: aliasAccts(reconciled.days.filter(hasData)) as RollupDay[], pending: reconciled.pending as number, conflicts: reconciled.conflicts as number }
}

/** What each machine's collector publishes: daily rows (rollup.go) and account-usage.json (ghpub.Files). */
function collectorFiles(evs: Ev[], published: Record<string, AccountSnapshot[]>, withLedger: (machine: string) => boolean = () => true): Published {
  const machines = [...new Set([...evs.map(e => e.machine), ...Object.keys(published)])].sort()
  return {
    generatedAt: NOW.toISOString(),
    machines: machines.map((id): MachineFiles => {
      const mine = evs.filter(e => e.machine === id)
      const cells = new Map<string, number[]>()
      for (const e of mine) {
        const key = [localDate(e), e.provider, e.source, e.model, e.acct, e.q === 'estimated' ? 'e' : ''].join('|')
        const c = cells.get(key) ?? [0, 0, 0, 0, 0]
        c[0] += e.in; c[1] += e.cacheW; c[2] += e.cacheR; c[3] += e.out; c[4]++
        cells.set(key, c)
      }
      const byMonth = new Map<string, unknown[][]>()
      for (const [key, [inT, cacheW, cacheR, out, n]] of cells) {
        const [date, provider, source, model, acct, q] = key.split('|')
        byMonth.set(date.slice(0, 7), [...(byMonth.get(date.slice(0, 7)) ?? []), [date, provider, source, model, acct, q, inT, cacheW, 0, cacheR, out, 0, n, 0, 0, 0]])
      }
      const ledger = new Map<string, number>()
      for (const e of mine.filter(e => e.source === 'codex')) {
        const key = [utcDate(e), e.provider, e.source, LABELLED.has(e.acctQ) ? e.acct : ''].join('|')
        ledger.set(key, (ledger.get(key) ?? 0) + sum(e))
      }
      const cc = mine[0]?.cc
      return {
        id,
        meta: { id, label: id, ...(cc ? { cc } : {}) },
        usage: [...byMonth].map(([month, rows]) => ({ schema: 2, month, cols: COLS2, rows })),
        accountUsage: published[id] || withLedger(id) ? {
          schema: 2,
          snapshots: (published[id] ?? []).map(s => ({ ...s, observedAt: s.observedAt.replace('.000Z', 'Z') })),
          ...(withLedger(id) ? { ledgerCols: LEDGER_COLS, ledger: [...ledger].map(([key, tokens]) => [...key.split('|'), tokens]) } : {}),
        } : null,
      }
    }),
  }
}

/** The fields the page shows, per provider, with accounts in a stable order. */
const pick = (days: NormDay[] | RollupDay[]) => days.map(d => ({
  date: d.date,
  providers: Object.fromEntries(Object.entries(d.providers).map(([provider, p]) => [provider, {
    exact: p!.exact, estimated: p!.estimated, prompts: p!.prompts, promptsNoUsage: p!.promptsNoUsage, accts: [...p!.accts].sort(),
    models: p!.models, byMachine: p!.byMachine, byCountry: p!.byCountry, accountUsage: p!.accountUsage,
  }])),
}))

/** The server's output without the workspace split the page does not have. */
const withoutWorkspaces = (days: LedgerDay[]) => days.map(d => ({
  ...d, providers: Object.fromEntries(Object.entries(d.providers).map(([name, p]) => {
    const rest = { ...p! }
    delete rest.byWorkspace
    return [name, rest]
  })),
}))

test('the port reconciles exactly as api/src/lib/account-usage.js', () => {
  const evs = events()
  const { a, b } = snapshots(evs)
  const byDate = new Map<string, Ev[]>()
  for (const e of evs) byDate.set(localDate(e), [...(byDate.get(localDate(e)) ?? []), e])
  const days: LedgerDay[] = [...byDate].sort().map(([date, list]) => {
    const toServer = list.map(e => ({ kind: 'usage', calls: 0, ...e }))
    return { ...computeRollup(date, toServer), accountLedger: accountLedger(toServer) }
  })
  for (const date of datesBetween('2026-09-07', '2026-10-01')) {
    if (!days.some(d => d.date === date)) days.push({ ...computeRollup(date, []), accountLedger: accountLedger([]) })
  }
  days.sort((x, y) => x.date.localeCompare(y.date))
  const cases: Array<[string, LedgerDay[], AccountSnapshot[]]> = [
    ['reconciled, conflicting, live and duplicate totals', days, [...a, ...b]],
    ['no totals', days, []],
    // An old rollup without a v3 ledger around a total keeps that window pending.
    ['a day without a ledger', days.map(d => d.date === '2026-09-15' ? { ...d, accountLedger: { v: 2, entries: [] } } : d), [...a, ...b]],
    ['a total on a date with no rollup at all', days.filter(d => d.date !== '2026-09-08'), a],
  ]
  for (const [name, input, snaps] of cases) {
    const before = structuredClone(input)
    const server = serverReconcile(input, snaps, NOW)
    // The page's days have no workspace split; everything else must match field for field.
    const port = reconcileAccountUsage(withoutWorkspaces(input), snaps, NOW)
    assert.deepEqual(withoutWorkspaces(port.days), withoutWorkspaces(server.days), name)
    assert.deepEqual([port.pending, port.conflicts], [server.pending, server.conflicts], name)
    assert.deepEqual(input, before, 'inputs are never mutated')
  }
  const result = reconcileAccountUsage(withoutWorkspaces(days), [...a, ...b], NOW)
  assert.equal(result.conflicts, 1, 'the fixture has a conflicting window')
  assert.ok(result.days.some(d => (d.providers.openai?.exact.unattributed ?? 0) > 0), 'and recovered history')
})

test('published files reconcile on the page exactly as the API reconciles the same events', () => {
  const evs = events()
  const { a, b } = snapshots(evs)
  const server = serverResponse(evs, [...a, ...b])
  const page = buildUsage(collectorFiles(evs, { [A]: a, [B]: b }), NOW)
  assert.deepEqual([page.accountUsagePending, page.accountUsageConflicts], [server.pending, server.conflicts])
  assert.equal(page.accountUsageConflicts, 1)
  assert.equal(page.accountUsagePending, 0)

  const serverDays = normaliseDays({ generatedAt: '', lastIngestAt: null, days: server.days, totals: { accounts: 0, accountsByProvider: {}, machines: 0, machinesLive: 0 } })
  const pageDays = normaliseDays(page)
  assert.deepEqual(pageDays.map(d => d.date), serverDays.map(d => d.date))
  assert.deepEqual(pick(pageDays), pick(serverDays), 'every bucket, account history record, model and place matches')
  assert.equal(page.firstDate, server.days[0].date, 'a total before the first local day starts the history')

  for (const view of [DEFAULT_VIEW, { ...DEFAULT_VIEW, exactOnly: true }, { ...DEFAULT_VIEW, provider: 'openai' as const }]) {
    const s = summarise(buildCells(serverDays, view), '2026-09-01', TODAY)
    const p = summarise(buildCells(pageDays, view), '2026-09-01', TODAY)
    assert.deepEqual(p, s)
    assert.equal(activeDayCount(pageDays, view, '2026-09-01', TODAY), activeDayCount(serverDays, view, '2026-09-01', TODAY))
  }
  const summary = summarise(buildCells(pageDays, DEFAULT_VIEW), '2026-09-01', TODAY)
  assert.ok(summary.unattributed > 100_000, 'recovered account history counts in the period')

  // Spot checks against the fixture's construction.
  const sept8 = pageDays.find(d => d.date === '2026-09-08')!.providers.openai!
  assert.equal(sept8.exact.unattributed, a.find(s => s.date === '2026-09-08')!.totalTokens, 'a day with no local tokens recovers its whole total')
  assert.deepEqual(sept8.accts, ['acct2'])
  assert.deepEqual(Object.keys(sept8.byMachine), ['unknown'])
  const today = pageDays.find(d => d.date === TODAY)?.providers.openai?.accountUsage ?? []
  assert.ok(today.every(r => r.recoveredTokens === 0), 'a lagging live total adds nothing')
  const json = JSON.stringify(page)
  for (const hash of [H1, H2]) assert.equal(json.includes(hash), false, 'no account hash reaches the page')
  assert.equal(json.includes('accountLedger'), false, 'the ledger stays private to the reconciliation')
  assert.equal(page.totals.accounts, 2)
  assert.equal(page.totals.accountsByProvider.openai, 2)
})

test('a machine that publishes no ledger keeps the totals it may overlap pending', () => {
  const evs = events()
  const { a } = snapshots(evs)
  // Machine C runs an older collector: codex rows, no account-usage.json.
  const older: Ev = { ...evs.find(e => e.machine === A && e.source === 'codex')!, id: 'c-1', machine: C, cc: '', ts: '2026-09-15T12:00:00Z', tzOffsetMin: 0 }
  const page = buildUsage(collectorFiles([...evs, older], { [A]: a }, id => id !== C), NOW)
  // Every closed window that reaches C's local day (or a day either side of it) waits, whole.
  const touches = (acct: string) => {
    const dates = a.filter(s => s.acct === acct && s.date < TODAY).map(s => s.date).sort()
    return surroundingDates(dates[0])[0] <= '2026-09-16' && surroundingDates(dates.at(-1)!)[2] >= '2026-09-14' ? dates.length : 0
  }
  const closed = touches(H1) + touches(H2)
  assert.ok(touches(H2) > 20, "H2's closed window touches C's day: all of it waits")
  assert.equal(page.accountUsagePending, closed)
  assert.ok(normaliseDays(page).every(d => !d.providers.openai?.accountUsage?.some(r => r.source === 'codex' && r.recoveredTokens > 0 && d.date < TODAY && d.date >= '2026-09-14' && d.date <= '2026-09-16')))
  // Only reconciled sources matter: a machine with Claude Code rows alone and no ledger changes nothing.
  const claudeOnly = buildUsage(collectorFiles([...evs, { ...evs.at(-1)!, id: 'c-2', machine: C }], { [A]: a }, id => id !== C), NOW)
  assert.equal(claudeOnly.accountUsagePending, 0)
  // A machine with codex tokens but neither totals nor a ledger file at all: the same.
  const files = collectorFiles([...evs, older], { [A]: a })
  files.machines.find(m => m.id === C)!.accountUsage = null
  assert.equal(buildUsage(files, NOW).accountUsagePending, closed)
  // No ledger anywhere (snapshots only, as early files had): nothing is added.
  const bare = collectorFiles(evs, { [A]: a }, () => false)
  const none = buildUsage(bare, NOW)
  assert.equal(none.accountUsagePending, a.length)
  assert.ok(normaliseDays(none).every(d => !d.providers.openai?.exact.unattributed))
})

test('a local date a machine lists as unledgered keeps the totals it may overlap pending', () => {
  const evs = events()
  const { a } = snapshots(evs)
  const older: Ev = { ...evs.find(e => e.machine === A && e.source === 'codex')!, id: 'c-1', machine: C, cc: '', ts: '2026-09-15T12:00:00Z', tzOffsetMin: 0 }
  const noLedger = buildUsage(collectorFiles([...evs, older], { [A]: a }, id => id !== C), NOW)
  // C's totals for that date came from an older rollup: in its rows, not in its (otherwise complete) ledger.
  const files = collectorFiles([...evs, older], { [A]: a })
  const c = files.machines.find(m => m.id === C)!
  c.accountUsage = { schema: 2, snapshots: [], ledgerCols: LEDGER_COLS, ledger: [], unledgered: ['2026-09-15'] }
  const page = buildUsage(files, NOW)
  assert.ok((noLedger.accountUsagePending ?? 0) > 0)
  assert.equal(page.accountUsagePending, noLedger.accountUsagePending, 'the same as a machine with no ledger at all')
  // Any other date (or a malformed list) leaves that day covered by the ledger.
  for (const unledgered of [['2026-08-01'], 'nonsense', [15]]) {
    c.accountUsage = { schema: 2, snapshots: [], ledgerCols: LEDGER_COLS, ledger: [], unledgered: unledgered as never }
    const covered = buildUsage(files, NOW)
    delete c.accountUsage.unledgered
    assert.equal(covered.accountUsagePending, buildUsage(files, NOW).accountUsagePending, JSON.stringify(unledgered))
  }
})

test('one machine’s own rows hold no account history (the page adds its estimated share)', () => {
  const evs = events()
  const { a, b } = snapshots(evs)
  const published = collectorFiles(evs, { [A]: a, [B]: b })
  const scoped = buildUsage(published, NOW, { machine: A })
  assert.equal(scoped.days.some(d => Object.values(d.providers).some(p => p!.accountUsage || p!.exact.unattributed)), false)
  assert.deepEqual([scoped.accountUsagePending, scoped.accountUsageConflicts], [0, 0])
  assert.equal(scoped.days.every(d => d.providers.openai === undefined || Object.keys(d.providers.openai.byMachine).join() === A), true)
})

test('account history is placed on machines and regions as d0m1.com places it, and machine views add up', () => {
  const evs = events()
  const { a, b } = snapshots(evs)
  const published = collectorFiles(evs, { [A]: a, [B]: b })
  const page = buildUsage(published, NOW)
  const fleet = attributeAccountHistory(normaliseDays(page), page.machines!)
  const server = serverResponse(evs, [...a, ...b])
  const serverDays = attributeAccountHistory(normaliseDays({ generatedAt: '', lastIngestAt: null, days: server.days,
    totals: { accounts: 0, accountsByProvider: {}, machines: 0, machinesLive: 0 } }), page.machines!)
  const places = (days: NormDay[], field: 'byMachine' | 'byCountry') =>
    placeTotals(days, DEFAULT_VIEW, '2026-09-01', TODAY, field).map(t => [t.key, t.value, t.assigned])
  for (const field of ['byMachine', 'byCountry'] as const) {
    assert.deepEqual(places(fleet, field), places(serverDays, field), `${field}: the page and the API place it alike`)
  }
  const machines = places(fleet, 'byMachine')
  assert.deepEqual(machines.map(([key]) => key).sort(), [A, B], 'no unknown machine')
  assert.ok(machines.every(([, , assigned]) => (assigned as number) > 0))
  assert.deepEqual(places(fleet, 'byCountry').map(([key]) => key).sort(), ['JP', 'ZZ'], 'B publishes no country: its own region bucket')
  const total = (days: NormDay[]) => summarise(buildCells(days, DEFAULT_VIEW), '2026-09-01', TODAY).total
  assert.equal(total(fleet), total(normaliseDays(page)), 'totals unchanged')
  // One machine's view: its own rows plus its share; the views add up to the fleet.
  let views = 0
  for (const m of page.machines!) {
    const own = normaliseDays(buildUsage(published, NOW, { machine: m.id }))
    views += total(addAssignedHistory(own, fleet, m.id, m.cc || 'ZZ'))
  }
  assert.equal(views, total(fleet))
})

test('account-usage.json is read by name and validated', () => {
  const file = {
    schema: 2,
    snapshots: [
      { provider: 'openai', source: 'codex', acct: H2, date: '2026-09-01', totalTokens: 10, observedAt: '2026-09-02T00:00:00Z' },
      { provider: 'acme', source: 'codex', acct: H2, date: '2026-09-01', totalTokens: 10, observedAt: '2026-09-02T00:00:00Z' },
      { provider: 'openai', source: 'codex', acct: '', date: '2026-09-01', totalTokens: 10, observedAt: '2026-09-02T00:00:00Z' },
      { provider: 'openai', source: 'codex', acct: H2, date: '2026-9-1', totalTokens: 10, observedAt: '2026-09-02T00:00:00Z' },
      { provider: 'openai', source: 'codex', acct: H2, date: '2026-09-01', totalTokens: -1, observedAt: '2026-09-02T00:00:00Z' },
      { provider: 'openai', source: 'codex', acct: H2, date: '2026-09-01', totalTokens: 10, observedAt: 'soon' },
      'nonsense',
    ],
    ledgerCols: ['tokens', 'acct', 'source', 'provider', 'date'],
    ledger: [[7, H2, 'codex', 'openai', '2026-09-01'], [3, '', 'codex', 'openai', 'bad'], 'x'],
  }
  assert.deepEqual(readSnapshots(file as never), [{ provider: 'openai', source: 'codex', acct: H2, date: '2026-09-01', totalTokens: 10, observedAt: '2026-09-02T00:00:00.000Z' }])
  assert.deepEqual(readLedger(file as never), [{ date: '2026-09-01', provider: 'openai', source: 'codex', acct: H2, totalTokens: 7 }])
  assert.equal(readLedger({ schema: 2, snapshots: [] }), null, 'no ledger: no coverage')
  assert.equal(readLedger({ schema: 2, ledgerCols: ['date'], ledger: [] }), null, 'a ledger without its columns covers nothing')
  assert.deepEqual(readLedger({ schema: 2, ledgerCols: LEDGER_COLS, ledger: [] }), [], 'an empty ledger covers: nothing ran')
})
