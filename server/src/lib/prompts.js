// GET /api/prompts[?month=all|YYYY-MM&order=asc|desc&provider=&model=&acct=&workspace=&machine=&q=&limit=&cursor=&facets=1]
// GET /api/prompts/facets        the filter values across the whole archive
// GET /api/prompts/{id}[?month=YYYY-MM]  (full text)
// Owner only (SPEC "Owner auth"). A missing month means all months; a
// missing order means oldest first (asc). The cursor carries its order.
//
// Every list and facet request is served from one decrypted index of the
// archive's metadata (and inline text for search), built by listing every
// month partition and kept per process for INDEX_TTL_MS. Ingest drops it
// when it writes prompts, so the owner sees new prompts at once on the same
// instance and within a minute everywhere else. List items carry at most a
// 4 KB preview (`text`, mirrored in `textPreview`) with `truncated`; the blob
// of a large prompt is fetched only for /{id}.
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { ownerDecision } from './auth.js'
import { decryptJson, parseKey } from './crypto.js'
import { PROMPT_INDEX, readMonths } from './months.js'
import { num } from './tables.js'
import { labelled, legacyPromptQ } from './merge.js'
import { isId, localDate, PROVIDERS, truncateUtf8 } from './validate.js'
import { PREVIEW_BYTES } from './ingest.js'
import { canonicalize, workspaceGroups } from './workspaces.js'

const MONTH_RE = /^\d{4}-\d{2}$/
const DEFAULT_LIMIT = 500
const MAX_LIMIT = 2000
const PRIVATE = { 'Cache-Control': 'private, no-store' }
export const INDEX_TTL_MS = 60_000
const PROVIDER_ORDER = ['anthropic', 'openai', 'xai', 'cursor', 'google']
// Only the columns the index needs (the SDK adds PartitionKey and RowKey).
const SELECT = ['ts', 'tzOffsetMin', 'provider', 'source', 'model', 'acctQ', 'blob', 'enc', 'penc']
// Month partitions listed at once.
const MONTH_PARALLEL = 8

function authorise(req, env) {
  const d = ownerDecision(req.headers, env.AGENTS_OWNER_IDS || '')
  if (d.status === 401) return error(401, 'login required')
  if (d.status === 403) return error(403, 'not the owner')
  return null
}

// The account facet key (SPEC "Accounts"). A prompt whose attribution is good
// enough to show a label (recorded, session, timeline, bounded) groups by
// account and quality; anything weaker is unattributed: one bucket per
// candidate account the collector inferred (named "probably <label>" when a
// labelled prompt of that account exists), else one per provider. A strong
// attribution to an account with no label (one that was never logged in
// while the collector watched) is still its own account: an unnamed group.
export function acctKeyOf(provider, acct, acctLabel, acctQ, probable) {
  if (labelled(acctQ) && (acctLabel || acct)) return `${acct || `label:${provider}:${acctLabel}`}~${acctQ}`
  if (acct && probable) return `unknown:${provider}:${acct}`
  return `unknown:${provider}`
}

// The stored quality of a row: plaintext since v1.4, else the legacy rule.
function rowQ(row, payload) {
  return row.acctQ || legacyPromptQ(payload.acct, payload.acctLabel)
}

function baseItem(row, payload) {
  const acctQ = rowQ(row, payload)
  return {
    id: row.rowKey,
    ts: row.ts,
    tzOffsetMin: num(row.tzOffsetMin),
    provider: row.provider,
    source: row.source,
    model: row.model || '',
    acctQ,
    acctLabel: labelled(acctQ) ? payload.acctLabel || '' : '',
    workspace: payload.workspace || '',
    machine: payload.machine || '',
    session: payload.session || '',
  }
}

// Newest first; id breaks ties so the cursor order is total. Oldest first is
// its exact reverse (ts, then id, ascending).
const newerFirst = (a, b) => (a.ts === b.ts ? (a.id < b.id ? 1 : a.id > b.id ? -1 : 0) : a.ts < b.ts ? 1 : -1)
const olderFirst = (a, b) => newerFirst(b, a)
const ORDERS = ['asc', 'desc']

