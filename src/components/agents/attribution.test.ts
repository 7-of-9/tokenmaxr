import assert from 'node:assert/strict'
import { buildActivity } from './activity.ts'
import { addAssignedHistory, attributeAccountHistory, attributionFor, providerActivity, splitTokens } from './attribution.ts'
import { machineDays } from './machines.ts'
import { activeDayCount, addDays, buildCells, DEFAULT_VIEW, normaliseDays, placeTotals, summarise, type NormDay } from './usage.ts'
import type { PlaceBucket, ProviderDay, PublicMachine, TokenBucket, UsageResponse, WorkspaceDay } from './types.ts'

// Fixtures in the API's shape: local Codex tokens per machine, and account history (recoveredTokens) filed under
// machine "unknown" / country ZZ in the provider and in its byWorkspace.unknown group, as account-usage.js adds it.
const bucket = (out = 0, unattributed = 0): TokenBucket => ({ in: 0, out, unattributed, cacheW: 0, cacheW1h: 0, cacheR: 0, reasoning: 0, calls: 0, events: 0 })
const place = (out = 0, unattributed = 0, prompts = 0): PlaceBucket => ({ in: 0, out, unattributed, cacheW: 0, cacheR: 0, prompts })
const machines: PublicMachine[] = [
  { id: 'm_mac', label: 'Mac', cc: 'TH', os: 'darwin', live: false, lastSeenAt: null },
  { id: 'm_beast', label: 'BEAST', cc: 'GB', os: 'windows', live: false, lastSeenAt: null },
  { id: 'm_nocc', label: 'No country', cc: '', os: 'linux', live: false, lastSeenAt: null },
]
const CC: Record<string, string> = { m_mac: 'TH', m_beast: 'GB', m_nocc: 'ZZ' }

/**
 * One provider day: `local` machine → recorded tokens (one workspace and one prompt each), plus `history` account
 * tokens, and `silent` machine → prompts without recorded tokens (deleted or missing logs), in that machine's workspace.
 */
function providerDay(local: Record<string, number>, history = 0, accounts = 1, silent: Record<string, number> = {}): ProviderDay {
  const groups: Record<string, WorkspaceDay> = {}
  const byMachine: Record<string, PlaceBucket> = {}
  const byCountry: Record<string, PlaceBucket> = {}
  let total = 0
  let prompts = 0
  let promptsNoUsage = 0
  for (const id of new Set([...Object.keys(local), ...Object.keys(silent)])) {
    const tokens = local[id] ?? 0
    const all = (id in local ? 1 : 0) + (silent[id] ?? 0)
    const noUsage = silent[id] ?? 0
    total += tokens
    prompts += all
    promptsNoUsage += noUsage
    byMachine[id] = place(tokens, 0, all)
    const cc = CC[id] ?? 'ZZ'
    byCountry[cc] = place((byCountry[cc]?.out ?? 0) + tokens, 0, (byCountry[cc]?.prompts ?? 0) + all)
    groups[`ws_${id}`] = { exact: bucket(tokens), estimated: bucket(), prompts: all, promptsNoUsage: noUsage,
      models: { exact: tokens ? { 'gpt-5.5': bucket(tokens) } : {}, estimated: {} },
      byMachine: { [id]: { ...place(tokens, 0, all), promptsNoUsage: noUsage } }, byCountry: { [cc]: { ...place(tokens, 0, all), promptsNoUsage: noUsage } } }
  }
  const p: ProviderDay = { exact: bucket(total, history), estimated: bucket(), prompts, promptsNoUsage,
    accts: Array.from({ length: accounts }, (_, i) => `acct${i + 1}`),
    models: { exact: { ...(total ? { 'gpt-5.5': bucket(total) } : {}), ...(history ? { unknown: bucket(0, history) } : {}) }, estimated: {} },
    byMachine, byCountry, byWorkspace: groups }
  if (history) {
    byMachine.unknown = place(0, history)
    byCountry.ZZ = { ...(byCountry.ZZ ?? place()), unattributed: history }
    groups.unknown = { exact: bucket(0, history), estimated: bucket(), prompts: 0, promptsNoUsage: 0,
      models: { exact: { unknown: bucket(0, history) }, estimated: {} }, byMachine: { unknown: { ...place(0, history), promptsNoUsage: 0 } },
      byCountry: { ZZ: { ...place(0, history), promptsNoUsage: 0 } } }
    // One record per account: the API reconciles each account separately and sums the recovered tokens.
    p.accountUsage = Array.from({ length: accounts }, () => ({ source: 'codex', timezone: 'UTC' as const, totalTokens: history / accounts,
      matchedTokens: 0, uncertainTokens: 0, recoveredTokens: history / accounts, observedAt: '2026-10-01T00:00:00Z' }))
  }
  return p
}

