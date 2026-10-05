// /agents: one card per provider account from GET /api/limits (SPEC "Limit snapshots"). Pure, so it is tested
// without a browser. Run: node --experimental-strip-types src/components/agents/limits.test.ts
import type { Provider } from './types'

/** One plan window as the tool last wrote it. */
export interface LimitRow {
  id: string
  provider: Provider
  source: string
  acct: string
  acctQ: string
  plan?: string
  /** session (the 5-hour window), week, extra, plan, or another short token the tool used. */
  window: string
  /** A model the window is limited to, e.g. "Fable". */
  scope?: string
  /** Organisation name. */
  name?: string
  /** Account email (owner-only read). */
  label?: string
  detail?: string
  usedPercent?: number
  resetsAt?: string | null
  observedAt: string
  /** ok, full or disabled. Older collectors also wrote "paused" for any limit Claude did not lead with: ignored. */
  status?: string
}

/** A source refused the owner's limits: 401 not signed in (or locked), 403 not the owner (or the wrong key). */
export class LimitsDenied extends Error {
  readonly status: 401 | 403

  constructor(status: 401 | 403) {
    super(status === 401 ? 'Sign-in required.' : 'Not the owner.')
    this.name = 'LimitsDenied'
    this.status = status
  }
}

export const TOOL_LABEL: Record<string, string> = {
  'claude-code': 'Claude',
  codex: 'Codex',
  'grok-cli': 'Grok',
  cursor: 'Cursor',
  'gemini-cli': 'Gemini',
}

const PROVIDER_ORDER: Provider[] = ['anthropic', 'openai', 'xai', 'cursor', 'google']

export interface Meter {
  key: string
  window: string
  scope?: string
  detail?: string
  /** Percent used at the reading; null when the tool wrote none. */
  used: number | null
  resetsAt: number | null
  observedAt: number
  /** The window has reset since the reading, so the reading no longer says how much is used. */
  lapsed: boolean
  /** Used up and not reset yet. */
  full: boolean
}

export type AccountState = 'ready' | 'blocked' | 'unknown'

export interface Account {
  key: string
  provider: Provider
  tool: string
  email: string
  plan: string
  org: string
  /** The unscoped 5-hour and weekly windows. */
  session?: Meter
  week?: Meter
  /** Model-scoped windows, e.g. a week limited to one model. */
  scoped: Meter[]
  /** Pay-as-you-go credits past the plan, when the tool reports them. */
  extra?: { enabled: boolean; detail: string }
  observedAt: number
  state: AccountState
  /** Blocked: the window that blocks (the one that comes back last). */
  blockedBy?: Meter
}

function time(iso?: string | null): number | null {
  // API timestamps carry a timezone; never interpret a zone-less value in the viewer's timezone.
  if (!iso || !/(?:Z|[+-]\d{2}:\d{2})$/i.test(iso)) return null
  const ms = new Date(iso).getTime()
  return Number.isFinite(ms) ? ms : null
}

function toMeter(row: LimitRow, now: number): Meter {
  const used = typeof row.usedPercent === 'number' && Number.isFinite(row.usedPercent) && row.usedPercent >= 0 && row.usedPercent <= 100 ? row.usedPercent : null
  const resetsAt = time(row.resetsAt)
  const observedAt = time(row.observedAt) ?? 0
  const lapsed = resetsAt !== null && resetsAt <= now
  const hit = row.status === 'full' || (used !== null && used >= 100)
  return {
    key: row.id,
    window: row.window,
    scope: row.scope || undefined,
    detail: row.detail || undefined,
    used,
    resetsAt,
    observedAt,
    lapsed,
    full: hit && !lapsed && observedAt > 0,
  }
}

function currentMeter(meter?: Meter): meter is Meter & { used: number } {
  return !!meter && meter.used !== null && Number.isFinite(meter.used) && meter.used >= 0 && meter.used <= 100 &&
    !meter.lapsed && Number.isFinite(meter.observedAt) && meter.observedAt > 0
}

