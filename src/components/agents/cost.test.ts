// Assertions for cost.ts. Run: node --experimental-strip-types src/components/agents/cost.test.ts
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import {
  costOf,
  costOverDays,
  costRows,
  dayCost,
  formatUsd,
  lookupPrice,
  ratesOn,
  windowOn,
  type DayTokens,
  type ModelPrice,
  type PricedTokens,
  type PricingTable,
} from './cost.ts'
import type { ProviderDay } from './types'
import {
  buildCells,
  DAYS_PER_MONTH,
  DEFAULT_VIEW,
  monthsBetween,
  periodRange,
  perMonth,
  summarise,
  type NormDay,
  type ViewOptions,
} from './usage.ts'

const table = JSON.parse(readFileSync(new URL('../../data/modelPricing.json', import.meta.url), 'utf8')) as PricingTable
const near = (actual: number | null, expected: number) => {
  assert.notEqual(actual, null)
  assert.ok(Math.abs((actual as number) - expected) < 1e-9, `${actual} ≈ ${expected}`)
}
const M = 1_000_000

// Lookup: exact id, case-insensitive.
assert.equal(lookupPrice('claude-opus-5-5', table)?.via, 'exact')
assert.equal(lookupPrice('GPT-5.5', table)?.id, 'gpt-5.5')

// Lookup: family fallbacks in array order.
assert.deepEqual(
  [lookupPrice('claude-opus-5-5[1m]', table)?.id, lookupPrice('claude-opus-5-5[1m]', table)?.via],
  ['claude-opus-5-5', 'family'],
)
assert.equal(lookupPrice('claude-opus-5-20260101', table)?.id, 'claude-opus-5')
assert.equal(lookupPrice('claude-opus-4-6', table)?.id, 'claude-opus-4-6')
assert.equal(lookupPrice('claude-opus-4-0', table)?.id, 'claude-opus-4')
assert.equal(lookupPrice('claude-haiku-4-5-20251001', table)?.id, 'claude-haiku-4-5')
assert.equal(lookupPrice('gpt-5.6-terra-high', table)?.id, 'gpt-5.6-terra')
assert.equal(lookupPrice('gpt-5.1-codex-mini', table)?.id, 'gpt-5.1-codex-mini')
assert.equal(lookupPrice('grok-4.7-fast-reasoning', table)?.id, 'grok-4.7-fast')

// Cursor's own ids (SPEC "Cursor"): the underlying model where it has a verified list price, whatever the mode suffix.
assert.equal(lookupPrice('claude-4.5-sonnet-thinking', table)?.id, 'claude-sonnet-4-5', 'a shared rate must not replace the model identity')
assert.equal(lookupPrice('claude-4.5-sonnet', table)?.id, 'claude-sonnet-4-5')
assert.equal(lookupPrice('claude-4-sonnet-1m', table)?.id, 'claude-sonnet-4')
assert.equal(lookupPrice('claude-4.5-opus-high-thinking', table)?.id, 'claude-opus-4-5')
assert.equal(lookupPrice('claude-4.6-opus-high-thinking', table)?.id, 'claude-opus-4-6')
assert.equal(lookupPrice('claude-4.1-opus', table)?.id, 'claude-opus-4-1')
assert.equal(lookupPrice('claude-4.5-haiku', table)?.id, 'claude-haiku-4-5')
assert.equal(lookupPrice('gpt-5.1-codex-max-xhigh', table)?.id, 'gpt-5.1-codex-max')
assert.equal(lookupPrice('gpt-5.1-codex', table)?.id, 'gpt-5.1-codex')
assert.equal(lookupPrice('gpt-5', table)?.id, 'gpt-5')
assert.equal(lookupPrice('gemini-3.1-pro-preview', table)?.via, 'exact')
assert.equal(lookupPrice('gemini-3.1-pro', table)?.id, 'gemini-3.1-pro-preview')
assert.equal(lookupPrice('gemini-3-flash-preview', table)?.via, 'exact')
assert.equal(lookupPrice('gemini-3-flash', table)?.id, 'gemini-3-flash-preview', 'Cursor gemini-3-flash prices at the listed preview')