/** The same days as the GitHub Pages dashboard builds them: no workspace split. */
const withoutWorkspaces = (days: NormDay[]): NormDay[] => days.map(day => ({ ...day, providers: Object.fromEntries(Object.entries(day.providers).map(([name, p]) => {
  const rest = { ...p! }
  delete rest.byWorkspace
  return [name, rest]
})) }))

const response = (days: Array<[string, ProviderDay, ProviderDay?]>): UsageResponse => ({
  generatedAt: '', lastIngestAt: null, machines,
  days: days.map(([date, openai, anthropic]) => ({ date, providers: { openai, ...(anthropic ? { anthropic } : {}) } })),
  totals: { accounts: 1, accountsByProvider: {}, machines: machines.length, machinesLive: 0 },
})

const total = (days: NormDay[]) => [...buildCells(days, DEFAULT_VIEW).values()].reduce((sum, cell) => sum + cell.total, 0)
const rows = (days: NormDay[], field: 'byMachine' | 'byCountry', from = '2000-01-01', to = '2100-01-01') =>
  Object.fromEntries(placeTotals(days, DEFAULT_VIEW, from, to, field).map(t => [t.key, t.value]))
const assigned = (days: NormDay[], field: 'byMachine' | 'byCountry') =>
  Object.fromEntries(placeTotals(days, DEFAULT_VIEW, '2000-01-01', '2100-01-01', field).filter(t => t.assigned).map(t => [t.key, t.assigned]))
const sum = (values: Record<string, number>) => Object.values(values).reduce((a, b) => a + b, 0)

/** Every invariant: totals per day unchanged, machine and region rows add up to them, no unknown left. */
function check(before: NormDay[], after: NormDay[], unknownLeft = false) {
  const cellsBefore = buildCells(before, DEFAULT_VIEW)
  const cellsAfter = buildCells(after, DEFAULT_VIEW)
  assert.equal(cellsAfter.size, cellsBefore.size)
  for (const [date, cell] of cellsBefore) {
    const next = cellsAfter.get(date)!
    for (const field of ['total', 'exact', 'estimated', 'unattributed', 'tokensIn', 'tokensOut', 'prompts'] as const) assert.equal(next[field], cell[field], `${date} ${field}`)
    assert.deepEqual(next.models, cell.models, `${date} models`)
  }
  assert.equal(sum(rows(after, 'byMachine')), total(before), 'machine rows add up to the total')
  assert.equal(sum(rows(after, 'byCountry')), total(before), 'region rows add up to the total')
  assert.equal('unknown' in rows(after, 'byMachine'), unknownLeft)
  for (const day of after) for (const p of Object.values(day.providers)) {
    for (const group of [p!, ...Object.values(p!.byWorkspace ?? {})]) {
      assert.equal(sum(Object.fromEntries(Object.entries(group.byMachine).map(([k, b]) => [k, b.unattributed ?? 0]))), group.exact.unattributed ?? 0, 'workspace groups keep their history')
    }
  }
}

// ---- splitTokens: whole tokens, exact sums ----
assert.deepEqual([...splitTokens(10, new Map([['a', 1], ['b', 1], ['c', 1]]))], [['a', 4], ['b', 3], ['c', 3]])
assert.deepEqual([...splitTokens(7, new Map([['a', 0], ['b', 2]]))], [['b', 7]])
assert.equal(sum(Object.fromEntries(splitTokens(4_940_277_169, new Map([['a', 3_331], ['b', 98_765_432_101], ['c', 7]])))), 4_940_277_169)
assert.equal(splitTokens(0, new Map([['a', 1]])).size, 0)

// ---- (a) the same day: in proportion to each machine's Codex tokens ----
{
  const before = normaliseDays(response([['2026-09-01', providerDay({ m_mac: 300, m_beast: 100 }, 1000)]]))
  const snapshot = structuredClone(before)
  const after = attributeAccountHistory(before, machines)
  assert.deepEqual(before, snapshot, 'the input is never mutated')
  check(before, after)
  assert.deepEqual(rows(after, 'byMachine'), { m_mac: 1050, m_beast: 350 })
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 750, m_beast: 250 })
  assert.deepEqual(rows(after, 'byCountry'), { TH: 1050, GB: 350 }, 'regions follow the machines')
  assert.deepEqual(assigned(after, 'byCountry'), { TH: 750, GB: 250 })
  assert.equal(attributionFor(providerActivity(before, 'openai'), '2026-09-01', 1000)!.rule, 'same-day')
  // A machine without a country keeps its own region bucket, with its share of the history.
  const nocc = attributeAccountHistory(normaliseDays(response([['2026-09-01', providerDay({ m_nocc: 100, m_beast: 100 }, 1000)]])), machines)
  assert.deepEqual(rows(nocc, 'byCountry'), { ZZ: 600, GB: 600 })
  assert.deepEqual(assigned(nocc, 'byCountry'), { ZZ: 500, GB: 500 })
  // Nothing to assign: the same array back.
  const plain = normaliseDays(response([['2026-09-01', providerDay({ m_mac: 1 })]]))
  assert.equal(attributeAccountHistory(plain, machines), plain)
}