/** Only a usable unscoped weekly reading belongs in the weekly capacity comparison. Zero is valid. */
export function weeklyMeter(account: Account): (Meter & { used: number }) | undefined {
  return currentMeter(account.week) ? account.week : undefined
}

/** Browser-local choices; both true and false are explicit, durable decisions. */
export type AccountTracking = Record<string, boolean>

/** Owner-supplied, browser-local clarifications, bound to the source label they clarify. */
export type AccountPlanOverrides = Record<string, { name: string; reportedPlan: string }>

/** No machine, plan, row ID or meter timestamp is part of an account's tracking identity. */
export function accountTrackingKey(account: Account): string {
  const email = account.email.trim().toLowerCase()
  return JSON.stringify([account.provider, email ? 'email' : 'account', email || account.key])
}

/** Recover valid choices from storage, without treating malformed entries as unchecked accounts. */
export function parseTracking(raw: string | null): AccountTracking {
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return Object.fromEntries(Object.entries(parsed).filter(([key, value]) => key.length > 0 && typeof value === 'boolean'))
  } catch { return {} }
}

/** No account-specific identities or plan guesses are bundled with the application. */
export function parsePlanOverrides(raw: string | null): AccountPlanOverrides {
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const entries: [string, { name: string; reportedPlan: string }][] = []
    for (const [key, value] of Object.entries(parsed)) {
      if (!key || !value || typeof value !== 'object' || Array.isArray(value)) continue
      const { name, reportedPlan } = value as Record<string, unknown>
      if (typeof name !== 'string' || typeof reportedPlan !== 'string' || !name.trim() ||
        name.length > 80 || reportedPlan.length > 80 || [...name + reportedPlan].some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)) continue
      entries.push([key, { name: name.trim(), reportedPlan: reportedPlan.trim() }])
    }
    return Object.fromEntries(entries)
  } catch { return {} }
}

/** Set defaults once. A later lapse, refresh, or temporary disappearance never changes a saved choice. */
export function initializeTracking(accounts: Account[], choices: AccountTracking): AccountTracking {
  let result = choices
  for (const account of accounts) {
    const key = accountTrackingKey(account)
    if (Object.hasOwn(result, key) && typeof result[key] === 'boolean') continue
    if (result === choices) result = { ...choices }
    result[key] = !!weeklyMeter(account)
  }
  return result
}

/** The newest reading of a window. */
function newest(meters: Meter[]): Meter | undefined {
  return meters.reduce<Meter | undefined>((best, m) => (!best || m.observedAt > best.observedAt ? m : best), undefined)
}

/** An internal tier code (default_raven) names no plan. */
function planName(plan?: string): string {
  const p = (plan || '').trim()
  return p.toLowerCase().startsWith('default_') ? '' : p
}

function toAccount(rows: LimitRow[], now: number): Account {
  const head = rows.reduce((a, b) => ((time(b.observedAt) ?? 0) > (time(a.observedAt) ?? 0) ? b : a))
  const email = (head.label || '').trim()
  const metered = rows.filter((r) => r.window !== 'plan' && r.window !== 'extra' && r.status !== 'disabled').map((r) => toMeter(r, now))
  const session = newest(metered.filter((m) => m.window === 'session' && !m.scope))
  const week = newest(metered.filter((m) => m.window === 'week' && !m.scope))
  const scoped = metered
    .filter((m) => m.scope || (m.window !== 'session' && m.window !== 'week'))
    .sort((a, b) => (a.scope || a.window).localeCompare(b.scope || b.window))
  const extraRow = rows.find((r) => r.window === 'extra')
  // Windows can arrive in any order, including snapshots from before an upgrade/downgrade.
  const planRow = rows.filter((r) => !!planName(r.plan)).reduce<LimitRow | undefined>(
    (latest, row) => !latest || (time(row.observedAt) ?? 0) > (time(latest.observedAt) ?? 0) ? row : latest,
    undefined,
  )
  const plan = planName(planRow?.plan)
  const org = rows.map((r) => (r.name || '').trim()).find(Boolean) ?? ''

  // A full scoped window blocks one model, not the account.
  const blocking = [session, week].filter((m): m is Meter => !!m && m.full)
  const blockedBy = blocking.reduce<Meter | undefined>((last, m) => (!last || (m.resetsAt ?? Infinity) > (last.resetsAt ?? Infinity) ? m : last), undefined)
  const state: AccountState = blockedBy ? 'blocked' : [session, week].some(currentMeter) ? 'ready' : 'unknown'

  return {
    key: `${head.provider}|${email.toLowerCase() || head.acct || head.source}`,
    provider: head.provider,
    tool: TOOL_LABEL[head.source] ?? head.source,
    email,
    plan,
    org,
    session,
    week,
    scoped,
    extra: extraRow ? { enabled: extraRow.status !== 'disabled', detail: (extraRow.detail || '').trim() } : undefined,
    observedAt: Math.max(...rows.map((r) => time(r.observedAt) ?? 0)),
    state,
    blockedBy,
  }
}

