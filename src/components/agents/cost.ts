// API-equivalent cost at list prices (SPEC "Detail" 2). Pure functions: the pricing
// table is passed in, so cost.test.ts runs them under plain Node.

export interface PriceRates {
  /** USD per million tokens. null or missing means not priced. */
  input: number | null
  output: number | null
  cacheRead: number | null
  cacheWrite: number | null
  /** Anthropic 1-hour cache writes; falls back to cacheWrite. */
  cacheWrite1h?: number | null
}

/** A dated list price, in effect from `from` through `to` (inclusive local dates); `to` is null while it is current. */
export interface PriceWindow extends Partial<PriceRates> {
  from: string
  to: string | null
  sources?: string[]
  note?: string
}

export interface ModelPrice extends PriceRates {
  provider?: string
  displayName?: string
  /** Oldest first. Missing dates or rates are unpriced; historical costs never borrow today's rates. */
  history?: PriceWindow[]
}

export interface FamilyFallback {
  provider?: string
  pattern: string
  useModel: string
}

export interface PricingTable {
  models: Record<string, ModelPrice>
  familyFallbacks: FamilyFallback[]
}

/** 'at-the-time': each day at the list price in effect that day. 'today': every day at today's list price. */
export type PriceMode = 'at-the-time' | 'today'

export const DEFAULT_PRICE_MODE: PriceMode = 'at-the-time'

export interface PricedTokens {
  /** A known total without the token split needed to calculate a cost. */
  unattributed?: number
  in: number
  cacheW: number
  /** Subset of cacheW. */
  cacheW1h: number
  cacheR: number
  out: number
}

export interface PriceMatch {
  /** The pricing entry used. */
  id: string
  price: ModelPrice
  via: 'exact' | 'family'
}

const lookups = new WeakMap<PricingTable, Map<string, PriceMatch | null>>()

/** Exact lowercased id first, then the first familyFallbacks pattern that matches (array order matters). Memoised per table. */
export function lookupPrice(model: string, table: PricingTable): PriceMatch | null {
  const id = model.trim().toLowerCase()
  let memo = lookups.get(table)
  if (!memo) {
    memo = new Map()
    lookups.set(table, memo)
  }
  const hit = memo.get(id)
  if (hit !== undefined) return hit
  const match = resolve(id, table)
  memo.set(id, match)
  return match
}

function resolve(id: string, table: PricingTable): PriceMatch | null {
  if (!id || id === 'unknown') return null
  const exact = table.models[id]
  if (exact) return { id, price: exact, via: 'exact' }
  for (const rule of table.familyFallbacks) {
    let re: RegExp
    try {
      re = new RegExp(rule.pattern)
    } catch {
      continue
    }
    if (!re.test(id)) continue
    const price = table.models[rule.useModel]
    if (price) return { id: rule.useModel, price, via: 'family' }
  }
  return null
}

/** The history window covering a local date (YYYY-MM-DD), or null when none does. */
export function windowOn(price: ModelPrice, date: string): PriceWindow | null {
  for (const w of price.history ?? []) if (date >= w.from && (w.to === null || date <= w.to)) return w
  return null
}

/** The verified rates on a date. No window or missing rate means unpriced, not today's price. */
export function ratesOn(price: ModelPrice, date: string): PriceRates {
  const w = windowOn(price, date)
  return {
    input: w?.input ?? null,
    output: w?.output ?? null,
    cacheRead: w?.cacheRead ?? null,
    cacheWrite: w?.cacheWrite ?? null,
    cacheWrite1h: w?.cacheWrite1h ?? null,
  }
}

const rate = (value: number | null | undefined) => (typeof value === 'number' && Number.isFinite(value) ? value : null)

/**
 * in×input + (cacheW−cacheW1h)×cacheWrite + cacheW1h×(cacheWrite1h ?? cacheWrite) + cacheR×cacheRead + out×output,
 * per million tokens. Returns null when a bucket that has tokens has no rate.
 */
export function costOf(tokens: PricedTokens, price: PriceRates): number | null {
  if ((tokens.unattributed ?? 0) > 0) return null
  const w1h = Math.min(Math.max(0, tokens.cacheW1h), Math.max(0, tokens.cacheW))
  const parts: Array<[number, number | null]> = [
    [tokens.in, rate(price.input)],
    [tokens.cacheW - w1h, rate(price.cacheWrite)],
    [w1h, rate(price.cacheWrite1h) ?? rate(price.cacheWrite)],
    [tokens.cacheR, rate(price.cacheRead)],
    [tokens.out, rate(price.output)],
  ]
  let dollars = 0
  for (const [count, perMillion] of parts) {
    if (count <= 0) continue
    if (perMillion === null) return null
    dollars += (count * perMillion) / 1e6
  }
  return dollars
}

