// Handler dependencies. The Functions host and the harness use the Azure store
// from env; unit tests pass { store: memoryStore(), env, budgetMs }.
// rollupMinAgeMs (default 20 s, SPEC "Rollup") lets a test ask ingest to
// recompute every written day at once, so a read right after sees it.
import { getDefaultStore } from './tables.js'
import { ROLLUP_MIN_AGE_MS } from './days.js'

export function resolveCtx(ctx = {}) {
  const env = ctx.env ?? process.env
  return {
    env,
    store: ctx.store ?? getDefaultStore(env),
    budgetMs: ctx.budgetMs ?? 30000,
    // Storage calls in flight per request. Ingest is latency-bound: 64 about
    // doubled backfill throughput over 24 (test/load.js) while staying far
    // below the per-partition limit of 2,000 operations a second.
    parallel: ctx.parallel ?? 64,
    rollupMinAgeMs: ctx.rollupMinAgeMs ?? ROLLUP_MIN_AGE_MS,
  }
}