async function mapLimit(list, n, fn) {
  const out = new Array(list.length)
  let next = 0
  await Promise.all(Array.from({ length: Math.min(n, list.length) }, async () => {
    while (next < list.length) {
      const i = next++
      out[i] = await fn(list[i])
    }
  }))
  return out
}

async function buildIndex(store, key) {
  const months = await readMonths(store)
  const t = store.table('prompts')
  const perMonth = await mapLimit(months, MONTH_PARALLEL, (m) => t.list(m, { select: SELECT }))
  const entries = []
  let undecryptable = 0
  months.forEach((month, mi) => {
    for (const row of perMonth[mi]) {
      let payload
      try {
        payload = decryptJson(key, row.rowKey, row.blob ? row.penc : row.enc)
      } catch {
        undecryptable++
        continue
      }
      const it = baseItem(row, payload)
      it.month = month
      it.acct = payload.acct || ''
      // The whole inline text (blob prompts: their preview) for search.
      it.text = payload.text || ''
      it.blob = Boolean(row.blob)
      entries.push(it)
    }
  })
  entries.sort(newerFirst)

  const wsCounts = new Map()
  for (const e of entries) wsCounts.set(e.workspace, (wsCounts.get(e.workspace) || 0) + 1)
  const rootOf = canonicalize(wsCounts)
  const workspaces = workspaceGroups(wsCounts, rootOf)
  const groupByKey = new Map(workspaces.map((g) => [g.key, g]))
  // The newest label of each account, for "probably <label>".
  const labelOf = new Map()
  for (const e of entries) if (e.acct && e.acctLabel && !labelOf.has(e.acct)) labelOf.set(e.acct, e.acctLabel)
  for (const e of entries) {
    e.wsKey = rootOf.get(e.workspace) || 'none'
    e.wsLabel = groupByKey.get(e.wsKey)?.label || ''
    e.acctProbable = !e.acctLabel && e.acct ? labelOf.get(e.acct) || '' : ''
    e.acctKey = acctKeyOf(e.provider, e.acct, e.acctLabel, e.acctQ, e.acctProbable)
  }
  return { at: Date.now(), months, entries, undecryptable, facets: computeFacets(entries, workspaces, months) }
}

function tally(entries, keyOf) {
  const m = new Map()
  for (const e of entries) {
    const k = keyOf(e)
    if (k) m.set(k, (m.get(k) || 0) + 1)
  }
  return m
}

const byCount = (a, b) => b.count - a.count || String(a.value).localeCompare(String(b.value))
const providerRank = (p) => (PROVIDER_ORDER.includes(p) ? PROVIDER_ORDER.indexOf(p) : PROVIDER_ORDER.length)

function computeFacets(entries, workspaces, months) {
  const accounts = new Map()
  // entries are newest first, so the first label seen is the current one.
  for (const e of entries) {
    let a = accounts.get(e.acctKey)
    if (!a) {
      const unattributed = e.acctKey.startsWith('unknown:')
      a = { acct: e.acctKey, provider: e.provider, label: unattributed ? '' : e.acctLabel, quality: unattributed ? '' : e.acctQ, count: 0, unattributed }
      if (unattributed && e.acctProbable) a.probable = e.acctProbable
      accounts.set(e.acctKey, a)
    }
    a.count++
  }
  const monthCounts = tally(entries, (e) => e.month)
  return {
    total: entries.length,
    providers: [...tally(entries, (e) => e.provider)].map(([provider, count]) => ({ provider, count }))
      .sort((a, b) => providerRank(a.provider) - providerRank(b.provider)),
    accounts: [...accounts.values()].sort((a, b) =>
      providerRank(a.provider) - providerRank(b.provider) || Number(a.unattributed) - Number(b.unattributed) ||
      Number(!a.probable) - Number(!b.probable) || b.count - a.count || a.label.localeCompare(b.label) || a.acct.localeCompare(b.acct)),
    workspaces,
    models: [...tally(entries, (e) => e.model)].map(([value, count]) => ({ value, count })).sort(byCount),
    machines: [...tally(entries, (e) => e.machine)].map(([value, count]) => ({ value, count })).sort(byCount),
    months: months.map((month) => ({ month, count: monthCounts.get(month) || 0 })),
  }
}