// Same-rate model families still have different launches and retirements. Aliases must respect both.
const millionInput = { in: M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0 }
for (const [alias, canonical, from, last, expected] of [
  ['claude-4-sonnet', 'claude-sonnet-4', '2025-05-22', '2026-06-14', 3],
  ['claude-4.5-sonnet-thinking', 'claude-sonnet-4-5', '2025-09-29', null, 3],
  ['claude-4.6-opus-high-thinking', 'claude-opus-4-6', '2026-02-05', null, 5],
  ['gpt-5', 'gpt-5', '2025-08-07', null, 1.25],
  ['gpt-5.1-codex', 'gpt-5.1-codex', '2025-11-13', '2026-07-22', 1.25],
  ['gpt-5.1-codex-max-xhigh', 'gpt-5.1-codex-max', '2025-12-04', '2026-07-22', 1.25],
] as const) {
  const match = lookupPrice(alias, table)!
  assert.equal(match.id, canonical)
  const previous = new Date(Date.parse(`${from}T00:00:00Z`) - 86_400_000).toISOString().slice(0, 10)
  assert.equal(costOf(millionInput, ratesOn(match.price, previous)), null, `${alias}: no price before this model's launch`)
  near(costOf(millionInput, ratesOn(match.price, from)), expected)
  if (last) {
    near(costOf(millionInput, ratesOn(match.price, last)), expected)
    const next = new Date(Date.parse(`${last}T00:00:00Z`) + 86_400_000).toISOString().slice(0, 10)
    assert.equal(costOf(millionInput, ratesOn(match.price, next)), null, `${alias}: no price on/after retirement`)
    assert.equal(costOf(millionInput, match.price), null, `${alias}: never substitute a successor's current price`)
  } else {
    near(costOf(millionInput, match.price), expected)
  }
}
assert.equal(lookupPrice('claude-opus-4-9', table), null, 'unknown model versions cannot inherit a successor price')
assert.equal(lookupPrice('claude-sonnet-4-9', table), null)
assert.equal(lookupPrice('grok-4.2', table), null, 'unknown versions cannot inherit Grok 4.7 pricing')
assert.equal(lookupPrice('gpt-5.7', table), null, 'unknown versions cannot inherit GPT-5.6 pricing')
assert.equal(lookupPrice('gpt-5.2', table)?.id, 'gpt-5.2')
assert.equal(lookupPrice('gpt-5.2-codex-high', table)?.id, 'gpt-5.2-codex')

// Gemini CLI ids (SPEC "Gemini CLI"): the two models its session files record, plus dated and -lite variants.
assert.equal(lookupPrice('gemini-2.5-pro', table)?.via, 'exact')
assert.equal(lookupPrice('gemini-2.5-flash', table)?.via, 'exact')
assert.equal(lookupPrice('gemini-2.5-pro-preview-06-05', table)?.id, 'gemini-2.5-pro')
assert.equal(lookupPrice('gemini-2.5-flash-preview-05-20', table)?.id, 'gemini-2.5-flash')
assert.equal(lookupPrice('gemini-2.5-flash-lite', table)?.via, 'exact')
assert.equal(lookupPrice('gemini-2.5-flash-lite-preview-06-17', table)?.id, 'gemini-2.5-flash-lite', '-lite is not priced as full Flash')
for (const id of ['gemini-2.5-pro', 'gemini-2.5-flash', 'gemini-2.5-flash-lite', 'gemini-3-flash-preview', 'gemini-3.1-pro-preview']) {
  const p = table.models[id]
  assert.equal(p.provider, 'google')
  assert.equal(p.cacheWrite, p.input, `${id}: Google bills cache creation at the input rate`)
  assert.ok(p.cacheRead! < p.input!, `${id}: cache reads are cheaper than input`)
}

