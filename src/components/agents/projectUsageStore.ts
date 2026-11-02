// The page's preloaded project usage: one reading per weekly meter, for every account at once.
import { useEffect, useMemo, useRef, useState } from 'react'
import { LimitsDenied, type Account } from './limits'
import { mockProjectUsage, type ProjectUsageResponse } from './projectUsage'
import { useUsageSource } from './source'
import { useTokensSite } from './site'

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

/** The page's preloaded project usage, one entry per weekly meter. */
export interface ProjectUsageStore {
  /** False when this data source has no project usage: the panels are not offered. */
  supported: boolean
  /** The reading for a weekly meter, or undefined while nothing has been read for it. */
  get(meterId: string | undefined): ProjectLoad | undefined
  /** Read one meter again now (after an error). */
  retry(meterId: string): void
}

export interface ProjectExpansionProps { mock: boolean; usage: ProjectUsageStore }

interface Target { meterId: string; resetsAt: number }

/**
 * Preloads every account's project usage (owner direction 2026-11-02: "preload that project-usage data for all
 * accounts so the ux is slick"). Reads run a few at a time, keep the last good table on screen while they refresh,
 * start over with a new owner, source or Refresh, and repeat every few minutes.
 */
export function useProjectUsage(accounts: Account[], mock: boolean, reloadKey: number, authKey: string): ProjectUsageStore {
  const source = useUsageSource()
  const site = useTokensSite()
  const onDenied = useRef(site.onDenied)
  onDenied.current = site.onDenied
  const fetchProjects = source.fetchLimitProjects
  const supported = mock || !!fetchProjects
  // Accounts are rebuilt every second for the countdowns; the effect keys on the meters themselves.
  const targetsKey = useMemo(() => {
    const seen = new Map<string, number>()
    for (const account of accounts) if (account.week?.key && account.week.resetsAt) seen.set(account.week.key, account.week.resetsAt)
    return JSON.stringify([...seen])
  }, [accounts])
  const epoch = JSON.stringify([source.cacheKey, authKey, mock, reloadKey])
  const [loads, setLoads] = useState<{ epoch: string; byMeter: Record<string, ProjectLoad> }>({ epoch, byMeter: {} })
  const readOne = useRef<(meterId: string) => void>(() => {})
  // What has been read in this epoch, by meter and its reset: a new account or a passed reset reads only itself.
  const done = useRef<{ epoch: string; byMeter: Map<string, number> }>({ epoch, byMeter: new Map() })

  useEffect(() => {
    if (!supported) return
    const targets: Target[] = (JSON.parse(targetsKey) as [string, number][]).map(([meterId, resetsAt]) => ({ meterId, resetsAt }))
    let cancelled = false
    const inflight = new Map<string, AbortController>()
    const update = (meterId: string, load: ProjectLoad) => setLoads(previous => ({
      epoch, byMeter: { ...(previous.epoch === epoch ? previous.byMeter : {}), [meterId]: load },
    }))
    if (done.current.epoch !== epoch) done.current = { epoch, byMeter: new Map() }
    const read = async ({ meterId, resetsAt }: Target) => {
      if (mock) {
        done.current.byMeter.set(meterId, resetsAt)
        update(meterId, { status: 'ok', data: mockProjectUsage(resetsAt), at: Date.now() })
        return
      }
      if (!fetchProjects || inflight.has(meterId)) return
      const controller = new AbortController()
      inflight.set(meterId, controller)
      try {
        for (let attempt = 0; ; attempt++) {
          try {
            const data = await fetchProjects(meterId, AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)]))
            if (cancelled) return
            done.current.byMeter.set(meterId, resetsAt)
            update(meterId, { status: 'ok', data, at: Date.now() })
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
        update(meterId, { status: 'error', message: error instanceof Error && error.name === 'TimeoutError'
          ? 'Reading project usage timed out. Try again.' : error instanceof Error ? error.message : 'Could not read project usage.' })
      } finally {
        inflight.delete(meterId)
      }
    }
    const readAll = (only: (target: Target) => boolean = () => true) => {
      const queue = targets.filter(only)
      const workers = Array.from({ length: Math.min(PARALLEL, queue.length) }, async () => {
        while (queue.length && !cancelled) await read(queue.shift()!)
      })
      return Promise.all(workers)
    }
    readOne.current = (meterId) => {
      const target = targets.find(t => t.meterId === meterId)
      if (!target) return
      update(meterId, { status: 'loading' })
      void read(target)
    }
    void readAll(target => done.current.byMeter.get(target.meterId) !== target.resetsAt)
    const timer = window.setInterval(() => { void readAll() }, PROJECTS_REFRESH_MS)
    return () => {
      cancelled = true
      readOne.current = () => {}
      window.clearInterval(timer)
      for (const controller of inflight.values()) controller.abort()
      inflight.clear()
    }
  }, [supported, mock, fetchProjects, targetsKey, epoch])

  return useMemo(() => ({
    supported,
    get: (meterId) => (meterId && loads.epoch === epoch ? loads.byMeter[meterId] : undefined),
    retry: (meterId) => readOne.current(meterId),
  }), [supported, loads, epoch])
}

