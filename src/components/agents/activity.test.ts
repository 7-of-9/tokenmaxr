// Assertions for activity.ts. Run: node --experimental-strip-types src/components/agents/activity.test.ts
import assert from 'node:assert/strict'
import { buildActivity, formatDayLong, ordinal } from './activity.ts'
import { buildMockUsage } from './mock.ts'
import { DEFAULT_VIEW, normaliseDays, type NormDay } from './usage.ts'
import type { ModelBucket, ProviderDay } from './types'

const bucket = (tokens: number): ModelBucket => ({ in: tokens, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 1 })
const EMPTY = { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 }

function day(date: string, models: Record<string, number>, machines: Record<string, number>, promptsNoUsage = 0): NormDay {
  const exact: Record<string, ModelBucket> = {}
  for (const [id, v] of Object.entries(models)) exact[id] = bucket(v)
  const byMachine: ProviderDay['byMachine'] = {}
  for (const [id, v] of Object.entries(machines)) byMachine[id] = { in: v, cacheW: 0, cacheR: 0, out: 0, prompts: 1 }
  const total = Object.values(models).reduce((s, v) => s + v, 0)
  return {
    date,
    providers: {
      anthropic: {
        exact: { ...EMPTY, in: total },
        estimated: EMPTY,
        prompts: 1 + promptsNoUsage,
        promptsNoUsage,
        accts: [],
        models: { exact, estimated: {} },
        byMachine,
        byCountry: {},
      },
    },
  }
}

const days = [
  day('2026-01-05', { 'claude-opus-5': 100 }, { m1: 100 }),
  day('2026-02-03', { 'claude-opus-5': 300, 'claude-sonnet-5': 50 }, { m1: 200, m2: 150 }, 7),
  day('2026-02-20', { 'claude-sonnet-5': 25, unknown: 5 }, { m2: 30 }),
  day('2026-04-01', {}, {}, 12),
]
const machines = [{ id: 'm1', label: 'Studio Mac', cc: 'TH', os: 'darwin', live: true, lastSeenAt: null }]
const feed = buildActivity(days, DEFAULT_VIEW, machines, '2026-01-01', '2026-12-31')

// Newest month first; an empty March is skipped; an activity-only April is kept.
assert.deepEqual(
  feed.map((m) => m.month),
  ['2026-04', '2026-02', '2026-01'],
)
const [april, feb, jan] = feed
assert.equal(april.total, 0)
assert.equal(april.promptsNoUsage, 12)
assert.equal(april.prompts, 13)
assert.equal(feb.prompts, 9)

// Models add up to the month's total, largest first.
assert.equal(feb.total, 380)
assert.deepEqual(
  feb.models.map((m) => [m.model, m.value]),
  [
    ['claude-opus-5', 300],
    ['claude-sonnet-5', 75],
    ['unknown', 5],
  ],
)
// Machines: labels from the public list, unknown ids named neutrally.
assert.deepEqual(
  feb.machines.map((m) => [m.label, m.value]),
  [
    ['Unnamed machine', 180],
    ['Studio Mac', 200],
  ].sort((a, b) => (b[1] as number) - (a[1] as number)),
)
assert.equal(feb.promptsNoUsage, 7)

// "Started using": sonnet is new in February; opus arrived in the month history starts, which instead gets
// "first recorded"; the placeholder "unknown" never counts as a model someone started using.
assert.deepEqual(
  feb.started.map((s) => [s.model, s.date]),
  [['claude-sonnet-5', '2026-02-03']],
)
assert.deepEqual(jan.started, [])
assert.equal(jan.firstRecorded, '2026-01-05')
assert.equal(feb.firstRecorded, undefined)

// A period after the history start has no "first recorded" item, and started items stay in the period.
const later = buildActivity(days, DEFAULT_VIEW, machines, '2026-02-10', '2026-12-31')
assert.deepEqual(
  later.map((m) => m.month),
  ['2026-04', '2026-02'],
)
assert.equal(later[1].total, 30)
assert.deepEqual(later[1].started, [])
assert.equal(later[1].firstRecorded, undefined)

// Exact-only drops estimated buckets entirely.
const estimatedOnly: NormDay = {
  date: '2026-05-01',
  providers: {
    xai: {
      exact: EMPTY,
      estimated: { ...EMPTY, in: 9 },
      prompts: 1,
      promptsNoUsage: 0,
      accts: [],
      models: { exact: {}, estimated: { 'grok-4.7': bucket(9) } },
      byMachine: {},
      byCountry: {},
    },
  },
}
assert.equal(buildActivity([estimatedOnly], DEFAULT_VIEW, [], '2026-01-01', '2026-12-31')[0].total, 9)
assert.equal(buildActivity([estimatedOnly], { ...DEFAULT_VIEW, exactOnly: true }, [], '2026-01-01', '2026-12-31').length, 1)
assert.equal(buildActivity([estimatedOnly], { ...DEFAULT_VIEW, exactOnly: true }, [], '2026-01-01', '2026-12-31')[0].total, 0)

// The mock exercises every feed item: several models a month, several machines, and new models mid-year.
const withPromptModels = day('2026-03-03', { opus: 120 }, { m1: 120 })
withPromptModels.providers.anthropic!.prompts = 7
withPromptModels.providers.anthropic!.promptsByModel = { opus: 3, 'prompt-only': 4 }
const modelPromptMonth = buildActivity([withPromptModels], DEFAULT_VIEW, machines, '2026-03-01', '2026-03-31')[0]
assert.equal(modelPromptMonth.prompts, 7)
assert.deepEqual(modelPromptMonth.models.map(m => [m.model, m.value, m.prompts]), [['opus', 120, 3], ['prompt-only', 0, 4]])
assert.equal(modelPromptMonth.total, 120, 'prompt counts cannot alter token totals')
assert.equal(feb.models[0].prompts, null, 'missing model prompt metadata is not presented as zero or API calls')
const zeroTokens = day('2026-03-10', {}, {})
assert.equal(buildActivity([zeroTokens], DEFAULT_VIEW, machines, '2026-03-01', '2026-03-31')[0].prompts, 1,
  'a prompt-only month remains visible even if its session says usage was available')

// The mock exercises every feed item: several models a month, several machines, and new models mid-year.
const mock = normaliseDays(buildMockUsage(new Date(2026, 8, 30, 12)))
const mockFeed = buildActivity(mock, DEFAULT_VIEW, [], '2025-01-01', '2026-09-30')
assert.ok(mockFeed.every((m) => m.month >= '2025-10'))
assert.ok(mockFeed.some((m) => m.models.length >= 6), 'a month with six or more models')
assert.ok(mockFeed.some((m) => m.machines.length >= 2), 'a month on two or more machines')
assert.ok(mockFeed.some((m) => m.month >= '2026-06' && m.started.length > 0), 'a model started mid-year')
assert.ok(mockFeed.some((m) => m.promptsNoUsage > 0), 'prompts whose tokens were not recorded')
assert.equal(mockFeed[mockFeed.length - 1].firstRecorded?.slice(0, 7), '2025-10')

// GitHub's phrasing.
assert.deepEqual([1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 28, 31, 111].map(ordinal), ['1st', '2nd', '3rd', '4th', '11th', '12th', '13th', '21st', '22nd', '23rd', '28th', '31st', '111th'])
assert.equal(formatDayLong('2026-09-28', '2026-09-30'), 'September 28th')
assert.equal(formatDayLong('2025-12-03', '2026-09-30'), 'December 3rd, 2025')

console.log('activity.test.ts: ok')