// ---- UTC vs local days: no Codex tokens on the UTC day, so the adjacent local days decide ----
{
  const before = normaliseDays(response([
    ['2026-08-30', providerDay({ m_beast: 100 })],
    ['2026-08-31', providerDay({}, 900)],
    ['2026-09-01', providerDay({ m_mac: 200 })],
  ]))
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  assert.equal(attributionFor(providerActivity(before, 'openai'), '2026-08-31', 900)!.rule, 'adjacent-day')
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 600, m_beast: 300 })
  assert.deepEqual(rows(after, 'byMachine', '2026-08-31', '2026-08-31'), { m_mac: 600, m_beast: 300 }, 'the history stays on its own day')
}

// ---- (b) the nearest activity within 7 days, weighted by tokens in that window ----
{
  const before = normaliseDays(response([
    ['2026-09-05', providerDay({ m_mac: 5000 })],
    ['2026-09-07', providerDay({ m_mac: 100 })],
    ['2026-09-10', providerDay({}, 1000)],
    ['2026-09-13', providerDay({ m_beast: 300 })],
  ]))
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  const basis = attributionFor(providerActivity(before, 'openai'), '2026-09-10', 1000)!
  assert.equal(basis.rule, 'nearest')
  assert.deepEqual(Object.fromEntries(basis.shares), { m_mac: 100, m_beast: 300 }, 'the 3-day window, not the busier day 5 days away')
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 250, m_beast: 750 })
}

// ---- (c) nothing within 7 days: the machine with the most Codex tokens overall ----
{
  const before = normaliseDays(response([
    ['2026-07-01', providerDay({ m_mac: 100, m_beast: 900 })],
    ['2026-07-02', providerDay({ m_mac: 500 })],
    ['2026-08-15', providerDay({}, 1000)],
    ['2026-08-23', providerDay({}, 2000)],
  ]))
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  assert.equal(attributionFor(providerActivity(before, 'openai'), '2026-08-15', 1000)!.rule, 'most-used')
  assert.deepEqual(assigned(after, 'byMachine'), { m_beast: 3000 })
  assert.equal(addDays('2026-08-15', -7) > '2026-07-02', true)
}

// ---- Prompts without recorded tokens (deleted or missing logs) explain the history first ----
{
  // 2026-08-30: the Mac logs 300 tokens for its one prompt; 2026-08-31: BEAST logs 1000, the Mac's 2 prompts have no
  // log. The Mac's 2 prompts most probably used 2 × 300: they take that much, the rest follows BEAST's tokens.
  const days = (history: number): NormDay[] => normaliseDays(response([
    ['2026-08-30', providerDay({ m_mac: 300 })],
    ['2026-08-31', providerDay({ m_beast: 1000 }, history, 1, { m_mac: 2 })],
  ]))
  const before = days(800)
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  const activity = providerActivity(before, 'openai')
  assert.deepEqual(activity.get('2026-08-31')!.get('m_mac'), { tokens: 0, prompts: 2, unlogged: 2, expected: 600 })
  assert.equal(attributionFor(activity, '2026-08-31', 800)!.rule, 'same-day')
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 600, m_beast: 200 })
  assert.deepEqual(assigned(after, 'byCountry'), { TH: 600, GB: 200 })
  // Less history than those prompts most probably used: all of it is theirs, however much BEAST logged.
  assert.deepEqual(assigned(attributeAccountHistory(days(500), machines), 'byMachine'), { m_mac: 500 })
  // Without the workspace split (GitHub Pages) the day's count of such prompts goes to the machine that logged none.
  assert.deepEqual(assigned(attributeAccountHistory(withoutWorkspaces(before), machines), 'byMachine'), { m_mac: 600, m_beast: 200 })
}
{
  // Only the Mac was active on the day, with no logs: it takes everything, though BEAST logged a lot the day after.
  const before = normaliseDays(response([
    ['2026-08-22', providerDay({ m_mac: 50 })],
    ['2026-08-23', providerDay({}, 155, 1, { m_mac: 14 })],
    ['2026-08-24', providerDay({ m_beast: 1_000_000 })],
  ]))
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  assert.equal(attributionFor(providerActivity(before, 'openai'), '2026-08-23', 155)!.rule, 'same-day')
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 155 })
  assert.deepEqual(assigned(attributeAccountHistory(withoutWorkspaces(before), machines), 'byMachine'), { m_mac: 155 })
  // A prompt whose tokens were recorded, just on another day, is no missing log: BEAST's prompt below takes nothing.
  const recordedElsewhere = normaliseDays(response([['2026-09-04', providerDay({ m_mac: 35 }, 20)]]))
  recordedElsewhere[0].providers.openai!.byMachine.m_beast = place(0, 0, 1)
  assert.deepEqual(assigned(attributeAccountHistory(recordedElsewhere, machines), 'byMachine'), { m_mac: 20 })
  assert.deepEqual(assigned(attributeAccountHistory(withoutWorkspaces(recordedElsewhere), machines), 'byMachine'), { m_mac: 20 })
}
{
  // No machine ever logged a token, but one had prompts: that is Codex use, so no Unknown row remains.
  const before = normaliseDays(response([['2026-09-05', providerDay({}, 300, 1, { m_mac: 3 })], ['2026-09-30', providerDay({}, 100)]]))
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 400 })
  assert.equal(attributionFor(providerActivity(before, 'openai'), '2026-09-30', 100)!.rule, 'most-used')
}

