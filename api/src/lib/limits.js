// Plan-window snapshots (SPEC "Limit snapshots").
//
// Stored on the accounts table under partition "lim", which the public usage
// list never reads (it lists partition "a" and selects provider only). The
// row body is the validated snapshot. label may be the account email; this
// partition is owner-only and the public usage list never reads it. A newer
// observedAt replaces the row (a conditional write, retried when another
// writer got there first). An older one is ignored. GET is owner-only and
// leaves out a meter whose reset passed more than STALE_MS ago; plan rows, and
// rows with no reset, are always returned.
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { ownerDecision } from './auth.js'
import { isStatus } from './tables.js'

const PK = 'lim'
const PRIVATE = { 'Cache-Control': 'private, no-store' }
const PROVIDER_ORDER = ['anthropic', 'openai', 'xai', 'cursor', 'google']
const WINDOW_ORDER = ['session', 'week', 'extra', 'plan']
const RETRIES = 6
export const STALE_MS = 14 * 24 * 3600 * 1000

function authorise(req, env) {
  const d = ownerDecision(req.headers, env.AGENTS_OWNER_IDS || '')
  if (d.status === 401) return error(401, 'login required')
  if (d.status === 403) return error(403, 'not the owner')
  return null
}

// upsertLimits writes each snapshot when it is newer than the stored one.
// The write is conditional on the row read (create, or replace at its etag),
// so of two writers racing on one id the newer observedAt always ends up
// stored. A batch holding one id twice keeps its newest. An id that could not
// be written is returned so ingest can ask for a retry.
export async function upsertLimits(store, lim, items) {
  const failed = []
  const t = store.table('accounts')
  const byId = new Map()
  for (const item of items) {
    const held = byId.get(item.id)
    if (!held || held.observedAt < item.observedAt) byId.set(item.id, item)
  }
  await Promise.all([...byId.values()].map(async (item) => {
    try {
      for (let i = 0; i < RETRIES; i++) {
        const row = await lim(() => t.get(PK, item.id))
        if (row?.observedAt && row.observedAt >= item.observedAt) return
        const entity = {
          partitionKey: PK,
          rowKey: item.id,
          observedAt: item.observedAt,
          body: JSON.stringify(item),
        }
        try {
          if (row) await lim(() => t.replace(entity, row.etag))
          else await lim(() => t.create(entity))
          return
        } catch (err) {
          // Another writer changed (or created, or removed) the row: read again.
          if (!isStatus(err, 404, 409, 412)) throw err
        }
      }
      failed.push(item.id)
    } catch {
      failed.push(item.id)
    }
  }))
  return failed
}

// stale reports a meter whose window reset more than STALE_MS ago: a reading
// nobody refreshed since (an account no longer signed in anywhere) says
// nothing about the quota any more. A plan row, or any row with no reset time,
// never goes stale: collectors re-send unchanged plan rows only once a day (or,
// before that, never), and an account that is signed in, or one that is not
// at the moment, must stay listed.
export function stale(item, nowMs) {
  if (item.window === 'plan' || !item.resetsAt) return false
  const resets = Date.parse(item.resetsAt)
  return Number.isFinite(resets) && nowMs - resets > STALE_MS
}

export async function readLimits(store, now = new Date()) {
  const nowMs = now.getTime()
  const rows = await store.table('accounts').list(PK)
  const items = []
  for (const row of rows) {
    if (typeof row.body !== 'string') continue
    try {
      const item = JSON.parse(row.body)
      if (item && typeof item.id === 'string' && !stale(item, nowMs)) items.push(item)
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