// No list price today, only a past one: grok-code-fast-1 (retired 2026-05-15) and gemini-3-pro(-preview) (shut down
// 2026-03-09) resolve to entries with null rates and a closed history window, so they are unpriced at today's prices
// and priced at the time of use inside their listing.
const pastOnly = { in: 1 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 1 * M }
for (const [id, entry, inside, after, dollars] of [
  ['grok-code-fast-1', 'grok-code-fast-1', '2025-12-01', '2026-06-01', 0.2 + 1.5],
  ['gemini-3-pro', 'gemini-3-pro-preview', '2025-11-24', '2026-03-10', 2 + 12],
  ['gemini-3-pro-preview', 'gemini-3-pro-preview', '2026-03-09', '2026-03-10', 2 + 12],
] as const) {
  const match = lookupPrice(id, table)
  assert.equal(match?.id, entry)
  assert.equal(costOf(pastOnly, match!.price), null, `${id}: no price today`)
  near(costOf(pastOnly, ratesOn(match!.price, inside)), dollars)
  assert.equal(costOf(pastOnly, ratesOn(match!.price, after)), null, `${id}: unpriced after its listing ended`)
}
// Unpriced: Cursor's own modes and unknown ids.
assert.equal(lookupPrice('composer-1', table), null)
assert.equal(lookupPrice('default', table), null)
assert.equal(lookupPrice('unknown', table), null)
assert.equal(lookupPrice('', table), null)
assert.equal(lookupPrice('llama-3-70b', table), null)

// Cursor rows carry no cache split, so everything bills at input and output.
const sonnet46 = table.models['claude-sonnet-4-6']
near(costOf({ in: 2 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0.5 * M }, sonnet46), 2 * sonnet46.input! + 0.5 * sonnet46.output!)

// Gemini rows: in is input minus cached, cacheR the cached count, no cache writes, thoughts inside out.
// 3M raw input of which 2M cached, 0.2M out: 1 × $1.25 + 2 × $0.125 + 0.2 × $10 = $3.50 at 2.5 Pro's <= 200k tier.
const gemini = table.models['gemini-2.5-pro']
near(costOf({ in: 1 * M, cacheW: 0, cacheW1h: 0, cacheR: 2 * M, out: 0.2 * M }, gemini), 3.5)
near(costOf({ in: 1 * M, cacheW: 0, cacheW1h: 0, cacheR: 2 * M, out: 0.2 * M }, gemini), gemini.input! + 2 * gemini.cacheRead! + 0.2 * gemini.output!)

// Formula: in, cache writes split into 5-minute and 1-hour, cache reads, output (SPEC).
const opus = table.models['claude-opus-5-5']
near(
  costOf({ in: 1 * M, cacheW: 2 * M, cacheW1h: 0.5 * M, cacheR: 10 * M, out: 1 * M }, opus),
  1 * opus.input! + 1.5 * opus.cacheWrite! + 0.5 * opus.cacheWrite1h! + 10 * opus.cacheRead! + 1 * opus.output!,
)
// cacheW1h is a subset of cacheW, clamped, never added on top.
near(costOf({ in: 0, cacheW: 1 * M, cacheW1h: 5 * M, cacheR: 0, out: 0 }, opus), opus.cacheWrite1h!)
// No cacheWrite1h (OpenAI): 1-hour writes bill at cacheWrite.
const gpt = table.models['gpt-5.5']
near(costOf({ in: 0, cacheW: 2 * M, cacheW1h: 1 * M, cacheR: 0, out: 0 }, gpt), 2 * gpt.cacheWrite!)
// Reasoning is inside out and never billed again: costOf has no reasoning input at all.
near(costOf({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 3 * M }, gpt), 3 * gpt.output!)
// A null rate for a bucket with tokens is unpriced; a null rate for an empty bucket is fine.
assert.equal(costOf({ in: 1, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0 }, { input: null, output: 1, cacheRead: 1, cacheWrite: 1 }), null)
near(costOf({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: M }, { input: null, output: 2, cacheRead: 1, cacheWrite: 1 }), 2)

