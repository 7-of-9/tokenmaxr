// Preload every period of every account, so expanding a row or choosing a period never touches the network.
import { useEffect, useMemo, useRef, useState } from 'react'
import { LimitsDenied, type Account } from './limits'
import { clearProjectCache, DEFAULT_PROJECT_RANGE, isProjectRange, mockProjectUsageSet, readProjectCache, writeProjectCache, type ProjectRange, type ProjectUsageResponse, type ProjectUsageSet } from './projectUsage'
import { useUsageSource } from './source'
import { LOCAL_PREFS, useTokensSite, type SitePrefs } from './site'

const PROJECTS_REFRESH_MS = 180_000
/** Requests in flight at once: each one reads a week of the owner's events, and the API's time budget is per request
 * on a shared host, so too many at once make the first ones time out (owner report 2026-11-02). */
const PARALLEL = 2
/** A failed read is retried this many times, with these pauses, before the panel says anything. */
const RETRY_AFTER_MS = [1500, 4000, 8000]

export type ProjectLoad =
  | { status: 'loading' }
  /** stale: painted from the browser's copy while the first read of this visit is still running. */
  | { status: 'ok'; data: ProjectUsageResponse; at: number; stale?: boolean }
  | { status: 'error'; message: string }

type SetLoad =
  | { status: 'loading' }
  | { status: 'ok'; data: ProjectUsageSet; at: number }
  | { status: 'error'; message: string }

/** Preloaded project usage, isolated by account and owner. */
export interface ProjectUsageStore {
  /** False when this data source has no project usage: the panels are not offered. */
  supported: boolean
  /** The selected period's reading, or undefined while nothing has been read. */
  get(accountKey: string): ProjectLoad | undefined
  range(accountKey: string): ProjectRange
  select(accountKey: string, range: ProjectRange): void
  /** Read this account again now (after an error). */
  retry(accountKey: string): void
}

export interface ProjectExpansionProps { mock: boolean; usage: ProjectUsageStore }

/** resetMinute is rounded: readings of one reset differ by milliseconds, and each must not read again. */
interface Target { accountKey: string; meterId: string; resetsAt: number; resetMinute: number }
const RANGE_STORAGE = 'd0m1.tokens.projects.ranges'

function savedRanges(prefs: SitePrefs): Record<string, ProjectRange> {
  try {
    const parsed: unknown = JSON.parse(prefs.get(RANGE_STORAGE) || '{}')
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return Object.fromEntries(Object.entries(parsed).filter(([, value]) => isProjectRange(value)))
  } catch { return {} }
}