// ---- Several accounts on one day: their recovered tokens are assigned together; nothing is lost or doubled ----
{
  const before = normaliseDays(response([['2026-09-02', providerDay({ m_mac: 1, m_beast: 2 }, 1001, 2)]]))
  assert.equal(before[0].providers.openai!.accountUsage!.length, 2)
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  assert.deepEqual(assigned(after, 'byMachine'), { m_mac: 334, m_beast: 667 })
}

// ---- A fleet with no Codex machine: only then may the Unknown rows remain ----
{
  const claude = providerDay({ m_mac: 5000 })
  const before = normaliseDays(response([['2026-09-03', providerDay({}, 700), claude]]))
  const after = attributeAccountHistory(before, machines)
  check(before, after, true)
  assert.equal(after, before)
  assert.deepEqual(rows(after, 'byMachine'), { m_mac: 5000, unknown: 700 })
  assert.equal(attributionFor(providerActivity(before, 'openai'), '2026-09-03', 700), null)
}

// ---- Real-shaped history: a long stretch with no local logs, beside days with some ----
{
  const days: Array<[string, ProviderDay]> = []
  for (let i = 0; i < 45; i++) {
    const date = addDays('2026-08-21', i)
    const local: Record<string, number> = i % 9 === 0 ? { m_beast: 10_000 + i } : i % 5 === 0 ? { m_mac: 3_000, m_beast: 1_000 } : {}
    days.push([date, providerDay(local, i < 20 ? 100_000_007 + i : 4_321)])
  }
  const before = normaliseDays(response(days))
  const after = attributeAccountHistory(before, machines)
  check(before, after)
  assert.equal(Object.keys(rows(after, 'byMachine')).sort().join(), 'm_beast,m_mac')
  assert.deepEqual(Object.keys(rows(after, 'byCountry')).sort(), ['GB', 'TH'])
}