// Rows: priced by $ descending, unpriced last by size, total excludes unpriced. An unpriced Cursor row keeps its tokens.
const { rows, totalUsd } = costRows(
  [
    { model: 'unknown', provider: 'anthropic', tokens: { in: 9 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0 } },
    { model: 'gpt-5-nano', provider: 'openai', tokens: { in: 1 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0 } },
    { model: 'claude-opus-5-5', provider: 'anthropic', tokens: { in: 1 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0 } },
    { model: 'gemini-3-pro', provider: 'cursor', tokens: { in: 12 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0.1 * M } },
    { model: 'gemini-2.5-flash', provider: 'google', tokens: { in: 1 * M, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0 } },
  ],
  table,
  'today',
)
assert.deepEqual(
  rows.map((r) => r.model),
  ['claude-opus-5-5', 'gemini-2.5-flash', 'gpt-5-nano', 'gemini-3-pro', 'unknown'],
)
assert.equal(rows[3].usd, null)
// gemini-3-pro resolves to its past listing, which has no price today.
assert.deepEqual([rows[3].match?.id, rows[3].tokens.in, rows[3].tokens.out], ['gemini-3-pro-preview', 12 * M, 0.1 * M])
assert.equal(rows[4].usd, null)
near(totalUsd, opus.input! + table.models['gpt-5-nano'].input! + table.models['gemini-2.5-flash'].input!)

// ---- Pricing at the time (history windows) ----

const T = (t: Partial<PricedTokens>): PricedTokens => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, ...t })
// Today $1 in / $5 out; $2 / $10 until 30 June 2025; nothing known before 1 March 2025.
const dated: ModelPrice = {
  input: 1,
  output: 5,
  cacheRead: 0.1,
  cacheWrite: 1.25,
  history: [
    { from: '2025-03-01', to: '2025-06-30', input: 2, output: 10, cacheRead: 0.2, cacheWrite: 2.5 },
    { from: '2025-07-01', to: null, input: 1, output: 5, cacheRead: 0.1, cacheWrite: 1.25 },
  ],
}

// Window selection at the boundaries: from and to are both inclusive local dates.
assert.equal(windowOn(dated, '2025-03-01')?.input, 2)
assert.equal(windowOn(dated, '2025-06-30')?.input, 2)
assert.equal(windowOn(dated, '2025-07-01')?.input, 1)
assert.equal(windowOn(dated, '2026-09-30')?.to, null, 'an open window runs to today')
assert.equal(windowOn(dated, '2025-02-28'), null)
// Missing history must not silently borrow today's rate, including before launch and gaps between windows.
assert.equal(ratesOn(dated, '2025-02-28').input, null)
const flatNoHistory: ModelPrice = { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75, cacheWrite1h: 6 }
assert.equal(costOf(T({ in: M }), ratesOn(flatNoHistory, '2025-05-01')), null)
const missingRate = ratesOn({ ...flatNoHistory, history: [{ from: '2025-01-01', to: null, input: 4 }] }, '2025-05-01')
assert.equal(missingRate.output, null)
assert.equal(missingRate.cacheWrite1h, null)
assert.equal(costOf(T({ out: M }), missingRate), null)
const gap: ModelPrice = { ...dated, history: [dated.history![0], { ...dated.history![1], from: '2025-08-01' }] }
assert.equal(costOf(T({ in: M }), ratesOn(gap, '2025-07-01')), null)

// Sum over days: each day at its own window, then summed; today prices every day at today's rate.
const twoDays: DayTokens[] = [
  { date: '2025-06-30', tokens: T({ in: M, out: M }) },
  { date: '2025-07-01', tokens: T({ in: M, out: M }) },
]
near(costOverDays(twoDays, dated, 'at-the-time').usd, 2 + 10 + 1 + 5)
near(costOverDays(twoDays, dated, 'today').usd, 2 * (1 + 5))
// Without history or dated usage there is no historical cost; today's mode still works.
assert.deepEqual(costOverDays(twoDays, flatNoHistory, 'at-the-time'), { usd: null, unpricedDays: 2 })
near(costOverDays(twoDays, flatNoHistory, 'today').usd, costOf(T({ in: 2 * M, out: 2 * M }), flatNoHistory)!)
assert.equal(costRows([{ model: 'gpt-5', provider: 'openai', tokens: T({ in: M }) }], table).rows[0].usd, null)