/**
 * Reads every account's periods as soon as the meters arrive (owner direction 2026-11-07: expanding or choosing a
 * period must be instant, with no server round trip). The last good readings are kept in the browser, so even a reload
 * paints at once. Reads run a few at a time behind whatever is on screen and repeat every few minutes; a failed
 * refresh never replaces a good table. A new owner or source starts over; Refresh only reads again.
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
      const resetsAt = account.week?.resetsAt ?? 0
      targets.push({ accountKey: account.key, meterId, resetsAt, resetMinute: Math.round(resetsAt / 60_000) })
    }
    return JSON.stringify(targets)
  }, [accounts])
  const epoch = JSON.stringify([source.cacheKey, authKey, mock])
  const [loads, setLoads] = useState<{ epoch: string; byAccount: Record<string, SetLoad> }>({ epoch, byAccount: {} })
  const cached = useMemo(() => (mock ? {} : readProjectCache(authKey)), [authKey, mock])
  useEffect(() => { if (authKey === 'anon') clearProjectCache() }, [authKey])
  const lastReload = useRef(reloadKey)
  const readOne = useRef<(accountKey: string) => void>(() => {})
  // Completion times, so a target re-read (a new reset, say) keeps its table on screen meanwhile.
  const done = useRef<{ epoch: string; byMeter: Map<string, number> }>({ epoch, byMeter: new Map() })

  useEffect(() => {
    if (!supported) return
    const targets: Target[] = JSON.parse(targetsKey)
    let cancelled = false
    const inflight = new Map<string, AbortController>()
    const update = (accountKey: string, load: SetLoad) => setLoads(previous => {
      const byAccount = previous.epoch === epoch ? previous.byAccount : {}
      if (load.status === 'error' && byAccount[accountKey]?.status === 'ok') return previous
      return { epoch, byAccount: { ...byAccount, [accountKey]: load } }
    })
    if (done.current.epoch !== epoch) done.current = { epoch, byMeter: new Map() }
    if (lastReload.current !== reloadKey) {
      lastReload.current = reloadKey
      done.current.byMeter.clear()
    }
    const doneKey = (target: Target) => `${target.meterId}|${target.resetMinute}`
    const read = async (target: Target) => {
      const { accountKey, meterId, resetsAt } = target
      if (mock) {
        done.current.byMeter.set(doneKey(target), Date.now())
        update(accountKey, { status: 'ok', data: mockProjectUsageSet(resetsAt), at: Date.now() })
        return
      }
      if (!fetchProjects || inflight.has(accountKey)) return
      const controller = new AbortController()
      inflight.set(accountKey, controller)
      try {
        for (let attempt = 0; ; attempt++) {
          try {
            const data = await fetchProjects(meterId, AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)]))
            if (cancelled) return
            done.current.byMeter.set(doneKey(target), Date.now())
            const at = Date.now()
            update(accountKey, { status: 'ok', data, at })
            writeProjectCache(authKey, accountKey, data, at)
            return
          } catch (error: unknown) {
            if (cancelled) return
            if (error instanceof LimitsDenied) {
              clearProjectCache()
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
        update(accountKey, { status: 'error', message: error instanceof Error && error.name === 'TimeoutError'
          ? 'Reading project usage timed out. Try again.' : error instanceof Error ? error.message : 'Could not read project usage.' })
      } finally {
        inflight.delete(accountKey)
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
      update(target.accountKey, { status: 'loading' })
      void read(target)
    }
    void readAll(target => Date.now() - (done.current.byMeter.get(doneKey(target)) ?? 0) >= PROJECTS_REFRESH_MS)
    const timer = window.setInterval(() => { void readAll() }, PROJECTS_REFRESH_MS)
    return () => {
      cancelled = true
      readOne.current = () => {}
      window.clearInterval(timer)
      for (const controller of inflight.values()) controller.abort()
      inflight.clear()
    }
  }, [supported, mock, fetchProjects, targetsKey, epoch, reloadKey, authKey])

  return useMemo(() => {
    const targets: Target[] = JSON.parse(targetsKey)
    const rangeOf = (accountKey: string): ProjectRange => {
      if (Object.hasOwn(ranges, accountKey)) return ranges[accountKey]
      return targets.find(t => t.accountKey === accountKey)?.resetsAt ? DEFAULT_PROJECT_RANGE : '7d'
    }
    return {
      supported,
      get: (accountKey: string): ProjectLoad | undefined => {
        const load = loads.epoch === epoch ? loads.byAccount[accountKey] : undefined
        if (load?.status === 'ok') return { ...load, data: load.data[rangeOf(accountKey)] }
        const copy = cached[accountKey]
        if (copy) return { status: 'ok', data: copy.data[rangeOf(accountKey)], at: copy.at, stale: load?.status !== 'error' }
        return load
      },
      range: rangeOf,
      select: (accountKey: string, range: ProjectRange) => {
        if (!isProjectRange(range)) return
        const next = { ...ranges, [accountKey]: range }
        setRanges(next)
        try { prefs.set(RANGE_STORAGE, JSON.stringify(next)) } catch { /* Storage may be blocked. */ }
      },
      retry: (accountKey: string) => readOne.current(accountKey),
    }
  }, [supported, loads, epoch, targetsKey, ranges, prefs, cached])
}
