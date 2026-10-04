import { useEffect, useRef, useState } from 'react'
import type { UsageResponse } from './types'
import { buildMockUsage } from './mock'
import { useUsageSource } from './source'
import type { UsageTransfer } from './usageResponse'
import { readUsageCache, writeUsageCache } from './usageCache'

// Healthy responses take a few seconds. Failed upstream requests can hang for over 30 s.
const REQUEST_TIMEOUT_MS = 12_000

export interface UsageState {
  data: UsageResponse | null
  error: string | null
  loading: boolean
  progress: (UsageTransfer & { startedAt: number }) | null
  failures: number
  /** Client clock at the last successful load, for "live" checks. */
  loadedAt: number
}

function initialState(mock: boolean, source: string): UsageState {
  let cached = null
  try { if (!mock) cached = readUsageCache(window.sessionStorage, source) } catch { /* Browser storage may be disabled. */ }
  return { data: cached?.data ?? null, error: null, loading: true, loadedAt: cached?.loadedAt ?? 0, progress: null, failures: 0 }
}

/** Polls the context's source (every 15 s for the d0m1 API) while visible; returning to the tab refreshes immediately. */
export function useUsage(mock: boolean, reloadKey: number): UsageState {
  const usageSource = useUsageSource()
  const source = usageSource.cacheKey
  const [state, setState] = useState<UsageState>(() => initialState(mock, source))
  const sourceKey = mock ? 'mock' : source
  const previousSource = useRef(sourceKey)

  useEffect(() => {
    if (previousSource.current !== sourceKey) {
      previousSource.current = sourceKey
      setState(initialState(mock, source))
    }
    if (mock) {
      const now = new Date()
      setState({ data: buildMockUsage(now), error: null, loading: false, loadedAt: now.getTime(), progress: null, failures: 0 })
      return
    }

    let cancelled = false
    let timer: number | undefined
    let controller: AbortController | null = null
    let failures = 0

    const load = async () => {
      controller?.abort()
      const request = new AbortController()
      controller = request
      const timeout = window.setTimeout(() => request.abort(new DOMException('The usage API timed out. Retrying shortly.', 'TimeoutError')), REQUEST_TIMEOUT_MS)
      const startedAt = Date.now()
      setState((prev) => ({ ...prev, loading: true, error: prev.failures >= 3 ? prev.error : null,
        progress: { startedAt, phase: 'waiting', received: 0, total: null } }))
      try {
        const data = await usageSource.fetchUsage(request.signal, progress => {
          if (!cancelled && controller === request) setState(prev => prev.data ? prev : { ...prev, progress: { ...progress, startedAt } })
        })
        if (!cancelled && controller === request) {
          failures = 0
          const loadedAt = Date.now()
          try { writeUsageCache(window.sessionStorage, source, { data, loadedAt }) } catch { /* Keep live data without browser storage. */ }
          setState({ data, error: null, loading: false, loadedAt, progress: null, failures: 0 })
        }
      } catch (error) {
        if (cancelled || controller !== request || (error instanceof DOMException && error.name === 'AbortError')) return false
        const message = error instanceof Error ? error.message : 'Could not load usage.'
        failures++
        setState((prev) => ({ ...prev, error: message, loading: false, progress: null, failures }))
      } finally {
        window.clearTimeout(timeout)
      }
      return !cancelled && controller === request
    }

    const schedule = (delay: number) => {
      window.clearTimeout(timer)
      timer = window.setTimeout(tick, delay)
    }

    const tick = async () => {
      if (document.hidden) return
      const pollMs = usageSource.pollMs
      if (await load() && !document.hidden) schedule(failures ? Math.min(1000 * 2 ** (failures - 1), pollMs) : pollMs)
    }

    const onVisibility = () => {
      if (document.hidden) {
        window.clearTimeout(timer)
        return
      }
      schedule(0)
    }

    document.addEventListener('visibilitychange', onVisibility)
    if (document.hidden) {
      // Still show something when opened in a background tab.
      void load()
    } else {
      void tick()
    }

    return () => {
      cancelled = true
      window.clearTimeout(timer)
      controller?.abort()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [mock, reloadKey, source, sourceKey, usageSource])

  return state
}