// ---- Per-machine views: the fleet split (machineDays) and an exactly scoped source (addAssignedHistory) ----
{
  const before = normaliseDays(response([
    ['2026-09-01', providerDay({ m_mac: 300, m_beast: 100 }, 1000)],
    ['2026-09-02', providerDay({}, 400)],
    ['2026-09-03', providerDay({ m_mac: 50 })],
  ]))
  const fleet = attributeAccountHistory(before, machines)
  let machinesTotal = 0
  for (const m of machines) {
    const scoped = machineDays(fleet, m.id, m.cc)
    assert.equal(scoped.partial, false, 'assigned history is account history, not a missing model split')
    machinesTotal += total(scoped.days)
  }
  assert.equal(machinesTotal, total(before), 'the machine views add up to the fleet')
  const mac = machineDays(fleet, 'm_mac', 'TH').days
  // 2026-09-02 has no local Codex: the adjacent days decide (Mac 300 + 50, BEAST 100), 311 / 89.
  assert.equal(total(mac), 300 + 750 + 311 + 50)
  assert.deepEqual(rows(mac, 'byCountry'), { TH: 1411 })
  assert.equal(buildCells(mac, DEFAULT_VIEW).get('2026-09-01')!.unattributed, 750)
  assert.equal(buildCells(mac, DEFAULT_VIEW).get('2026-09-02')!.unattributed, 311)

  // The GitHub Pages dashboard scopes a machine from its own rows: they hold no account history until added.
  const own = normaliseDays(response([['2026-09-01', providerDay({ m_beast: 100 })]]))
  const withHistory = addAssignedHistory(own, fleet, 'm_beast', 'GB')
  assert.notEqual(withHistory, own)
  assert.equal(total(withHistory), 100 + 250 + 89)
  assert.deepEqual(rows(withHistory, 'byMachine'), { m_beast: 439 })
  assert.deepEqual(assigned(withHistory, 'byCountry'), { GB: 339 })
  assert.equal(buildCells(withHistory, DEFAULT_VIEW).get('2026-09-01')!.models.find(([model]) => model === 'account-history')?.[1], 250)
  assert.equal(addAssignedHistory(own, before, 'm_beast', 'GB'), own, 'nothing assigned: the same array back')
  // A day the machine logged nothing on gets a provider of its own.
  const nocc = addAssignedHistory([], fleet, 'm_mac', 'TH')
  assert.deepEqual(nocc.map(d => d.date), ['2026-09-01', '2026-09-02'])
  assert.equal(total(nocc), 750 + 311)

  // The activity feed's machine rows include the assigned share and add up to the month.
  const [month] = buildActivity(fleet, DEFAULT_VIEW, machines, '2026-09-01', '2026-09-30')
  assert.equal(month.machines.reduce((s, m) => s + m.value, 0), month.total)
  assert.deepEqual(month.machines.map(m => [m.label, m.value, m.assigned]), [['Mac', 1411, 1061], ['BEAST', 439, 339]])
  // Without attribution the feed keeps account history off the machine rows, as before.
  const [plain] = buildActivity(before, DEFAULT_VIEW, machines, '2026-09-01', '2026-09-30')
  assert.deepEqual(plain.machines.map(m => [m.label, m.value]), [['Mac', 350], ['BEAST', 100]])
  // Other metrics leave account history out, assigned or not.
  const output = placeTotals(fleet, { ...DEFAULT_VIEW, metric: 'output' }, '2026-09-01', '2026-09-30', 'byMachine')
  assert.deepEqual(output.map(t => [t.key, t.value, t.assigned]), [['m_mac', 350, 0], ['m_beast', 100, 0]])
}

// ---- Active days: history assigned to a machine is not that machine's record of an active day ----
{
  // 2026-09-02 has only account history; BEAST also has a Claude prompt there with no recorded tokens.
  const claude = (prompts: number): ProviderDay => {
    const p = providerDay({})
    p.prompts = prompts
    p.byMachine.m_beast = place(0, 0, prompts)
    return p
  }
  const before = normaliseDays(response([
    ['2026-09-01', providerDay({ m_mac: 300, m_beast: 100 }, 1000)],
    ['2026-09-02', providerDay({}, 400), claude(5)],
    ['2026-09-03', providerDay({ m_mac: 50, m_beast: 10 })],
  ]))
  const fleet = attributeAccountHistory(before, machines)
  const all = ['2026-09-01', '2026-09-03'] as const
  // The fleet keeps counting a day of account history as active, as before attribution.
  assert.equal(activeDayCount(fleet, DEFAULT_VIEW, ...all), activeDayCount(before, DEFAULT_VIEW, ...all))
  assert.equal(activeDayCount(fleet, DEFAULT_VIEW, ...all), 3)
  const beast = machineDays(fleet, 'm_beast', 'GB').days
  assert.ok(buildCells(beast, DEFAULT_VIEW).get('2026-09-02')!.unattributed > 0, 'BEAST holds some of that history')
  assert.equal(activeDayCount(beast, DEFAULT_VIEW, ...all), 2, 'but not as an active day of its own')
  const summary = summarise(buildCells(beast, DEFAULT_VIEW), ...all)
  assert.equal(summary.prompts - summary.activeDayPrompts, 5, 'nor do its prompts there count toward the per-day average')
  // The same through an exactly scoped source (GitHub Pages).
  const own = normaliseDays(response([['2026-09-01', providerDay({ m_beast: 100 })], ['2026-09-02', claude(5)]]))
  const scoped = addAssignedHistory(own, fleet, 'm_beast', 'GB')
  assert.equal(activeDayCount(scoped, DEFAULT_VIEW, ...all), 1)
  assert.equal(summarise(buildCells(scoped, DEFAULT_VIEW), ...all).activeDayPrompts, 1)
}

console.log('account history attribution: ok')