// A model only listed in the past (no price today): priced at the time, unpriced today; days outside its life are counted.
const retired: ModelPrice = {
  input: null,
  output: null,
  cacheRead: null,
  cacheWrite: null,
  history: [{ from: '2025-08-26', to: '2026-02-28', input: 0.2, output: 1.5, cacheRead: 0.02, cacheWrite: 0.2 }],
}
const retiredDays: DayTokens[] = [
  { date: '2025-12-01', tokens: T({ in: M, out: M }) },
  { date: '2026-03-01', tokens: T({ in: M }) },
]
assert.deepEqual(costOverDays(retiredDays, retired, 'today'), { usd: null, unpricedDays: 2 })
const retiredThen = costOverDays(retiredDays, retired, 'at-the-time')
near(retiredThen.usd, 0.2 + 1.5)
assert.equal(retiredThen.unpricedDays, 1)

// Rows: both modes, the chosen one drives usd, sort and total; a partly priced row says so.
const synthetic: PricingTable = {
  models: { dated, flat: { ...flatNoHistory, history: [{ from: '2025-01-01', to: null, ...flatNoHistory }] }, retired },
  familyFallbacks: [],
}
const rowsIn = [
  { model: 'dated', provider: 'openai', tokens: T({ in: 2 * M, out: 2 * M }), byDay: twoDays },
  { model: 'flat', provider: 'anthropic', tokens: T({ in: 2 * M, out: 2 * M }), byDay: twoDays },
  { model: 'retired', provider: 'xai', tokens: T({ in: 2 * M, out: M }), byDay: retiredDays },
]
const atTime = costRows(rowsIn, synthetic, 'at-the-time')
const now = costRows(rowsIn, synthetic, 'today')
assert.equal(costRows(rowsIn, synthetic).totalUsd, atTime.totalUsd, 'at the time is the default')
const byModel = (t: typeof atTime) => Object.fromEntries(t.rows.map((r) => [r.model, r]))
near(byModel(atTime).dated.usd, 18)
near(byModel(atTime).dated.usdToday, 12)
near(byModel(now).dated.usd, 12)
assert.equal(byModel(now).retired.usd, null)
near(byModel(now).retired.usdAtTheTime, 1.7)
assert.equal(byModel(atTime).retired.partial, true)
assert.equal(byModel(atTime).flat.partial, false)
near(atTime.totalUsd, 18 + 2 * 18 + 1.7)
near(now.totalUsd, 12 + 2 * 18)
near(atTime.totalToday, now.totalUsd)
near(now.totalAtTheTime, atTime.totalUsd)
assert.deepEqual(
  now.rows.map((r) => r.model),
  ['flat', 'dated', 'retired'],
)

// A flat history (launch price = today's) gives identical results in both modes.
const flatHistory: ModelPrice = { ...flatNoHistory, history: [{ from: '2025-05-22', to: null, input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75, cacheWrite1h: 6 }] }
const flatDays: DayTokens[] = [
  { date: '2025-06-01', tokens: T({ in: M, cacheW: 2 * M, cacheW1h: M, cacheR: 5 * M, out: M }) },
  { date: '2026-01-15', tokens: T({ in: 3 * M, out: 0.5 * M }) },
]
assert.deepEqual(costOverDays(flatDays, flatHistory, 'at-the-time'), costOverDays(flatDays, flatHistory, 'today'))

// One day's cost: the window on that day; tokens on a model with no price are flagged, not counted.
const before = dayCost('2025-06-30', [{ model: 'dated', tokens: T({ in: M }) }, { model: 'mystery', tokens: T({ in: M }) }], synthetic, 'at-the-time')
assert.deepEqual(before, { usd: 2, unpriced: true })
assert.deepEqual(dayCost('2025-06-30', [{ model: 'dated', tokens: T({ in: M }) }], synthetic, 'today'), { usd: 1, unpriced: false })

