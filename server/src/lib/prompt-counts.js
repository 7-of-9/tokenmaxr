// Public archive counts only. Prompt contents and attribution remain owner-only.
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { parseKey } from './crypto.js'
import { getPromptCountAggregates } from './prompts.js'
import { cleanLabel } from './validate.js'

export async function handlePromptCounts(req, rawCtx) {
  if (req.method !== 'GET') return error(405, 'method not allowed')
  const { store, env } = resolveCtx(rawCtx)
  let key
  try { key = parseKey(env.PROMPT_ENC_KEY) } catch { return error(503, 'prompt counts unavailable') }
  const rows = await store.table('machines').list('m', { select: ['label'] })
  const machines = rows.map(row => ({ id: row.rowKey, label: cleanLabel(row.label) || row.rowKey }))
  const counts = await getPromptCountAggregates(store, key, machines)
  return json(200, counts, { 'Cache-Control': 'public, max-age=60', 'X-Content-Type-Options': 'nosniff' })
}
