/** Private recorded usage in an account's current weekly window. */
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
  resetsAt: string
  projected: boolean
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

export function formatProjectShare(percent: number): string {
  return percent > 0 && percent < 0.01 ? '<0.01%' : `${percent.toLocaleString('en-GB', { maximumFractionDigits: 2 })}%`
}

/** Synthetic layout data only; never fetched or shown outside ?mock=1. */
export function mockProjectUsage(resetsAt: number, now = Date.now()): ProjectUsage {
  const projects = [
    { key: 'research', name: 'family-research', path: '/example/projects/family-research', tokens: 254_800_000, exactTokens: 254_800_000, estimatedTokens: 0, calls: 840, percent: 91 },
    { key: 'website', name: 'personal-site', path: '/example/projects/personal-site', tokens: 22_400_000, exactTokens: 22_400_000, estimatedTokens: 0, calls: 120, percent: 8 },
    { key: 'unassigned', name: 'Unassigned project', path: '', tokens: 2_800_000, exactTokens: 2_800_000, estimatedTokens: 0, calls: 10, percent: 1 },
  ]
  return {
    available: true, generatedAt: new Date(now).toISOString(), since: new Date(resetsAt - 7 * 86_400_000).toISOString(),
    until: new Date(now).toISOString(), resetsAt: new Date(resetsAt).toISOString(), projected: false,
    totals: { tokens: 280_000_000, exactTokens: 280_000_000, estimatedTokens: 0, calls: 970, uncertainTokens: 0 },
    projects, undecryptablePrompts: 0,
  }
}
