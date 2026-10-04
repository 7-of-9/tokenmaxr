// Assertions for heat.ts. Run: node --experimental-strip-types src/components/agents/heat.test.ts
import assert from 'node:assert/strict'
import { contrastRatio, GH_LEVELS, luminance, makeHeatScale, quantile } from './heat.ts'

// GitHub's dark palette, exactly: empty, then four greens that get strictly lighter.
assert.deepEqual([...GH_LEVELS], ['#151b23', '#033a16', '#196c2e', '#2ea043', '#56d364'])
for (let i = 1; i < GH_LEVELS.length; i += 1) assert.ok(luminance(GH_LEVELS[i]) > luminance(GH_LEVELS[i - 1]), `level ${i} is lighter`)
// The lowest green still separates from an empty day, and the top one reads against the panel.
assert.ok(contrastRatio(GH_LEVELS[1], GH_LEVELS[0]) > 1.2)
assert.ok(contrastRatio(GH_LEVELS[4], '#050f0a') > 8)

// Quantiles interpolate.
assert.equal(quantile([0, 10], 0.5), 5)
assert.equal(quantile([1, 2, 3], 1), 3)
assert.ok(Number.isNaN(quantile([], 0.5)))

// Quartiles of the non-empty days: 1…100 lands 25 days in each level.
const hundred = Array.from({ length: 100 }, (_, i) => i + 1)
const scale = makeHeatScale([0, 0, ...hundred])
const counts = [0, 0, 0, 0, 0]
for (const v of hundred) counts[scale.level(v)] += 1
assert.deepEqual(counts, [0, 25, 25, 25, 25])
assert.equal(scale.level(0), 0)
assert.equal(scale.level(-5), 0)
assert.equal(scale.level(Number.NaN), 0)

// One extreme day is simply the top level: it does not push every other day into level 1.
const skewed = makeHeatScale([...hundred.map((v) => v * 1e6), 1e15])
assert.equal(skewed.level(1e15), 4)
assert.equal(skewed.level(90e6), 4)
assert.equal(skewed.level(10e6), 1)
assert.equal(skewed.level(40e6), 2)

// Levels never go down as the value goes up.
let previous = 0
for (let v = 1; v <= 120; v += 1) {
  const level = scale.level(v)
  assert.ok(level >= previous, `monotonic at ${v}`)
  previous = level
}

// No data: every value is empty.
assert.equal(makeHeatScale([]).level(5), 0)
assert.equal(makeHeatScale([0, -1]).level(5), 0)
// Identical days share a level; a larger value is the top level.
const flat = makeHeatScale([5, 5, 5])
assert.equal(flat.level(5), 1)
assert.equal(flat.level(6), 4)

console.log('heat.test.ts: ok')
