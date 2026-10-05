// Where the /tokens and /tokens/agents pages get their data. d0m1.com reads its own agents API (the default
// below); the tokenmaxr GitHub Pages dashboard reads the static files collectors publish
// (pages/dashboard/githubSource.ts). Both produce the same UsageResponse and limit rows, so the pages never know
// which one they are showing.
import { createContext, useContext } from 'react'
import { LimitsDenied, type LimitRow } from './limits'
import { readPromptCounts, type PromptCountsResponse } from './promptCounts'
import type { UsageResponse } from './types'
import { readUsageResponse, type UsageTransfer } from './usageResponse'

export interface UsageSource {
  /** Identifies the data: the session cache is keyed by it, and a change starts the page over. */
  cacheKey: string
  /** The footer's "where this came from". */
  label: string
  /** Completes "Waiting for the …" while nothing has loaded. */
  loadingLabel: string
  /** In place of the graph when nothing has loaded and nothing is loading (default: waiting for the API). */
  unavailableText?: string
  /** "Counted by tokenmaxr": a site path (routed) or an absolute URL. */
  collectorHref: string
  /** The same usage's public GitHub Pages dashboard, linked from the footer (opens in a new tab). */
  publicHref?: string
  /** Usage refresh interval while the tab is visible. */
  pollMs: number
  /** The whole history. Aborting `signal` (with a reason) must reject with that reason. */
  fetchUsage(signal: AbortSignal, progress: (transfer: UsageTransfer) => void): Promise<UsageResponse>
  /** Per-model prompt counts, or null when the source has none beyond what fetchUsage returned. */
  fetchPromptCounts(signal: AbortSignal): Promise<PromptCountsResponse | null>
  /**
   * The same response for one machine, built exactly from that machine's own records, or null when this
   * source cannot (the page then splits the fleet days itself, see machines.ts).
   */
  scopeMachine?(data: UsageResponse, machine: string): UsageResponse | null
  /**
   * The owner's plan limits, as GET /api/limits serves them (the Agents page). Rejects with LimitsDenied when the
   * viewer is not the owner; absent: this source has none.
   */
  fetchLimits?(signal: AbortSignal): Promise<LimitRow[]>
}

/** The owner's public tokenmaxr dashboard on GitHub Pages: the same usage, published by the same collectors. */
const D0M1_PUBLIC_DASHBOARD = 'https://7-of-9.github.io/tokenmaxr-usage/'

/** d0m1.com's agents API. Requests stay relative: in development Vite proxies /api to `origin`. */
export function apiSource(origin: string): UsageSource {
  const host = new URL(origin).hostname
  const environment = host === 'd0m1.com' || host === 'www.d0m1.com'
    ? 'production'
    : ['localhost', '127.0.0.1', '[::1]'].includes(host) ? 'local' : 'remote'
  return {
    cacheKey: origin,
    label: `API: ${origin}/api (${environment})`,
    loadingLabel: `${environment} API`,
    collectorHref: '/collector',
    publicHref: D0M1_PUBLIC_DASHBOARD,
    pollMs: 15_000,
    async fetchUsage(signal, progress) {
      const response = await fetch('/api/usage?days=all', {
        cache: 'no-store',
        signal,
        headers: { Accept: 'application/json' },
      })
      if (!response.ok) throw new Error([502, 503, 504].includes(response.status)
        ? 'The usage API is temporarily unavailable. Retrying shortly.' : `The usage API answered ${response.status}.`)
      const type = response.headers.get('content-type') ?? ''
      if (!type.includes('json')) throw new Error('The usage API is not reachable from here.')
      return readUsageResponse(response, progress)
    },
    async fetchPromptCounts(signal) {
      const response = await fetch('/api/prompt-counts', {
        credentials: 'omit', cache: 'no-store', signal,
        headers: { Accept: 'application/json' },
      })
      if (!response.ok) throw new Error(response.status === 404
        ? 'Prompt model counts are not available yet.' : 'Prompt model counts are temporarily unavailable.')
      if (!(response.headers.get('content-type') ?? '').includes('json')) throw new Error('Prompt model counts are not available here.')
      return readPromptCounts(await response.json())
    },
    async fetchLimits(signal) {
      const response = await fetch('/api/limits', { headers: { Accept: 'application/json' }, signal })
      if (response.status === 401 || response.status === 403) throw new LimitsDenied(response.status)
      if (!response.ok) throw new Error(`The limits API answered ${response.status}.`)
      const body = (await response.json()) as { items?: LimitRow[] }
      return Array.isArray(body.items) ? body.items : []
    },
  }
}

let defaultSource: UsageSource | null = null

/** The d0m1 API at the build's configured origin, or this site's own. */
function d0m1Source() {
  return (defaultSource ??= apiSource(__AGENTS_API_ORIGIN__ || window.location.origin))
}

/** Null means the d0m1 API: d0m1.com never provides one, so its pages behave exactly as before the seam. */
export const UsageSourceContext = createContext<UsageSource | null>(null)

export function useUsageSource(): UsageSource {
  return useContext(UsageSourceContext) ?? d0m1Source()
}
