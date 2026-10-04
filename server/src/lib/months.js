// The list of months that hold prompts, newest first, kept in one row
// (accounts table, PK "meta", RK "months") so the prompts page can offer a
// month picker without scanning partitions.
import { isStatus } from './tables.js'

// store.cache key of the prompt archive index (prompts.js); ingest drops it.
export const PROMPT_INDEX = 'promptIndex'

const PK = 'meta'
const RK = 'months'
const RETRIES = 6

function parse(row) {
  try {
    const list = JSON.parse(row?.months || '[]')
    return Array.isArray(list) ? list.filter((m) => /^\d{4}-\d{2}$/.test(m)) : []
  } catch {
    return []
  }
}

export async function readMonths(store) {
  const row = await store.table('accounts').get(PK, RK)
  return parse(row).sort().reverse()
}

export async function addMonths(store, lim, months) {
  let known = store.cache.get('months')
  if (!known) store.cache.set('months', (known = new Set()))
  const missing = months.filter((m) => !known.has(m))
  if (missing.length === 0) return
  const t = store.table('accounts')
  for (let i = 0; i < RETRIES; i++) {
    const row = await lim(() => t.get(PK, RK))
    const list = new Set(parse(row))
    const before = list.size
    for (const m of missing) list.add(m)
    const entity = { partitionKey: PK, rowKey: RK, months: JSON.stringify([...list].sort().reverse()) }
    try {
      if (!row) await lim(() => t.create(entity))
      else if (list.size !== before) await lim(() => t.replace(entity, row.etag))
      for (const m of list) known.add(m)
      return
    } catch (err) {
      if (!isStatus(err, 404, 409, 412)) throw err
    }
  }
  throw new Error('months row contention')
}
