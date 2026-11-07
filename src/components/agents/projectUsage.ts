export const PROJECT_RANGES = [
  ['reset', 'Since weekly reset'],
  ['30m', 'Last 30 minutes'], ['1h', 'Last 1 hour'], ['3h', 'Last 3 hours'],
  ['6h', 'Last 6 hours'], ['12h', 'Last 12 hours'], ['24h', 'Last 24 hours'],
  ['3d', 'Last 3 days'], ['7d', 'Last 7 days'],
] as const
export type ProjectRange = typeof PROJECT_RANGES[number][0]
export const DEFAULT_PROJECT_RANGE: ProjectRange = 'reset'
export function isProjectRange(value: unknown): value is ProjectRange {
  return PROJECT_RANGES.some(([range]) => range === value)
}

/** Private recorded usage in an account's selected time window. */
export interface ProjectUsageRow {
  key: string
  name: string
  path: string
  tokens: number
  exactTokens: number
  estimatedTokens: number
  calls: number
  /** Share of recorded account tokens, not share of subscription quota. */
  percent: number
}

export interface ProjectUsage {
  available: true
  generatedAt: string
  since: string
  until: string
  range: ProjectRange
  resetsAt: string | null
  projected: boolean
  timingLimited: boolean
  totals: { tokens: number; exactTokens: number; estimatedTokens: number; calls: number; uncertainTokens: number }
  projects: ProjectUsageRow[]
  undecryptablePrompts: number
}

export type ProjectUsageResponse = ProjectUsage | {
  available: false
  reason: 'reset-unavailable' | 'account-unavailable' | 'organisation-unavailable'
  generatedAt: string
  until: string
  projects: []
}

/** Every period of one account, from one request. */
export type ProjectUsageSet = Record<ProjectRange, ProjectUsageResponse>

export function formatProjectShare(percent: number): string {
  return percent > 0 && percent < 0.01 ? '<0.01%' : `${percent.toLocaleString('en-GB', { maximumFractionDigits: 2 })}%`
}

/** Synthetic layout data only; never fetched or shown outside ?mock=1. */
export function mockProjectUsage(resetsAt: number, now = Date.now(), range: ProjectRange = DEFAULT_PROJECT_RANGE): ProjectUsage {
  const hours = { '30m': 0.5, '1h': 1, '3h': 3, '6h': 6, '12h': 12, '24h': 24, '3d': 72, '7d': 168 }
  const since = range === 'reset' ? resetsAt - 7 * 86_400_000 : now - hours[range] * 3_600_000
  const projects = [
    { key: 'research', name: 'family-research', path: '/example/projects/family-research', tokens: 254_800_000, exactTokens: 254_800_000, estimatedTokens: 0, calls: 840, percent: 91 },
    { key: 'website', name: 'personal-site', path: '/example/projects/personal-site', tokens: 22_400_000, exactTokens: 22_400_000, estimatedTokens: 0, calls: 120, percent: 8 },
    { key: 'unassigned', name: 'Unassigned project', path: '', tokens: 2_800_000, exactTokens: 2_800_000, estimatedTokens: 0, calls: 10, percent: 1 },
  ]
  return {
    available: true, generatedAt: new Date(now).toISOString(), since: new Date(since).toISOString(), range,
    until: new Date(now).toISOString(), resetsAt: range === 'reset' ? new Date(resetsAt).toISOString() : null, projected: false, timingLimited: false,
    totals: { tokens: 280_000_000, exactTokens: 280_000_000, estimatedTokens: 0, calls: 970, uncertainTokens: 0 },
    projects, undecryptablePrompts: 0,
  }
}

export function mockProjectUsageSet(resetsAt: number, now = Date.now()): ProjectUsageSet {
  return Object.fromEntries(PROJECT_RANGES.map(([range]) => [range, mockProjectUsage(resetsAt, now, range)])) as ProjectUsageSet
}

const CACHE_STORAGE = 'd0m1.tokens.projects.cache'
const CACHE_MAX_AGE_MS = 7 * 86_400_000
export interface CachedProjectUsage { data: ProjectUsageSet; at: number }

/** The last good readings, so a reload paints at once and refreshes behind it. Only the owner's own browser holds this. */
export function readProjectCache(owner: string, now = Date.now()): Record<string, CachedProjectUsage> {
  try {
    const parsed = JSON.parse(localStorage.getItem(CACHE_STORAGE) || 'null') as { owner?: string; accounts?: Record<string, CachedProjectUsage> } | null
    if (parsed?.owner !== owner || !parsed.accounts) return {}
    return Object.fromEntries(Object.entries(parsed.accounts).filter(([, entry]) => now - entry.at < CACHE_MAX_AGE_MS &&
      PROJECT_RANGES.every(([range]) => Array.isArray(entry.data?.[range]?.projects))))
  } catch { return {} }
}

/** Project names only: the full paths stay out of browser storage. */
export function writeProjectCache(owner: string, accountKey: string, data: ProjectUsageSet, at: number) {
  try {
    const stripped = Object.fromEntries(PROJECT_RANGES.map(([range]) => [range, { ...data[range],
      projects: data[range].projects.map(project => ({ ...project, path: '' })) }])) as ProjectUsageSet
    const accounts = { ...readProjectCache(owner, at), [accountKey]: { data: stripped, at } }
    localStorage.setItem(CACHE_STORAGE, JSON.stringify({ owner, accounts }))
  } catch { /* Storage may be blocked or full: the page simply reads again next time. */ }
}

export function clearProjectCache() {
  try { localStorage.removeItem(CACHE_STORAGE) } catch { /* Storage may be blocked. */ }
}