// The real table's history is well formed: oldest first, contiguous, the last window open and equal to today's price.
const nextDay = (iso: string) => new Date(Date.parse(`${iso}T00:00:00Z`) + 86_400_000).toISOString().slice(0, 10)
const RATE_KEYS = ['input', 'output', 'cacheRead', 'cacheWrite', 'cacheWrite1h'] as const
for (const [id, price] of Object.entries(table.models)) {
  const history = price.history
  if (!history) continue
  assert.ok(history.length > 0, `${id}: empty history`)
  history.forEach((w, i) => {
    assert.match(w.from, /^\d{4}-\d{2}-\d{2}$/, `${id}: from`)
    assert.ok((w.sources ?? []).length > 0, `${id} ${w.from}: every window cites a source`)
    if (i < history.length - 1) {
      assert.ok(w.to !== null && w.to >= w.from, `${id} ${w.from}: closed window`)
      assert.equal(nextDay(w.to!), history[i + 1].from, `${id}: windows are contiguous`)
    }
  })
  const last = history[history.length - 1]
  const listedToday = price.input !== null
  if (listedToday) {
    assert.equal(last.to, null, `${id}: the last window is today's price`)
    for (const key of RATE_KEYS) if (last[key] !== undefined) assert.equal(last[key], price[key], `${id}: last window ${key} = today's`)
  } else {
    assert.notEqual(last.to, null, `${id}: no price today, so its last window is closed`)
  }
}

// ---- The cost tile's monthly figure: the period's cost over its length in months (days / 30.44) ----

assert.equal(DAYS_PER_MONTH, 30.44)
// The last 12 months are 12 months, whatever their exact day count.
assert.equal(periodRange('recent', '2026-10-01').months, 12)
assert.equal(perMonth(11_718, periodRange('recent', '2026-10-01')), 976.5)
// A past calendar year: 365 days (366 in a leap year).
near(periodRange('2025', '2026-10-01').months, 365 / 30.44)
near(periodRange('2024', '2026-10-01').months, 366 / 30.44)
// The current year counts only the days so far, today included: 1 Jan to 1 Oct 2026 is 274 days.
near(periodRange('2026', '2026-10-01').months, 274 / 30.44)
near(monthsBetween('2026-01-01', '2026-01-01'), 1 / 30.44)
near(perMonth(3044, { months: monthsBetween('2026-01-01', '2026-04-10') }), (3044 * 30.44) / 100)
assert.equal(perMonth(500, { months: 0 }), 0)

// The headline's monthly token figure: the period total (in the chosen metric and provider filter) over the same months.
const EMPTY_BUCKET = { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 }
const providerDay = (b: Partial<typeof EMPTY_BUCKET>): ProviderDay => ({
  exact: { ...EMPTY_BUCKET, ...b },
  estimated: EMPTY_BUCKET,
  prompts: 1,
  promptsNoUsage: 0,
  accts: [],
  models: { exact: {}, estimated: {} },
  byMachine: {},
  byCountry: {},
})
const usageDays: NormDay[] = [
  { date: '2025-12-31', providers: { anthropic: providerDay({ in: 999 }) } },
  { date: '2026-01-10', providers: { anthropic: providerDay({ in: 100, cacheR: 1000, out: 50 }), openai: providerDay({ in: 200, out: 100 }) } },
  { date: '2026-09-30', providers: { anthropic: providerDay({ in: 10, cacheW: 40 }) } },
]
const year2026 = periodRange('2026', '2026-10-01')
const monthlyTokens = (view: ViewOptions) => {
  const s = summarise(buildCells(usageDays, view), year2026.from, year2026.to)
  return perMonth(s.total, year2026)
}
near(monthlyTokens(DEFAULT_VIEW), (1150 + 300 + 50) / (274 / 30.44))
near(monthlyTokens({ ...DEFAULT_VIEW, metric: 'effective' }), (100 + 50 + 0.1 * 1000 + 300 + 50) / (274 / 30.44))
near(monthlyTokens({ ...DEFAULT_VIEW, metric: 'output' }), 150 / (274 / 30.44))
near(monthlyTokens({ ...DEFAULT_VIEW, provider: 'openai' }), 300 / (274 / 30.44))

assert.equal(formatUsd(48210.4), '$48,210')
assert.equal(formatUsd(3.456), '$3.46')
assert.equal(formatUsd(0.001), '<$0.01')

console.log('cost.test.ts: ok')
