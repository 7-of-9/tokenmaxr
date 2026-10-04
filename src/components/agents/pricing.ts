// The /tokens pricing mode ("Prices: at the time | today"), shared by the Detail cost tile and the heatmap tooltip,
// and the pricing table, loaded once on demand so Overview's first paint does not wait for it.
import { useEffect, useState, useSyncExternalStore } from 'react'
import { DEFAULT_PRICE_MODE, type PriceMode, type PricingTable } from './cost'

const MODE_KEY = 'd0m1.tokens.prices'

function readMode(): PriceMode {
  try {
    return window.localStorage.getItem(MODE_KEY) === 'today' ? 'today' : DEFAULT_PRICE_MODE
  } catch {
    return DEFAULT_PRICE_MODE
  }
}

let mode: PriceMode | null = null
const listeners = new Set<() => void>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  // Another tab changed it.
  const onStorage = (event: StorageEvent) => {
    if (event.key !== MODE_KEY) return
    mode = readMode()
    listener()
  }
  window.addEventListener('storage', onStorage)
  return () => {
    listeners.delete(listener)
    window.removeEventListener('storage', onStorage)
  }
}

const getMode = () => (mode ??= readMode())

export function setPriceMode(next: PriceMode) {
  mode = next
  try {
    window.localStorage.setItem(MODE_KEY, next)
  } catch {
    // Private mode or blocked storage: the choice still holds until the page closes.
  }
  for (const listener of listeners) listener()
}

/** The saved pricing mode (at the time unless the owner chose today), the same in every component that reads it. */
export function usePriceMode(): [PriceMode, (next: PriceMode) => void] {
  return [useSyncExternalStore(subscribe, getMode, () => DEFAULT_PRICE_MODE), setPriceMode]
}

let table: PricingTable | null = null
let loading: Promise<PricingTable> | null = null

/** modelPricing.json, or null until its chunk has loaded. */
export function usePricingTable(): PricingTable | null {
  const [loaded, setLoaded] = useState(table)
  useEffect(() => {
    if (loaded) return
    let live = true
    loading ??= import('../../data/modelPricing.json').then((m) => (table = m.default as unknown as PricingTable))
    loading
      .then((t) => live && setLoaded(t))
      .catch(() => {
        // A failed chunk load: try again on the next mount. Costs simply stay hidden meanwhile.
        loading = null
      })
    return () => {
      live = false
    }
  }, [loaded])
  return loaded
}
