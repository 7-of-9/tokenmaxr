// Preload the selected range for each account, retaining separate cached ranges.
import { useEffect, useMemo, useRef, useState } from 'react'
import { LimitsDenied, type Account } from './limits'
import { DEFAULT_PROJECT_RANGE, isProjectRange, mockProjectUsage, type ProjectRange, type ProjectUsageResponse } from './projectUsage'
import { useUsageSource } from './source'
import { LOCAL_PREFS, useTokensSite, type SitePrefs } from './site'

/** Project usage is read for every account as soon as the meters arrive, so opening a panel is instant. */
const PROJECTS_REFRESH_MS = 180_000
/** Requests in flight at once: each one reads a week of the owner's events, and the API's time budget is per request
 * on a shared host, so too many at once make the first ones time out (owner report 2026-11-02). */
const PARALLEL = 2
/** A failed read is retried this many times, with these pauses, before the panel says anything. */
const RETRY_AFTER_MS = [1500, 4000, 8000]

export type ProjectLoad =
  | { status: 'loading' }
  | { status: 'ok'; data: ProjectUsageResponse; at: number }
  | { status: 'error'; message: string }

/** Preloaded project usage, isolated by account, range and owner. */
export interface ProjectUsageStore {
  /** False when this data source has no project usage: the panels are not offered. */
  supported: boolean
  /** The selected reading, or undefined while nothing has been read. */
  get(accountKey: string): ProjectLoad | undefined
  range(accountKey: string): ProjectRange
  select(accountKey: string, range: ProjectRange): void
  /** Read this account's selected range again now (after an error). */
  retry(accountKey: string): void
}

export interface ProjectExpansionProps { mock: boolean; usage: ProjectUsageStore }

interface Target { accountKey: string; meterId: string; range: ProjectRange; resetsAt: number; cacheKey: string }
const RANGE_STORAGE = 'd0m1.tokens.projects.ranges'

function savedRanges(prefs: SitePrefs): Record<string, ProjectRange> {
  try {
    const parsed: unknown = JSON.parse(prefs.get(RANGE_STORAGE) || '{}')
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return Object.fromEntries(Object.entries(parsed).filter(([, value]) => isProjectRange(value)))
  } catch { return {} }
}

/**
 * Preloads every account's project usage (owner direction 2026-11-02: "preload that project-usage data for all
 * accounts so the ux is slick"). Reads run a few at a time, keep the last good table on screen while they refresh,
 * start over with a new owner, source or Refresh, and repeat every few minutes.
 */
