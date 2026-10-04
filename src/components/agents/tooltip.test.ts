import assert from 'node:assert/strict'
import { averageSources, projectLabel, samplePeriod } from './tooltip.ts'
import { buildCells, DEFAULT_VIEW, type DayDerivation } from './usage.ts'
import type { ProviderDay, TokenBucket } from './types.ts'

const bucket = (out = 0): TokenBucket => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out, reasoning: 0, calls: 0, events: 0 })
const base: DayDerivation = { provider: 'anthropic', workspace: 'workspace13', month: '2026-03', basis: 'provider-history',
  sampleFrom: '2026-09', sampleTo: '2026-10', samplePrompts: 320, perPrompt: bucket(40),
  missingInMonth: 20, affectedDays: 10, missingToday: 3, rate: 40, dailyEstimate: 80 }
const second = { ...base, workspace: 'workspace18', missingInMonth: 100, affectedDays: 5, dailyEstimate: 800 }
assert.equal(averageSources([base, second]).length, 1, 'different project calculations can use one provider sample')
assert.equal(averageSources([base, second, { ...base, provider: 'openai' }]).length, 2, 'never merge samples across providers')
assert.equal(averageSources([base, { ...base, sampleFrom: '2025-09' }]).length, 2, 'different sample periods stay distinct')
assert.equal(averageSources([{ ...base, basis: 'workspace-history' }, { ...second, basis: 'workspace-history' }]).length, 2,
  'separate projects stay identifiable even when their rates happen to match')
assert.equal(projectLabel('workspace13'), 'Project 13')
assert.equal(projectLabel(null), 'Project unknown')
assert.equal(samplePeriod('2026-09', '2026-10'), 'Sep–Oct 2026')
assert.equal(samplePeriod('2025-12', '2026-01'), 'Dec 2025–Jan 2026')

// Legacy data has no workspace field: it still contributes to an explicit unknown project.
const p: ProviderDay = { exact: bucket(100), estimated: bucket(20), prompts: 8, promptsNoUsage: 3,
  models: { exact: {}, estimated: {} }, accts: [], byCountry: {}, byMachine: {} }
const cell = buildCells([{ date: '2026-03-15', providers: { anthropic: p } }], DEFAULT_VIEW).get('2026-03-15')!
assert.deepEqual(cell.byProvider.anthropic!.projects, [{ workspace: null, total: 120, inferred: 0, prompts: 8, promptsNoUsage: 3 }])
console.log('tooltip grouping and legacy project accounting: ok')