// One index per store and process; concurrent requests share one build.
async function getIndex(store, key, nowMs) {
  const cached = store.cache.get(PROMPT_INDEX)
  if (cached?.index && nowMs - cached.index.at < INDEX_TTL_MS) return cached.index
  if (cached?.pending) return cached.pending
  const pending = buildIndex(store, key).then((index) => {
    const cur = store.cache.get(PROMPT_INDEX)
    if (cur?.pending === pending) store.cache.set(PROMPT_INDEX, { index })
    return index
  }, (err) => {
    if (store.cache.get(PROMPT_INDEX)?.pending === pending) store.cache.delete(PROMPT_INDEX)
    throw err
  })
  store.cache.set(PROMPT_INDEX, { index: cached?.index, pending })
  return pending
}

// Public counts share the private index's lifetime and ingest invalidation,
// but construct a new allow-listed result. Never return or spread an entry.
export async function getPromptCountAggregates(store, key, machines, nowMs = Date.now()) {
  const index = await getIndex(store, key, nowMs)
  const ids = new Set(machines.map(m => m.id))
  const labels = new Map()
  for (const m of machines) {
    if (!m.label) continue
    labels.set(m.label, labels.has(m.label) ? null : m.id)
  }
  const machineOf = value => ids.has(value) ? value : labels.get(value) || 'unknown'
  const counter = () => ({ prompts: 0, byModel: new Map() })
  const increment = (group, model) => {
    group.prompts++
    group.byModel.set(model, (group.byModel.get(model) || 0) + 1)
  }
  const sorted = map => Object.fromEntries([...map].sort(([a], [b]) => a.localeCompare(b)))
  const finish = group => ({ prompts: group.prompts, byModel: sorted(group.byModel) })
  const days = new Map()
  let total = 0
  for (const entry of index.entries) {
    if (!PROVIDERS.includes(entry.provider)) continue
    const tsMs = Date.parse(entry.ts)
    if (!Number.isFinite(tsMs)) continue
    const date = localDate(tsMs, entry.tzOffsetMin)
    let providers = days.get(date)
    if (!providers) days.set(date, (providers = new Map()))
    let group = providers.get(entry.provider)
    if (!group) providers.set(entry.provider, (group = { ...counter(), byMachine: new Map() }))
    const model = entry.model || 'unknown'
    const machine = machineOf(entry.machine)
    let machineGroup = group.byMachine.get(machine)
    if (!machineGroup) group.byMachine.set(machine, (machineGroup = counter()))
    increment(group, model)
    increment(machineGroup, model)
    total++
  }
  return {
    source: 'archive', generatedAt: new Date(index.at).toISOString(), undecryptable: index.undecryptable, total,
    days: [...days].sort(([a], [b]) => a.localeCompare(b)).map(([date, providers]) => ({
      date,
      providers: Object.fromEntries([...providers].sort(([a], [b]) => a.localeCompare(b)).map(([provider, group]) => [provider, {
        ...finish(group),
        byMachine: Object.fromEntries([...group.byMachine].sort(([a], [b]) => a.localeCompare(b)).map(([id, counts]) => [id, finish(counts)])),
        byMachineComplete: !group.byMachine.has('unknown'),
      }])),
    })),
  }
}

function encodeCursor(item, order) {
  return Buffer.from(JSON.stringify({ ts: item.ts, id: item.id, o: order })).toString('base64url')
}

function decodeCursor(s) {
  try {
    const c = JSON.parse(Buffer.from(s, 'base64url').toString('utf8'))
    // Cursors from before `order` existed (no `o`) walked newest first.
    if (typeof c?.ts !== 'string' || typeof c?.id !== 'string') return null
    return { ts: c.ts, id: c.id, o: ORDERS.includes(c.o) ? c.o : 'desc' }
  } catch {
    return null
  }
}

function listItem(e) {
  const cut = truncateUtf8(e.text, PREVIEW_BYTES)
  return {
    id: e.id, ts: e.ts, tzOffsetMin: e.tzOffsetMin, month: e.month,
    provider: e.provider, source: e.source, model: e.model,
    acct: e.acctKey, acctQ: e.acctQ, acctLabel: e.acctLabel, acctProbable: e.acctProbable,
    workspace: e.workspace, workspaceKey: e.wsKey, workspaceLabel: e.wsLabel,
    machine: e.machine, session: e.session,
    text: cut.text, textPreview: cut.text, truncated: e.blob || cut.truncated,
  }
}

