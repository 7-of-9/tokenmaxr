// The quota card's meters, from each machine's quota.json (the plan windows the tools last wrote). Pure, so
// quota.test.ts runs it under plain Node. Account hashes are never shown: when one tool has several
// accounts, they read as per-page aliases.
import { ago, countdown, severityColor, TOOL_LABEL } from '../../src/components/agents/limits.ts'
import type { PublishedMeter } from './githubSource.ts'

/** A meter nobody read for a week says nothing about now. */
export const METER_MAX_AGE_MS = 7 * 86_400_000
/** Readings older than this are flagged. */
export const METER_STALE_MS = 6 * 3_600_000

export interface MeterView {
  key: string
  /** "Claude · week · Fable" */
  name: string
  plan: string
  /** acct1..N, only when the tool has more than one account on this page. */
  account: string | null
  /** Null when unknown, or when the window has reset since the reading. */
  usedPercent: number | null
  color: string
  /** The window has reset since it was read: what is used now is unknown. */
  resetSinceRead: boolean
  /** "resets in 2d 04h", or '' when the reset time is unknown. */
  resets: string
  /** "read 4 min ago" */
  read: string
  stale: boolean
}

/**
 * One meter per provider, tool, account, window and scope: the newest reading wins when several machines
 * share an account. Readings older than a week are dropped. `machine` keeps one machine's readings.
 */
export function quotaMeters(meters: PublishedMeter[], now: number, machine = 'all'): MeterView[] {
  const newest = new Map<string, PublishedMeter>()
  for (const m of meters) {
    const observed = Date.parse(m.observedAt)
    if (!Number.isFinite(observed) || now - observed > METER_MAX_AGE_MS) continue
    if (machine !== 'all' && m.machine !== machine) continue
    const key = [m.provider, m.source, m.acct ?? '', m.window, m.scope ?? ''].join('|')
    const current = newest.get(key)
    if (!current || observed > Date.parse(current.observedAt)) newest.set(key, m)
  }
  const kept = [...newest.values()]
  // Aliases within each tool, in hash order; a tool with one account needs none.
  const accounts = new Map<string, string[]>()
  for (const m of kept) {
    const tool = `${m.provider}|${m.source}`
    const list = accounts.get(tool) ?? []
    if (m.acct && !list.includes(m.acct)) list.push(m.acct)
    accounts.set(tool, list)
  }
  for (const list of accounts.values()) list.sort()
  return kept.map((m): MeterView => {
    const resetsAt = m.resetsAt ? Date.parse(m.resetsAt) : NaN
    const resetSinceRead = Number.isFinite(resetsAt) && resetsAt <= now
    const used = typeof m.usedPercent === 'number' && Number.isFinite(m.usedPercent) ? Math.min(100, Math.max(0, m.usedPercent)) : null
    const usedPercent = resetSinceRead ? null : used
    const tool = accounts.get(`${m.provider}|${m.source}`) ?? []
    const alias = m.acct ? `acct${tool.indexOf(m.acct) + 1}` : ''
    return {
      // Unique per meter, without the hash.
      key: [m.provider, m.source, alias, m.window, m.scope ?? ''].join('|'),
      name: [TOOL_LABEL[m.source] ?? (m.source || m.provider), m.window, m.scope].filter(Boolean).join(' · '),
      plan: m.plan ?? '',
      account: tool.length > 1 ? alias || null : null,
      usedPercent,
      color: usedPercent === null ? 'var(--gh-muted)' : severityColor(usedPercent / 100),
      resetSinceRead,
      resets: resetSinceRead || !Number.isFinite(resetsAt) ? '' : `resets in ${countdown(resetsAt - now)}`,
      read: `read ${ago(Date.parse(m.observedAt), now)}`,
      stale: now - Date.parse(m.observedAt) > METER_STALE_MS,
    }
  }).sort((a, b) => (b.usedPercent ?? -1) - (a.usedPercent ?? -1) || a.name.localeCompare(b.name))
}
