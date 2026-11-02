// Owner-only recorded project usage in the selected account's current week.
// Read deduplicated events, not daily rollups: a reset can fall within a day.
import { createHmac } from 'node:crypto'
import { json } from './http.js'
import { resolveCtx } from './context.js'
import { ownerDecision } from './auth.js'
import { decryptJson, parseKey } from './crypto.js'
import { readLimits } from './limits.js'
import { labelled, rowToEvent } from './merge.js'
import { getPromptWorkspaces } from './prompts.js'
import { localDate } from './validate.js'
import { limiter, deadline } from './pool.js'

const DAY = 86_400_000
const WEEK = 7 * DAY
const PRIVATE = { 'Cache-Control': 'private, no-store', 'X-Content-Type-Options': 'nosniff' }
const FIELDS = ['kind', 'provider', 'source', 'ts', 'session', 'acct', 'acctQ', 'ws', 'q', 'in', 'cacheW', 'cacheR', 'out', 'calls']
const tokenCount = e => e.in + e.cacheW + e.cacheR + e.out
const joinKey = e => `${e.provider}|${e.session}|${Date.parse(e.ts)}`

// The same seven-day projection as the weekly gauge, including a stale
// reading whose named reset has passed more than once. Half-open weeks.
export function weeklyRange(resetsAt, nowMs) {
  if (typeof resetsAt !== 'string' || !/(?:Z|[+-]\d{2}:\d{2})$/i.test(resetsAt)) return null
  let end = Date.parse(resetsAt)
  if (!Number.isFinite(end)) return null
  const projected = end <= nowMs
  if (projected) end += (Math.floor((nowMs - end) / WEEK) + 1) * WEEK
  const start = end - WEEK
  if (start > nowMs) return null
  return { since: new Date(start).toISOString(), resetsAt: new Date(end).toISOString(), projected }
}

function datesBetween(start, end) {
  const dates = []
  for (let t = Date.parse(start.slice(0, 10)); t <= Date.parse(end.slice(0, 10)); t += DAY) dates.push(new Date(t).toISOString().slice(0, 10))
  return dates
}

// A collision or conflicting historical spelling must not misname a project.
function offer(map, key, value) {
  if (!key || !value) return
  if (!map.has(key)) map.set(key, value)
  else if (map.get(key) !== value) map.set(key, null)
}

async function projectNames(store, key, metadata, events, readDay, checkBudget, until) {
  const byTag = new Map()
  const byPrompt = new Map()
  const needed = new Set(events.filter(e => e.kind === 'usage').map(e => e.ws).filter(Boolean))
  for (const p of metadata.entries) {
    if (!p.workspace || !p.session || Date.parse(p.ts) > until) continue
    offer(byPrompt, joinKey(p), p.wsKey)
  }
  const matchActivities = rows => {
    for (const e of rows) if (e.kind === 'activity' && e.ws && e.session) {
      offer(byTag, e.ws, byPrompt.get(joinKey(e)))
    }
  }
  matchActivities(events)

  // Linked fleets escrow K under the existing prompt encryption key. Read
  // only; unlike link.fleetKey, this must never create or rotate a key.
  const escrow = await store.table('accounts').get('fleet', 'kenc')
  if (escrow?.enc) {
    const fleet = Buffer.from(decryptJson(key, 'fleet-k', escrow.enc), 'base64')
    if (fleet.length !== 32) throw new Error('Invalid fleet key')
    for (const p of metadata.entries) {
      if (!p.workspace || Date.parse(p.ts) > until) continue
      const tag = 'w_' + createHmac('sha256', fleet).update(`workspace|${p.workspace}`).digest('hex').slice(0, 16)
      offer(byTag, tag, p.wsKey)
    }
  }

  // A machine-created fleet may have no escrow. Join a prompt to its own
  // activity by provider/session/exact timestamp, then name that activity's
  // workspace tag. Include prompts before the reset for continuing sessions.
  // Never guess a usage event's workspace from its session alone: cwd moves.
  const unresolvedSessions = new Set(events.filter(e => e.ws && !byTag.get(e.ws)).map(e => `${e.provider}|${e.session}`))
  const latest = new Map()
  for (const p of metadata.entries) {
    if (!p.workspace || !p.session || Date.parse(p.ts) > until || !unresolvedSessions.has(`${p.provider}|${p.session}`)) continue
    const k = `${p.provider}|${p.session}|${p.workspace}`
    if (!latest.has(k) || latest.get(k).ts < p.ts) latest.set(k, p)
  }
  const days = [...new Set([...latest.values()].map(p => localDate(Date.parse(p.ts), p.tzOffsetMin)))].sort().reverse().slice(0, 16)
  const read = limiter(4)
  await Promise.all(days.map(date => read(async () => {
    if ([...needed].every(tag => byTag.get(tag))) return
    checkBudget()
    matchActivities(await readDay(date, true))
  })))
  return byTag
}