const hasTokens = (t: PricedTokens) => t.in + t.cacheW + t.cacheR + t.out + (t.unattributed ?? 0) > 0

/** One local day of a model's tokens. */
export interface DayTokens {
  date: string
  tokens: PricedTokens
}

export interface DatedCost {
  /** null when no day could be priced. */
  usd: number | null
  /** Days with tokens but no rate in this mode; they are left out of usd. */
  unpricedDays: number
}

/**
 * A model's cost over its days, summed. 'today' prices every day at today's rates; 'at-the-time' prices each day
 * at the documented window in effect on it. Uncovered days are excluded and counted as unpriced.
 */
export function costOverDays(byDay: DayTokens[], price: ModelPrice, mode: PriceMode): DatedCost {
  let usd: number | null = null
  let unpricedDays = 0
  for (const { date, tokens } of byDay) {
    if (!hasTokens(tokens)) continue
    const dollars = costOf(tokens, mode === 'today' ? price : ratesOn(price, date))
    if (dollars === null) unpricedDays += 1
    else usd = (usd ?? 0) + dollars
  }
  return { usd, unpricedDays }
}

export interface CostInput {
  model: string
  provider: string
  /** The period's summed tokens (what the table shows). */
  tokens: PricedTokens
  /** The same tokens per local day, required for at-the-time pricing. */
  byDay?: DayTokens[]
}

export type CostRow<M extends CostInput = CostInput> = M & {
  match: PriceMatch | null
  /** In the chosen mode; null when unpriced. */
  usd: number | null
  /** Both modes, for the hint that compares them. */
  usdAtTheTime: number | null
  usdToday: number | null
  /** Some of the row's days had no list price in the chosen mode and are left out of usd. */
  partial: boolean
}

function rowCost(m: CostInput, price: ModelPrice, mode: PriceMode): DatedCost {
  if (m.byDay) return costOverDays(m.byDay, price, mode)
  if (mode === 'at-the-time') return { usd: null, unpricedDays: hasTokens(m.tokens) ? 1 : 0 }
  const usd = costOf(m.tokens, price)
  return { usd, unpricedDays: usd === null && hasTokens(m.tokens) ? 1 : 0 }
}

export interface CostTable<M extends CostInput> {
  rows: CostRow<M>[]
  /** In the chosen mode. */
  totalUsd: number
  totalAtTheTime: number
  totalToday: number
}

/** Priced rows sorted by $ in the chosen mode (largest first); unpriced rows follow, largest token count first. */
export function costRows<M extends CostInput>(models: M[], table: PricingTable, mode: PriceMode = DEFAULT_PRICE_MODE): CostTable<M> {
  let totalAtTheTime = 0
  let totalToday = 0
  const rows = models.map((m): CostRow<M> => {
    const match = lookupPrice(m.model, table)
    const then = match ? rowCost(m, match.price, 'at-the-time') : null
    const now = match ? rowCost(m, match.price, 'today') : null
    const chosen = mode === 'today' ? now : then
    totalAtTheTime += then?.usd ?? 0
    totalToday += now?.usd ?? 0
    return {
      ...m,
      match,
      usd: chosen?.usd ?? null,
      usdAtTheTime: then?.usd ?? null,
      usdToday: now?.usd ?? null,
      partial: chosen?.usd != null && chosen.unpricedDays > 0,
    }
  })
  const size = (t: PricedTokens) => t.in + t.cacheW + t.cacheR + t.out + (t.unattributed ?? 0)
  rows.sort((a, b) => {
    if (a.usd !== null && b.usd !== null) return b.usd - a.usd
    if (a.usd !== null) return -1
    if (b.usd !== null) return 1
    return size(b.tokens) - size(a.tokens)
  })
  return { rows, totalUsd: mode === 'today' ? totalToday : totalAtTheTime, totalAtTheTime, totalToday }
}

export interface DayModelTokens {
  model: string
  tokens: PricedTokens
}

export interface DayCost {
  usd: number
  /** Some of the day's tokens are on models with no list price in this mode. */
  unpriced: boolean
}

/** One day's cost over its model buckets in the chosen mode. */
export function dayCost(date: string, models: DayModelTokens[], table: PricingTable, mode: PriceMode): DayCost {
  let usd = 0
  let unpriced = false
  for (const { model, tokens } of models) {
    if (!hasTokens(tokens)) continue
    const match = lookupPrice(model, table)
    const dollars = match ? costOf(tokens, mode === 'today' ? match.price : ratesOn(match.price, date)) : null
    if (dollars === null) unpriced = true
    else usd += dollars
  }
  return { usd, unpriced }
}

export function formatUsd(value: number) {
  if (!Number.isFinite(value)) return '—'
  if (value >= 100) return `$${Math.round(value).toLocaleString('en-US')}`
  if (value >= 0.01) return `$${value.toFixed(2)}`
  return value > 0 ? '<$0.01' : '$0'
}
