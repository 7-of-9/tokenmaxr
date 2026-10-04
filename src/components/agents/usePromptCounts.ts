import { useEffect, useRef, useState } from 'react'
import type { PromptCountsResponse } from './promptCounts'
import { useUsageSource } from './source'

const POLL_MS = 60_000
const TIMEOUT_MS = 10_000

export interface PromptCountsState {
  data: PromptCountsResponse | null
  error: string | null
  loading: boolean
}

/** Public, optional enrichment: a failure never blocks or clears token usage. */
export function usePromptCounts(mock: boolean, reloadKey: number): PromptCountsState {
  const countsSource = useUsageSource()
  const source = mock ? 'mock' : countsSource.cacheKey
  const previousSource = useRef(source)
  const [state, setState] = useState<PromptCountsState>({ data: null, error: null, loading: !mock })

  useEffect(() => {
    if (previousSource.current !== source) {
      previousSource.current = source
      setState({ data: null, error: null, loading: !mock })
    }
    if (mock) {
      setState({ data: null, error: null, loading: false })
      return
    }
    let cancelled = false
    let timer: number | undefined
    let controller: AbortController | null = null
    let inFlight = false
    let retryAfter = 0

    const schedule = (delay: number) => {
      window.clearTimeout(timer)
      if (!cancelled && !document.hidden) timer = window.setTimeout(() => { void load() }, delay)
    }
    const load = async () => {
      if (cancelled || inFlight || document.hidden) return
      const delay = retryAfter - Date.now()
      if (delay > 0) { schedule(delay); return }
      inFlight = true
      const request = new AbortController()
      controller = request
      let timedOut = false
      const timeout = window.setTimeout(() => { timedOut = true; request.abort() }, TIMEOUT_MS)
      setState(previous => ({ ...previous, loading: true }))
      try {
        const data = await countsSource.fetchPromptCounts(request.signal)
        if (request.signal.aborted) throw new Error('Prompt model counts timed out.')
        if (!cancelled && controller === request) {
          retryAfter = 0
          setState({ data, error: null, loading: false })
        }
      } catch {
        if (!cancelled && controller === request) {
          // A new endpoint may not be deployed yet. Focus events must not turn
          // a 404 or upstream failure into repeated immediate retries.
          retryAfter = Date.now() + POLL_MS
          setState(previous => ({ ...previous, loading: false, error: timedOut
            ? 'Prompt model counts timed out. Retrying shortly.'
            : 'Prompt model counts are temporarily unavailable.' }))
        }
      } finally {
        window.clearTimeout(timeout)
        if (controller === request) {
          inFlight = false
          schedule(Math.max(POLL_MS, retryAfter - Date.now()))
        }
      }
    }
    const onVisible = () => {
      if (document.hidden) window.clearTimeout(timer)
      else schedule(Math.max(0, retryAfter - Date.now()))
    }
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('focus', onVisible)
    void load()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
      controller?.abort()
      document.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('focus', onVisible)
    }
  }, [mock, reloadKey, source, countsSource])

  // Do not let the previous API's enrichment appear for a newly selected source.
  return previousSource.current === source ? state : { data: null, error: null, loading: !mock }
}