export async function handleLimitProjects(req, rawCtx) {
  const reply = (status, body) => json(status, body, PRIVATE)
  if (req.method !== 'GET') return reply(405, { error: 'method not allowed' })
  const decision = ownerDecision(req.headers, (rawCtx?.env ?? process.env).AGENTS_OWNER_IDS || '')
  if (decision.status === 401) return reply(401, { error: 'login required' })
  if (decision.status === 403) return reply(403, { error: 'not the owner' })
  const id = req.params?.id
  if (!/^[a-f0-9]{32}$/.test(id || '')) return reply(400, { error: 'invalid weekly meter' })
  const { store, env, budgetMs } = resolveCtx(rawCtx)
  const now = req.now ?? new Date()
  const nowMs = now.getTime()
  const snapshots = await readLimits(store, now)
  const meter = snapshots.find(row => row.id === id)
  if (!meter) return reply(404, { error: 'Weekly meter unavailable. Refresh the accounts.' })
  const base = { generatedAt: now.toISOString(), until: now.toISOString(), projects: [] }
  const unavailable = reason => reply(200, { ...base, available: false, reason })
  if (meter.window !== 'week' || meter.scope || meter.status === 'disabled') return unavailable('reset-unavailable')
  const window = weeklyRange(meter.resetsAt, nowMs)
  if (!window) return unavailable('reset-unavailable')
  if (!meter.acct || !labelled(meter.acctQ)) return unavailable('account-unavailable')
  // Event rows identify the login, not its organisation. Do not duplicate
  // that login's usage into distinct personal/Team quota rows.
  const orgs = new Set(snapshots.filter(r => r.provider === meter.provider && r.acct === meter.acct && r.org).map(r => r.org))
  if (orgs.size > 1) return unavailable('organisation-unavailable')
  let key
  try { key = parseKey(env.PROMPT_ENC_KEY) } catch { return reply(503, { error: 'Project names are temporarily unavailable.' }) }
  const budget = deadline(Math.min(budgetMs, 20000))
  const checkBudget = () => { if (budget.expired(1500)) throw new Error('Project lookup time budget exceeded') }
  const read = limiter(4)
  const dayCache = new Map()
  const readDay = (date, activityOnly = false) => {
    const cacheKey = `${date}|${activityOnly}`
    if (!dayCache.has(cacheKey)) dayCache.set(cacheKey, read(async () => {
      checkBudget()
      const rows = await store.table('events').list(date, {
        select: FIELDS, provider: meter.provider,
        ...(activityOnly ? { kind: 'activity' } : { acct: meter.acct }),
      })
      return rows.map(rowToEvent)
    }))
    return dayCache.get(cacheKey)
  }
  // Events are partitioned by machine-local dates (+/-14h), but filtered by
  // their UTC timestamps. Read the bordering dates; never prorate a day.
  const dates = datesBetween(new Date(Date.parse(window.since) - DAY).toISOString(), new Date(nowMs + DAY).toISOString())
  const [partitions, metadata] = await Promise.all([
    Promise.all(dates.map(d => readDay(d))), getPromptWorkspaces(store, key, nowMs),
  ])
  checkBudget()
  const unique = new Map()
  for (const event of partitions.flat()) {
    const ts = Date.parse(event.ts)
    if (event.provider !== meter.provider || event.acct !== meter.acct || ts < Date.parse(window.since) || ts > nowMs || !Number.isFinite(ts)) continue
    unique.set(event.id, event)
  }
  const events = [...unique.values()]
  const names = await projectNames(store, key, metadata, events, readDay, checkBudget, nowMs)
  checkBudget()
  const groups = new Map(metadata.workspaces.map(g => [g.key, g]))
  const totals = { tokens: 0, exactTokens: 0, estimatedTokens: 0, calls: 0, uncertainTokens: 0 }
  const projects = new Map()
  for (const e of events) {
    if (e.kind !== 'usage') continue
    const tokens = tokenCount(e)
    if (!labelled(e.acctQ)) { totals.uncertainTokens += tokens; continue }
    if (!tokens) continue
    const group = groups.get(names.get(e.ws))
    const projectKey = group?.key || 'unassigned'
    let project = projects.get(projectKey)
    if (!project) {
      project = { key: projectKey, name: group?.label || 'Unassigned project', path: group?.path || '', tokens: 0, exactTokens: 0, estimatedTokens: 0, calls: 0 }
      projects.set(projectKey, project)
    }
    const tier = e.q === 'estimated' ? 'estimatedTokens' : 'exactTokens'
    project.tokens += tokens
    project[tier] += tokens
    project.calls += e.calls || 1
    totals.tokens += tokens
    totals[tier] += tokens
    totals.calls += e.calls || 1
  }
  const ranked = [...projects.values()].sort((a, b) => b.tokens - a.tokens || a.name.localeCompare(b.name))
    .map(p => ({ ...p, percent: totals.tokens ? 100 * p.tokens / totals.tokens : 0 }))
  return reply(200, { ...base, ...window, available: true, totals, projects: ranked, undecryptablePrompts: metadata.undecryptable })
}

handleLimitProjects.responseHeaders = PRIVATE