export function useProjectUsage(accounts: Account[], mock: boolean, reloadKey: number, authKey: string): ProjectUsageStore {
  const source = useUsageSource()
  const site = useTokensSite()
  const prefs = site.prefs ?? LOCAL_PREFS
  const onDenied = useRef(site.onDenied)
  onDenied.current = site.onDenied
  const fetchProjects = source.fetchLimitProjects
  const supported = mock || !!fetchProjects
  const [ranges, setRanges] = useState(() => savedRanges(prefs))
  useEffect(() => {
    setRanges(savedRanges(prefs))
    return prefs.subscribe(RANGE_STORAGE, () => setRanges(savedRanges(prefs)))
  }, [prefs, authKey])
  // Accounts are rebuilt every second for the countdowns; the effect keys on the meters themselves.
  const targetsKey = useMemo(() => {
    const targets: Target[] = []
    for (const account of accounts) {
      const meterId = account.projectMeterId ?? account.week?.key
      if (!meterId) continue
      const range = Object.hasOwn(ranges, account.key) ? ranges[account.key] : DEFAULT_PROJECT_RANGE
      const resetsAt = account.week?.resetsAt ?? 0
      targets.push({ accountKey: account.key, meterId, range, resetsAt,
        cacheKey: JSON.stringify([meterId, range, range === 'reset' ? resetsAt : 0]) })
    }
    return JSON.stringify(targets)
  }, [accounts, ranges])
  const epoch = JSON.stringify([source.cacheKey, authKey, mock, reloadKey])
  const [loads, setLoads] = useState<{ epoch: string; byMeter: Record<string, ProjectLoad> }>({ epoch, byMeter: {} })
  const readOne = useRef<(accountKey: string) => void>(() => {})
  // Completion times by query: reuse fresh ranges, refetch older ones on selection.
  const done = useRef<{ epoch: string; byMeter: Map<string, number> }>({ epoch, byMeter: new Map() })

  useEffect(() => {
    if (!supported) return
    const targets: Target[] = JSON.parse(targetsKey)
    let cancelled = false
    const inflight = new Map<string, AbortController>()
    const update = (cacheKey: string, load: ProjectLoad) => setLoads(previous => ({
      epoch, byMeter: { ...(previous.epoch === epoch ? previous.byMeter : {}), [cacheKey]: load },
    }))
    if (done.current.epoch !== epoch) done.current = { epoch, byMeter: new Map() }
    const read = async ({ meterId, resetsAt, range, cacheKey }: Target) => {
      if (mock) {
        done.current.byMeter.set(cacheKey, Date.now())
        update(cacheKey, { status: 'ok', data: mockProjectUsage(resetsAt, Date.now(), range), at: Date.now() })
        return
      }
      if (!fetchProjects || inflight.has(cacheKey)) return
      const controller = new AbortController()
      inflight.set(cacheKey, controller)
      try {
        for (let attempt = 0; ; attempt++) {
          try {
            const data = await fetchProjects(meterId, range, AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)]))
            if (cancelled) return
            done.current.byMeter.set(cacheKey, Date.now())
            update(cacheKey, { status: 'ok', data, at: Date.now() })
            return
          } catch (error: unknown) {
            if (cancelled) return
            if (error instanceof LimitsDenied) {
              onDenied.current?.(error.status)
              throw error
            }
            // The server's first answer under load is often a timeout that a retry a moment later gets through;
            // the owner never sees that. A table already on screen stays while retries run.
            if (attempt >= RETRY_AFTER_MS.length) throw error
            await new Promise<void>(resolve => {
              const timer = window.setTimeout(resolve, RETRY_AFTER_MS[attempt])
              controller.signal.addEventListener('abort', () => { window.clearTimeout(timer); resolve() }, { once: true })
            })
            if (cancelled || controller.signal.aborted) return
          }
        }
      } catch (error: unknown) {
        if (cancelled) return
        update(cacheKey, { status: 'error', message: error instanceof Error && error.name === 'TimeoutError'
          ? 'Reading project usage timed out. Try again.' : error instanceof Error ? error.message : 'Could not read project usage.' })
      } finally {
        inflight.delete(cacheKey)
      }
    }
    const readAll = (only: (target: Target) => boolean = () => true) => {
      const queue = targets.filter(only)
      const workers = Array.from({ length: Math.min(PARALLEL, queue.length) }, async () => {
        while (queue.length && !cancelled) await read(queue.shift()!)
      })
      return Promise.all(workers)
    }
    readOne.current = (accountKey) => {
      const target = targets.find(t => t.accountKey === accountKey)
      if (!target) return
      update(target.cacheKey, { status: 'loading' })
      void read(target)
    }
    void readAll(target => Date.now() - (done.current.byMeter.get(target.cacheKey) ?? 0) >= PROJECTS_REFRESH_MS)
    const timer = window.setInterval(() => { void readAll() }, PROJECTS_REFRESH_MS)
    return () => {
      cancelled = true
      readOne.current = () => {}
      window.clearInterval(timer)
      for (const controller of inflight.values()) controller.abort()
      inflight.clear()
    }
  }, [supported, mock, fetchProjects, targetsKey, epoch])

  return useMemo(() => {
    const targets: Target[] = JSON.parse(targetsKey)
    return {
      supported,
      get: (accountKey: string) => {
        const target = targets.find(t => t.accountKey === accountKey)
        return target && loads.epoch === epoch ? loads.byMeter[target.cacheKey] : undefined
      },
      range: (accountKey: string) => Object.hasOwn(ranges, accountKey) ? ranges[accountKey] : DEFAULT_PROJECT_RANGE,
      select: (accountKey: string, range: ProjectRange) => {
        if (!isProjectRange(range)) return
        const next = { ...ranges, [accountKey]: range }
        setRanges(next)
        try { prefs.set(RANGE_STORAGE, JSON.stringify(next)) } catch { /* Storage may be blocked. */ }
      },
      retry: (accountKey: string) => readOne.current(accountKey),
    }
  }, [supported, loads, epoch, targetsKey, ranges, prefs])
}