/** One account per provider and email, in a fixed provider order so cards never reshuffle as states change. */
export function buildAccounts(items: LimitRow[], now: number, planOverrides: AccountPlanOverrides = {}): Account[] {
  const groups = new Map<string, LimitRow[]>()
  for (const item of items) {
    const key = `${item.provider}|${(item.label || '').trim().toLowerCase() || item.acct || item.source}`
    const list = groups.get(key)
    if (list) list.push(item)
    else groups.set(key, [item])
  }
  const rank = (p: Provider) => {
    const i = PROVIDER_ORDER.indexOf(p)
    return i < 0 ? PROVIDER_ORDER.length : i
  }
  return [...groups.values()]
    .map((rows) => {
      const account = toAccount(rows, now)
      const override = planOverrides[accountTrackingKey(account)]
      // A later, more specific provider report wins over a clarification of an older label.
      if (override && planName(override.reportedPlan).toLowerCase() === account.plan.toLowerCase()) {
        return { ...account, plan: override.name }
      }
      return account
    })
    .sort((a, b) => rank(a.provider) - rank(b.provider) || a.email.localeCompare(b.email) || a.tool.localeCompare(b.tool))
}

export interface CountdownPart {
  value: number
  unit: 'day' | 'hour' | 'minute' | 'second'
}

/** The two largest adjacent units; invalid timestamps have no countdown. */
export function countdownParts(ms: number): CountdownPart[] {
  if (!Number.isFinite(ms)) return []
  const s = Math.max(0, Math.floor(ms / 1000))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d > 0) return [{ value: d, unit: 'day' }, { value: h, unit: 'hour' }]
  if (h > 0) return [{ value: h, unit: 'hour' }, { value: m, unit: 'minute' }]
  if (m > 0) return [{ value: m, unit: 'minute' }, { value: sec, unit: 'second' }]
  return [{ value: sec, unit: 'second' }]
}

/** "2d 4h", "3h 07m", "12m 05s", "45s": the two largest units, for a reset that is coming. */
export function countdown(ms: number): string {
  const parts = countdownParts(ms)
  if (!parts.length) return '—'
  return parts.map(({ value, unit }, i) => `${i && unit !== 'hour' ? String(value).padStart(2, '0') : value}${unit[0]}`).join(' ')
}

/** Badness, not availability: green at 0, amber at 0.5, red at 1. */
export function severityColor(ratio: number): string {
  if (!Number.isFinite(ratio)) return 'rgb(139, 148, 158)'
  const value = Math.min(1, Math.max(0, ratio))
  const stops = [[57, 211, 83], [224, 173, 67], [239, 68, 68]]
  const step = value < 0.5 ? 0 : 1
  const blend = value * 2 - step
  const rgb = stops[step].map((channel, i) => Math.round(channel + (stops[step + 1][i] - channel) * blend))
  return `rgb(${rgb.join(', ')})`
}

