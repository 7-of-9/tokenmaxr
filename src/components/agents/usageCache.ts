import type { UsageResponse } from './types'

const MAX_AGE_MS = 24 * 60 * 60 * 1000
const key = (source: string) => `d0m1:usage:v1:${source}`

export interface UsageSnapshot { data: UsageResponse; loadedAt: number }

/** Only public aggregates, isolated by API origin. Reloads can render the last success immediately. */
export function readUsageCache(storage: Pick<Storage, 'getItem'>, source: string, now = Date.now()): UsageSnapshot | null {
  try {
    const cached = JSON.parse(storage.getItem(key(source)) ?? 'null') as UsageSnapshot | null
    if (!cached || !Number.isFinite(cached.loadedAt) || now - cached.loadedAt > MAX_AGE_MS || cached.loadedAt > now ||
      !Array.isArray(cached.data?.days) || typeof cached.data.generatedAt !== 'string') return null
    return cached
  } catch { return null }
}

export function writeUsageCache(storage: Pick<Storage, 'setItem'>, source: string, snapshot: UsageSnapshot) {
  try { storage.setItem(key(source), JSON.stringify(snapshot)) } catch { /* A full/disabled cache must not break live data. */ }
}