const contains = (value, needle) => !needle || String(value || '').toLowerCase().includes(needle)

export async function handlePrompts(req, rawCtx) {
  if (req.method !== 'GET') return error(405, 'method not allowed')
  const { store, env } = resolveCtx(rawCtx)
  const denied = authorise(req, env)
  if (denied) return denied
  let key
  try {
    key = parseKey(env.PROMPT_ENC_KEY)
  } catch {
    return error(503, 'prompt key not configured')
  }
  const nowMs = Date.now()
  if (req.params?.id === 'facets') {
    const index = await getIndex(store, key, nowMs)
    return json(200, { generatedAt: new Date(index.at).toISOString(), undecryptable: index.undecryptable, ...index.facets }, PRIVATE)
  }
  if (req.params?.id) return promptItem(req, store, key)

  const q = req.query
  const month = !q.month || q.month === 'all' ? 'all' : q.month
  if (month !== 'all' && !MONTH_RE.test(month)) return error(400, 'month must be YYYY-MM or all')
  const order = q.order || 'asc'
  if (!ORDERS.includes(order)) return error(400, 'order must be asc or desc')
  const cmp = order === 'asc' ? olderFirst : newerFirst
  const limit = Math.min(Math.max(parseInt(q.limit, 10) || DEFAULT_LIMIT, 1), MAX_LIMIT)
  let cursor = null
  if (q.cursor) {
    cursor = decodeCursor(q.cursor)
    if (!cursor) return error(400, 'bad cursor')
    if (cursor.o !== order) return error(400, 'cursor belongs to the other order')
  }
  const needle = {
    q: (q.q || '').toLowerCase(),
    acctLabel: (q.acctLabel || '').toLowerCase(),
  }
  // provider, model, acct and machine match exactly (facet values); workspace
  // takes a facet key or a raw path as stored; acctLabel and q are substrings.
  const ws = q.workspace || ''
  const wsKey = ws === 'none' ? 'none' : ws

  const index = await getIndex(store, key, nowMs)
  const matches = []
  for (const e of index.entries) {
    if (month !== 'all' && e.month !== month) continue
    if (q.provider && e.provider !== q.provider) continue
    if (q.acct && e.acctKey !== q.acct) continue
    if (ws && e.wsKey !== wsKey && e.workspace !== ws) continue
    if (q.machine && e.machine !== q.machine) continue
    if (q.model && e.model !== q.model) continue
    if (!contains(e.acctLabel, needle.acctLabel)) continue
    if (!contains(e.text, needle.q)) continue
    matches.push(e)
  }
  // The index is newest first; oldest first is the same list reversed.
  if (order === 'asc') matches.reverse()
  const start = cursor ? matches.findIndex((e) => cmp(e, cursor) > 0) : 0
  const page = start < 0 ? [] : matches.slice(start, start + limit)
  const more = start >= 0 && start + limit < matches.length
  const body = {
    month,
    order,
    items: page.map(listItem),
    months: index.months,
    cursor: more ? encodeCursor(page[page.length - 1], order) : null,
    total: matches.length,
    undecryptable: index.undecryptable,
  }
  if (q.facets === '1' || q.facets === 'true') body.facets = index.facets
  return json(200, body, PRIVATE)
}

async function promptItem(req, store, key) {
  const id = req.params.id
  if (!isId(id)) return error(400, 'id must be 32 lowercase hex')
  const month = req.query.month
  if (month && !MONTH_RE.test(month)) return error(400, 'month must be YYYY-MM')
  const t = store.table('prompts')
  let row = null
  if (month) {
    row = await t.get(month, id)
  } else {
    for (const m of await readMonths(store)) {
      row = await t.get(m, id)
      if (row) break
    }
  }
  if (!row) return error(404, 'prompt not found')
  let enc = row.enc
  if (row.blob) {
    enc = await store.blobs.get(id)
    if (!enc) return error(404, 'prompt blob missing')
  }
  let payload
  try {
    payload = decryptJson(key, id, enc)
  } catch {
    return error(500, 'prompt could not be decrypted')
  }
  return json(200, { ...baseItem(row, payload), text: payload.text || '', truncated: false }, PRIVATE)
}
