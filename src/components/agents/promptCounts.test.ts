import assert from 'node:assert/strict'
import test from 'node:test'
import { applyPromptCounts, readPromptCounts, type PromptCountsResponse } from './promptCounts.ts'
import type { ProviderDay, TokenBucket } from './types.ts'
import type { NormDay } from './usage.ts'

const bucket = (): TokenBucket => ({ in: 0, out: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, reasoning: 0, calls: 0, events: 0 })
const provider = (prompts = 7): ProviderDay => ({ exact: { ...bucket(), in: 100 }, estimated: bucket(), prompts,
  promptsNoUsage: 2, accts: [], models: { exact: {}, estimated: {} }, byMachine: {}, byCountry: {} })
const usage = (): NormDay[] => [
  { date: '2026-10-01', providers: { openai: provider(), anthropic: provider(4) } },
  { date: '2026-10-02', providers: { openai: provider(3) } },
]
const response = (): PromptCountsResponse => ({ source: 'archive', generatedAt: '2026-10-03T02:00:00Z', total: 3, undecryptable: 0,
  days: [{ date: '2026-10-01', providers: { openai: { prompts: 3, byModel: { 'model-a': 2, unknown: 1 }, byMachineComplete: true,
    byMachine: { studio: { prompts: 2, byModel: { 'model-a': 2 } }, mac: { prompts: 1, byModel: { unknown: 1 } } } } } }] })

test('archive counts enrich models without replacing activity totals or token statistics', () => {
  const days = usage(), original = structuredClone(days)
  const result = applyPromptCounts(days, readPromptCounts(response()))
  assert.deepEqual(result[0].providers.openai?.promptsByModel, { 'model-a': 2, unknown: 1 })
  assert.equal(result[0].providers.openai?.prompts, 7)
  assert.equal(result[0].providers.openai?.promptsNoUsage, 2)
  assert.equal(result[0].providers.openai?.exact.in, 100)
  assert.deepEqual(result[0].providers.anthropic?.promptsByModel, {})
  assert.deepEqual(result[1].providers.openai?.promptsByModel, {})
  assert.deepEqual(days, original, 'the usage cache must not be mutated')
})

test('machine selection uses only its archive counts and a complete missing machine means zero', () => {
  const counts = readPromptCounts(response())
  assert.deepEqual(applyPromptCounts(usage(), counts, 'studio')[0].providers.openai?.promptsByModel, { 'model-a': 2 })
  assert.deepEqual(applyPromptCounts(usage(), counts, 'mac')[0].providers.openai?.promptsByModel, { unknown: 1 })
  assert.deepEqual(applyPromptCounts(usage(), counts, 'absent')[0].providers.openai?.promptsByModel, {})
})

test('ambiguous machine coverage stays unavailable while all-machine counts remain usable', () => {
  const counts = response(), p = counts.days[0].providers.openai!
  p.byMachineComplete = false
  p.byMachine.unknown = p.byMachine.mac
  delete p.byMachine.mac
  for (const machine of ['studio', 'mac', 'absent']) {
    assert.equal(applyPromptCounts(usage(), counts, machine)[0].providers.openai?.promptsByModel, undefined)
  }
  assert.deepEqual(applyPromptCounts(usage(), counts)[0].providers.openai?.promptsByModel, { 'model-a': 2, unknown: 1 })
})

test('unavailable data stays unavailable; valid empty archive means zero archived model counts', () => {
  const days = usage()
  assert.equal(applyPromptCounts(days, null), days)
  const empty = readPromptCounts({ ...response(), days: [], total: 0 })
  assert.deepEqual(applyPromptCounts(days, empty)[0].providers.openai?.promptsByModel, {})
  assert.equal(applyPromptCounts(days, empty)[0].providers.openai?.prompts, 7)
})

test('archive-only days and providers do not fabricate activity totals or token days', () => {
  const counts = response()
  counts.days.push({ date: '2026-09-30', providers: { xai: counts.days[0].providers.openai } })
  const result = applyPromptCounts(usage(), counts)
  assert.deepEqual(result.map(day => day.date), ['2026-10-01', '2026-10-02'])
  assert.equal(result[0].providers.xai, undefined)
})

test('response validation retains only counts and normalises an empty model to unknown', () => {
  const raw = response()
  raw.days[0].providers.openai!.byModel = { 'model-a': 2, '': 1 }
  const result = readPromptCounts({ ...raw, text: 'private', accounts: ['private'], days: raw.days.map(day => ({ ...day, workspace: 'private' })) })
  assert.deepEqual({ ...result.days[0].providers.openai?.byModel }, { 'model-a': 2, unknown: 1 })
  assert.equal('text' in result, false)
  assert.equal('accounts' in result, false)
  assert.equal('workspace' in result.days[0], false)
})

test('malformed counts and incomplete responses cannot silently turn into reported zeros', () => {
  const invalid: unknown[] = [null, {}, { ...response(), source: 'private' }, { ...response(), total: 4 },
    { ...response(), days: [response().days[0], response().days[0]] },
    { ...response(), days: [{ ...response().days[0], date: '2026-02-30' }] }]
  for (const number of [-1, 1.5, NaN, Infinity]) {
    const raw = response()
    raw.days[0].providers.openai!.byModel['model-a'] = number
    invalid.push(raw)
  }
  const badMachine = response()
  badMachine.days[0].providers.openai!.byMachine.unknown = badMachine.days[0].providers.openai!.byMachine.mac
  delete badMachine.days[0].providers.openai!.byMachine.mac
  invalid.push(badMachine)
  for (const raw of invalid) assert.throws(() => readPromptCounts(raw), /invalid response/)
})
