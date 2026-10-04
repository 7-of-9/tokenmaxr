// Plan-window snapshots (SPEC "Limit snapshots").
//
// Stored on the accounts table under partition "lim", which the public usage
// list never reads (it lists partition "a" and selects provider only). The
// row body is the validated snapshot. label may be the account email; this
// partition is owner-only and the public usage list never reads it. A newer
// observedAt replaces the row. An older one is ignored. GET is owner-only.
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { ownerDecision } from './auth.js'

const PK = 'lim'
const PRIVATE = { 'Cache-Control': 'private, no-store' }
const PROVIDER_ORDER = ['anthropic', 'openai', 'xai', 'cursor', 'google']
const WINDOW_ORDER = ['session', 'week', 'extra', 'plan']

function authorise(req, env) {
  const d = ownerDecision(req.headers, env.AGENTS_OWNER_IDS || '')
  if (d.status === 401) return error(401, 'login required')
  if (d.status === 403) return error(403, 'not the owner')
  return null
}

// upsertLimits writes each snapshot when it is newer than the stored one.
// An id that could not be written is returned so ingest can ask for a retry.
export async function upsertLimits(store, lim, items) {
  const failed = []
  const t = store.table('accounts')
  await Promise.all(items.map(async (item) => {
    try {
      const row = await lim(() => t.get(PK, item.id))
      if (row?.observedAt && row.observedAt >= item.observedAt) return
      await lim(() => t.upsertMerge({
        partitionKey: PK,
        rowKey: item.id,
        observedAt: item.observedAt,
        body: JSON.stringify(item),
      }))
    } catch {
      failed.push(item.id)
    }
  }))
  return failed
}

export async function readLimits(store) {
  const rows = await store.table('accounts').list(PK)
  const items = []
  for (const row of rows) {
    if (typeof row.body !== 'string') continue
    try {
      const item = JSON.parse(row.body)
      if (item && typeof item.id === 'string') items.push(item)
    } catch {
      // A corrupt row is skipped, not fatal.
    }
  }
  const rank = (list, v) => {
    const i = list.indexOf(v)
    return i < 0 ? list.length : i
  }
  items.sort((a, b) =>
    rank(PROVIDER_ORDER, a.provider) - rank(PROVIDER_ORDER, b.provider) ||
    String(a.name || '').localeCompare(String(b.name || '')) ||
    String(a.plan || '').localeCompare(String(b.plan || '')) ||
    rank(WINDOW_ORDER, a.window) - rank(WINDOW_ORDER, b.window) ||
    String(a.scope || '').localeCompare(String(b.scope || '')) ||
    String(a.resetsAt || '9999').localeCompare(String(b.resetsAt || '9999')))
  return items
}

export async function handleLimits(req, rawCtx) {
  if (req.method !== 'GET') return error(405, 'method not allowed')
  const ctx = resolveCtx(rawCtx)
  const denied = authorise(req, ctx.env)
  if (denied) return denied
  const items = await readLimits(ctx.store)
  return json(200, { items }, PRIVATE)
}
