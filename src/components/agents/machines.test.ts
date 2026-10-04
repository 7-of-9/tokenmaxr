import assert from 'node:assert/strict'
import { machineDays } from './machines.ts'
import { buildCells, DEFAULT_VIEW, normaliseDays } from './usage.ts'
import type { ProviderDay, TokenBucket, UsageResponse } from './types.ts'

const bucket = (out = 0): TokenBucket => ({ in: 0, out, cacheW: 0, cacheW1h: 0, cacheR: 0, reasoning: 0, calls: 0, events: 0 })
function group(machine: string, cc: string, tokens: number, model: string): ProviderDay {
  const place = { in: 0, cacheW: 0, cacheR: 0, out: tokens, prompts: 2, promptsNoUsage: 1 }
  return { exact: bucket(tokens), estimated: bucket(), prompts: 2, promptsNoUsage: 1, accts: ['acct1'],
    models: { exact: { [model]: bucket(tokens) }, estimated: {} }, byMachine: { [machine]: place }, byCountry: { [cc]: place } }
}
const mac = group('mac', 'TH', 100, 'model-mac')
const win = group('win', 'GB', 900, 'model-win')
const combined: ProviderDay = { exact: bucket(1000), estimated: bucket(), prompts: 4, promptsNoUsage: 2, accts: ['acct1', 'acct2'],
  models: { exact: { ...mac.models.exact, ...win.models.exact }, estimated: {} },
  byMachine: { ...mac.byMachine, ...win.byMachine }, byCountry: { ...mac.byCountry, ...win.byCountry }, byWorkspace: { workspace1: mac, workspace2: win } }
const response: UsageResponse = { days: [{ date: '2026-10-01', providers: { anthropic: combined } }], generatedAt: '', lastIngestAt: null,
  totals: { accounts: 2, accountsByProvider: {}, machines: 2, machinesLive: 2 } }
const days = normaliseDays(response)
assert.equal(machineDays(days, 'all').days, days)
for (const [id, want, model, cc] of [['mac', 100, 'model-mac', 'TH'], ['win', 900, 'model-win', 'GB']] as const) {
  const result = machineDays(days, id)
  assert.equal(result.partial, false)
  assert.equal(buildCells(result.days, DEFAULT_VIEW).get('2026-10-01')?.total, want)
  const p = result.days[0].providers.anthropic!
  assert.deepEqual(Object.keys(p.models.exact), [model])
  assert.deepEqual(Object.keys(p.byCountry), [cc])
  assert.equal(p.promptsNoUsage, 1)
  assert.deepEqual(p.accts, [], 'do not assign fleet account aliases to a machine')
}
assert.equal(buildCells(machineDays(days, 'missing').days, DEFAULT_VIEW).size, 0)
const shared = structuredClone(response)
const sharedGroup = { ...combined }
delete sharedGroup.byWorkspace
shared.days[0].providers.anthropic!.byWorkspace = { unknown: sharedGroup }
const fallback = machineDays(normaliseDays(shared), 'win')
assert.equal(fallback.partial, true)
assert.equal(buildCells(fallback.days, DEFAULT_VIEW).get('2026-10-01')?.total, 900)
assert.deepEqual(Object.keys(fallback.days[0].providers.anthropic!.models.exact), ['unknown'])
assert.equal(buildCells(days, DEFAULT_VIEW).get('2026-10-01')?.total, 1000, 'input is unchanged')
console.log('machine filtering: ok')
