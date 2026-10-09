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
  /** Hashed organisation (Claude). Absent from older collectors and other providers. */
  org?: string
  /** team, enterprise or personal: one Claude login can be a Team seat and a personal organisation, two accounts. */
  orgKind?: string
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
  /** team, enterprise or personal; '' for rows from before the collector reported organisations. */
  orgKind: string
  /** The hashed organisation ('' when unknown): a Team or Enterprise organisation's identity, stable across renames. */
  orgId: string
  /** Provider and email (else acct or source): the account's identity before organisations were reported. */
  identity: string
  /** "email · plan", or "email · organisation" without a plan: what tells two accounts of one email apart. */
  label: string
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

/**
 * How long a window lasts, by source and window, for a reading that never said when it resets: only where the
 * length is known (Claude Code's 5-hour session and 7-day week). Codex maps windows of other lengths to the same
 * names, so its readings are left alone.
 */
const WINDOW_MS: Record<string, number> = { 'claude-code|session': 5 * 3_600_000, 'claude-code|week': 7 * 86_400_000 }

function toMeter(row: LimitRow, now: number): Meter {
  const used = typeof row.usedPercent === 'number' && Number.isFinite(row.usedPercent) && row.usedPercent >= 0 && row.usedPercent <= 100 ? row.usedPercent : null
  const resetsAt = time(row.resetsAt)
  const observedAt = time(row.observedAt) ?? 0
  // Without a reset time, a window has certainly reset once a whole window has passed since the reading:
  // otherwise a full meter would block ("Temporarily limited") for ever.
  const windowKey = `${row.source}|${row.window}`
  const length = Object.hasOwn(WINDOW_MS, windowKey) ? WINDOW_MS[windowKey] : undefined
  const lapsed = resetsAt !== null ? resetsAt <= now : length !== undefined && observedAt > 0 && observedAt + length <= now
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

/** A Team or Enterprise organisation is named in an account's identity; a personal one is the person's own. */
function namedKind(kind: string): boolean {
  return kind === 'team' || kind === 'enterprise'
}

/** A Team or Enterprise organisation's identity in keys: its hash, else (older rows) its name. */
function orgIdentity(account: Pick<Account, 'orgId' | 'org'>): string {
  return account.orgId || account.org.trim().toLowerCase()
}

/**
 * No machine, plan, row ID or meter timestamp is part of an account's tracking identity. The organisation's kind
 * (and a Team or Enterprise organisation's hash) is: one email can be a Team seat and a personal plan.
 */
export function accountTrackingKey(account: Account): string {
  const email = account.email.trim().toLowerCase()
  const base = [account.provider, email ? 'email' : 'account', email || account.key]
  if (!email || !account.orgKind) return JSON.stringify(base)
  const org = namedKind(account.orgKind) ? orgIdentity(account) : ''
  return JSON.stringify(org ? [...base, account.orgKind, org] : [...base, account.orgKind])
}

/**
 * Keys a choice may have been saved under before, newest first: a Team or Enterprise organisation by name (before
 * its hash was used), then the email alone (before organisations were reported), or for an account with no email
 * its pre-organisation identity.
 */
function earlierTrackingKeys(account: Account): string[] {
  if (!account.orgKind) return []
  const email = account.email.trim().toLowerCase()
  if (!email) return [JSON.stringify([account.provider, 'account', account.identity])]
  const keys: string[] = []
  const name = account.org.trim().toLowerCase()
  if (namedKind(account.orgKind) && name && account.orgId) keys.push(JSON.stringify([account.provider, 'email', email, account.orgKind, name]))
  keys.push(JSON.stringify([account.provider, 'email', email]))
  return keys
}

/** A saved choice: under today's key, else under an earlier one (earlierTrackingKeys). */
function saved<T>(store: Record<string, T>, account: Account): T | undefined {
  for (const key of [accountTrackingKey(account), ...earlierTrackingKeys(account)]) {
    if (Object.hasOwn(store, key)) return store[key]
  }
  return undefined
}

/** What a screen reader names an account by: its label, and a Team or Enterprise organisation's name. */
export function accountName(account: Account): string {
  const org = namedKind(account.orgKind) && account.org && !account.label.includes(account.org) ? account.org : ''
  return [account.label || account.tool, org].filter(Boolean).join(' · ')
}

/** Whether an account is tracked: the saved choice (see saved), else tracked when it has a weekly reading. */
export function trackedChoice(choices: AccountTracking, account: Account): boolean {
  const choice = saved(choices, account)
  return typeof choice === 'boolean' ? choice : !!weeklyMeter(account)
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

/**
 * Set defaults once. A later lapse, refresh, or temporary disappearance never changes a saved choice. A choice
 * saved under an earlier key (the email alone, before organisations were reported) carries over to each of its
 * organisations' accounts, and stays, for older pages.
 */
export function initializeTracking(accounts: Account[], choices: AccountTracking): AccountTracking {
  let result = choices
  for (const account of accounts) {
    const key = accountTrackingKey(account)
    if (Object.hasOwn(result, key) && typeof result[key] === 'boolean') continue
    if (result === choices) result = { ...choices }
    result[key] = trackedChoice(choices, account)
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

/** The newest row of a list, by reading time. */
function newestRow(rows: LimitRow[]): LimitRow | undefined {
  return rows.reduce<LimitRow | undefined>((best, r) => (!best || (time(r.observedAt) ?? 0) > (time(best.observedAt) ?? 0) ? r : best), undefined)
}

function toAccount(rows: LimitRow[], now: number): Account {
  const head = newestRow(rows) as LimitRow
  // The identity comes from organisation-aware rows when there are any: an older collector's row attached to this
  // account (attachLegacy) does not decide it.
  const aware = rows.filter((r) => r.orgKind)
  const idRow = newestRow(aware) ?? head
  const email = (idRow.label || head.label || '').trim()
  const metered = rows.filter((r) => r.window !== 'plan' && r.window !== 'extra' && r.status !== 'disabled').map((r) => toMeter(r, now))
  const session = newest(metered.filter((m) => m.window === 'session' && !m.scope))
  const week = newest(metered.filter((m) => m.window === 'week' && !m.scope))
  // The newest reading per scoped window: two machines (or an older collector) may report the same one.
  const byScope = new Map<string, Meter>()
  for (const m of metered) {
    if (!m.scope && (m.window === 'session' || m.window === 'week')) continue
    const key = `${m.window}|${m.scope ?? ''}`
    const held = byScope.get(key)
    if (!held || m.observedAt > held.observedAt) byScope.set(key, m)
  }
  const scoped = [...byScope.values()].sort((a, b) => (a.scope || a.window).localeCompare(b.scope || b.window))
  const extraRow = newestRow(rows.filter((r) => r.window === 'extra'))
  // Windows can arrive in any order, including snapshots from before an upgrade/downgrade.
  const plan = planName(newestRow(rows.filter((r) => !!planName(r.plan)))?.plan)
  const org = [...aware, ...rows].map((r) => (r.name || '').trim()).find(Boolean) ?? ''
  const orgKind = idRow.orgKind || ''
  const orgId = aware.map((r) => r.org || '').find(Boolean) ?? ''

  // A full scoped window blocks one model, not the account.
  const blocking = [session, week].filter((m): m is Meter => !!m && m.full)
  const blockedBy = blocking.reduce<Meter | undefined>((last, m) => (!last || (m.resetsAt ?? Infinity) > (last.resetsAt ?? Infinity) ? m : last), undefined)
  const state: AccountState = blockedBy ? 'blocked' : [session, week].some(currentMeter) ? 'ready' : 'unknown'

  return {
    key: groupKey(idRow),
    provider: head.provider,
    tool: TOOL_LABEL[head.source] ?? head.source,
    email,
    plan,
    org,
    orgKind,
    orgId,
    identity: identityOf(idRow),
    label: [email, plan || org].filter(Boolean).join(' · '),
    session,
    week,
    scoped,
    extra: extraRow ? { enabled: extraRow.status !== 'disabled', detail: (extraRow.detail || '').trim() } : undefined,
    observedAt: Math.max(...rows.map((r) => time(r.observedAt) ?? 0)),
    state,
    blockedBy,
  }
}

/** Provider and email (else acct, else source): an account before organisations were reported. */
function identityOf(row: LimitRow): string {
  return `${row.provider}|${(row.label || '').trim().toLowerCase() || row.acct || row.source}`
}

/**
 * An account is a provider and email, and, once the collector reports it, the organisation's kind and a Team or
 * Enterprise organisation's hash: a Team seat and the same person's personal plan are two accounts. Rows from
 * before organisations were reported have no kind and still merge across machines by email.
 */
function groupKey(row: LimitRow): string {
  const kind = row.orgKind || ''
  if (!kind) return identityOf(row)
  const org = namedKind(kind) ? (row.org || (row.name || '').trim().toLowerCase()) : ''
  return `${identityOf(row)}|${kind}${org ? `|${org}` : ''}`
}

interface AwareGroup {
  key: string
  kind: string
  rows: LimitRow[]
  accts: Set<string>
  plans: Set<string>
  names: Set<string>
}

/**
 * Where an older collector's row (no organisation) belongs while machines run mixed versions: the
 * organisation-aware account of the same provider and acct (one login) whose plan it names, and for a Team or
 * Enterprise organisation whose name it names too (an older collector stamped both from the login). It is left out
 * when that account has a reading of the same window as new or newer, or when it is a bare plan row that adds
 * nothing to the login's organisation-aware accounts, or a row without a reading of its own (extra usage, a plan, no
 * reset) that matches none of them. Returns the account key to add it to, '' to leave it out, or
 * undefined: it matches no organisation and keeps a line of its own.
 */
function attachLegacy(row: LimitRow, groups: AwareGroup[]): string | undefined {
  if (!row.acct) return undefined
  const mine = groups.filter((g) => g.rows[0].provider === row.provider && g.accts.has(row.acct))
  if (!mine.length) return undefined
  const plan = planName(row.plan).toLowerCase()
  const name = (row.name || '').trim().toLowerCase()
  if (row.window === 'plan' && !plan && !name) return ''
  const match = mine.find((g) =>
    (plan ? g.plans.has(plan) : !!name && g.names.has(name)) && (!namedKind(g.kind) || (!!name && g.names.has(name))))
  // A row with no reading of its own (extra usage, a plan, anything without a reset) that matches none of the
  // login's organisations is an old collector's leftover: its id is never written again, so it would stay forever.
  if (!match) return row.window === 'plan' || row.window === 'extra' || !row.resetsAt ? '' : undefined
  const at = time(row.observedAt) ?? 0
  const newer = match.rows.some((r) => r.window === row.window && (r.scope || '') === (row.scope || '') && (time(r.observedAt) ?? 0) >= at)
  return newer ? '' : match.key
}

/** One account per provider, email and organisation, in a fixed provider order so cards never reshuffle as states change. */
export function buildAccounts(items: LimitRow[], now: number, planOverrides: AccountPlanOverrides = {}): Account[] {
  const groups = new Map<string, LimitRow[]>()
  const add = (key: string, row: LimitRow) => {
    const list = groups.get(key)
    if (list) list.push(row)
    else groups.set(key, [row])
  }
  for (const item of items) if (item.orgKind) add(groupKey(item), item)
  const aware: AwareGroup[] = [...groups].map(([key, rows]) => ({
    key,
    kind: rows[0].orgKind || '',
    rows,
    accts: new Set(rows.map((r) => r.acct).filter(Boolean)),
    plans: new Set(rows.map((r) => planName(r.plan).toLowerCase()).filter(Boolean)),
    names: new Set(rows.map((r) => (r.name || '').trim().toLowerCase()).filter(Boolean)),
  }))
  const attached: [string, LimitRow][] = []
  for (const item of items) {
    if (item.orgKind) continue
    const to = attachLegacy(item, aware)
    if (to === '') continue
    if (to === undefined) add(groupKey(item), item)
    else attached.push([to, item])
  }
  for (const [key, row] of attached) add(key, row)
  const rank = (p: Provider) => {
    const i = PROVIDER_ORDER.indexOf(p)
    return i < 0 ? PROVIDER_ORDER.length : i
  }
  const accounts = [...groups.values()]
    .map((rows) => {
      const account = toAccount(rows, now)
      const override = saved(planOverrides, account)
      // A later, more specific provider report wins over a clarification of an older label.
      if (override && planName(override.reportedPlan).toLowerCase() === account.plan.toLowerCase()) {
        return { ...account, plan: override.name, label: [account.email, override.name].filter(Boolean).join(' · ') }
      }
      return account
    })
  // An older collector's line beside the same email's organisation-aware ones says which plan it is, or that its
  // organisation is unknown, so no two lines read the same.
  const withOrgs = new Set(accounts.filter((a) => a.orgKind && a.email).map((a) => `${a.provider}|${a.email.toLowerCase()}`))
  for (const account of accounts) {
    if (account.orgKind || !account.email || !withOrgs.has(`${account.provider}|${account.email.toLowerCase()}`)) continue
    account.label = [account.email, account.plan || account.org || 'organisation unknown'].join(' · ')
    if (accounts.some((a) => a !== account && a.provider === account.provider && a.label === account.label)) account.label += ' · organisation unknown'
  }
  return accounts.sort((a, b) => rank(a.provider) - rank(b.provider) || a.email.localeCompare(b.email) || a.tool.localeCompare(b.tool) ||
    a.label.localeCompare(b.label) || a.orgKind.localeCompare(b.orgKind) || a.key.localeCompare(b.key))
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

/**
 * Synthetic accounts for ?mock=1, relative to now: one blocked on its session, one on its week, one ready, one stale,
 * and one email that is both a Team seat and a personal Max plan (with a row from before organisations were reported).
 */
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
    row({ id: 'm12', provider: 'anthropic', source: 'claude-code', window: 'session', acct: 'a_00000000000000d1', label: 'both@example.dev', name: 'Example Team', plan: 'Team', org: 'a_00000000000000e1', orgKind: 'team', usedPercent: 31, resetsAt: at(3 * H + 40 * MIN) }),
    row({ id: 'm13', provider: 'anthropic', source: 'claude-code', window: 'week', acct: 'a_00000000000000d1', label: 'both@example.dev', name: 'Example Team', plan: 'Team', org: 'a_00000000000000e1', orgKind: 'team', usedPercent: 58, resetsAt: at(2 * D + 7 * H) }),
    row({ id: 'm14', provider: 'anthropic', source: 'claude-code', window: 'session', acct: 'a_00000000000000d1', label: 'both@example.dev', name: 'Sam Example', plan: 'Max (20x)', org: 'a_00000000000000e2', orgKind: 'personal', usedPercent: 12, resetsAt: at(1 * H + 5 * MIN) }),
    row({ id: 'm15', provider: 'anthropic', source: 'claude-code', window: 'week', acct: 'a_00000000000000d1', label: 'both@example.dev', name: 'Sam Example', plan: 'Max (20x)', org: 'a_00000000000000e2', orgKind: 'personal', usedPercent: 23, resetsAt: at(5 * D + 2 * H) }),
    row({ id: 'm17', provider: 'anthropic', source: 'claude-code', window: 'plan', acct: 'a_00000000000000d1', label: 'both@example.dev', name: 'Sam Example', plan: 'Max (20x)', org: 'a_00000000000000e2', orgKind: 'personal', status: undefined }),
    // The Team seat's week written by an older collector before organisations were reported: the Team account
    // has a newer reading of it, so it is not shown.
    row({ id: 'm16', provider: 'anthropic', source: 'claude-code', window: 'week', acct: 'a_00000000000000d1', label: 'both@example.dev', name: 'Example Team', plan: 'Team', usedPercent: 97, resetsAt: at(1 * D), observedAt: at(-2 * D) }),
  ]
}