/** A weekly reset that is close is green; a full seven days away is red. */
export function resetSeverity(resetsAt: number | null, now: number): number | null {
  if (resetsAt === null || !Number.isFinite(resetsAt) || !Number.isFinite(now)) return null
  return Math.min(1, Math.max(0, (resetsAt - now) / (7 * 86_400_000)))
}

/** "just now", "4 min ago", "3 h ago", "12 Sept". */
export function ago(ms: number, now: number): string {
  const diff = now - ms
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.round(diff / 60_000)} min ago`
  if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)} h ago`
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short' }).format(new Date(ms))
}

/** A reset moment: "15:20" today, "Thu 15:20" in the coming week, "12 Oct" further out or past. */
export function clock(ms: number, now: number): string {
  const at = new Date(ms)
  const hm = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' }).format(at)
  if (new Date(now).toDateString() === at.toDateString()) return hm
  if (ms > now && ms - now < 6 * 86_400_000) return `${new Intl.DateTimeFormat('en-GB', { weekday: 'short' }).format(at)} ${hm}`
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short' }).format(at)
}

/** Synthetic accounts for ?mock=1, relative to now: one blocked on its session, one on its week, one ready, one stale. */
export function mockLimits(now: number): LimitRow[] {
  const at = (ms: number) => new Date(now + ms).toISOString()
  const MIN = 60_000
  const H = 60 * MIN
  const D = 24 * H
  const row = (r: Partial<LimitRow> & Pick<LimitRow, 'id' | 'provider' | 'source' | 'window'>): LimitRow => ({
    acct: '',
    acctQ: 'unknown',
    observedAt: at(-4 * MIN),
    status: 'ok',
    ...r,
  })
  return [
    row({ id: 'm1', provider: 'anthropic', source: 'claude-code', window: 'session', label: 'owner@example.com', plan: 'Max (20x)', usedPercent: 100, status: 'full', resetsAt: at(2 * H + 14 * MIN) }),
    row({ id: 'm2', provider: 'anthropic', source: 'claude-code', window: 'week', label: 'owner@example.com', plan: 'Max (20x)', usedPercent: 64, resetsAt: at(3 * D + 5 * H) }),
    row({ id: 'm3', provider: 'anthropic', source: 'claude-code', window: 'week', scope: 'Fable', label: 'owner@example.com', plan: 'Max (20x)', usedPercent: 81, resetsAt: at(3 * D + 5 * H) }),
    row({ id: 'm4', provider: 'anthropic', source: 'claude-code', window: 'session', label: 'work@example.org', name: 'Example Org', usedPercent: 22, resetsAt: at(3 * H) }),
    row({ id: 'm5', provider: 'anthropic', source: 'claude-code', window: 'week', label: 'work@example.org', name: 'Example Org', usedPercent: 100, status: 'full', resetsAt: at(1 * D + 9 * H) }),
    row({ id: 'm6', provider: 'anthropic', source: 'claude-code', window: 'extra', label: 'work@example.org', status: 'disabled', detail: 'out of credits' }),
    row({ id: 'm7', provider: 'openai', source: 'codex', window: 'session', label: 'owner@example.com', plan: 'Pro', usedPercent: 9, resetsAt: at(4 * H) }),
    row({ id: 'm8', provider: 'openai', source: 'codex', window: 'week', label: 'owner@example.com', plan: 'Pro', usedPercent: 37, resetsAt: at(5 * D) }),
    row({ id: 'm9', provider: 'openai', source: 'codex', window: 'week', label: 'side@example.net', plan: 'Plus', usedPercent: 88, resetsAt: at(-2 * D), observedAt: at(-9 * D) }),
    row({ id: 'm10', provider: 'xai', source: 'grok-cli', window: 'plan', label: 'owner@example.com', plan: 'SuperGrok Heavy', status: undefined }),
    row({ id: 'm11', provider: 'cursor', source: 'cursor', window: 'plan', label: 'owner@example.com', status: undefined }),
  ]
}
