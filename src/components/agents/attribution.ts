// Account history on the machine and region panels (SPEC "Account history: machine attribution"). A provider's
// account totals beyond the local records (api/src/lib/account-usage.js, recoveredTokens) arrive filed under
// machine "unknown" and country "ZZ": the provider counts per account and UTC day, never per machine. Every panel
// that splits by machine or region assigns them, at read time, to the machines that most probably made them, and
// marks the share as an estimate. Day, provider and model totals never change. Pure functions, so
// attribution.test.ts runs them under plain Node.
import type { PlaceBucket, Provider, ProviderDay, PublicMachine, WorkspaceDay } from './types'
import { ACCOUNT_HISTORY, addDays, formatCompact, PROVIDER_LABEL, PROVIDERS, type NormDay } from './usage.ts'

export const UNKNOWN_MACHINE = 'unknown'
export const UNKNOWN_COUNTRY = 'ZZ'
/** How far from a day's account history to look for the provider's local activity. */
export const NEAREST_DAYS = 7

/**
 * Which rule placed a day's account history: the same day's activity, the next or previous day's (the totals are
 * per UTC day, machine rows per machine-local day), the nearest activity within NEAREST_DAYS, else the machine
 * with the most of the provider's tokens overall.
 */
export type AttributionRule = 'same-day' | 'adjacent-day' | 'nearest' | 'most-used'

export interface Attribution {
  rule: AttributionRule
  /** Machine id → weight: its share of the amount (see attributionFor). */
  shares: Map<string, number>
}

/** One machine's activity of one provider on one local date. */
export interface MachineActivity {
  /** Recorded tokens. */
  tokens: number
  prompts: number
  /** Prompts whose tokens were never recorded (deleted or missing logs): the usual source of account history. */
  unlogged: number
  /** Tokens those prompts most probably used: `unlogged` × the machine's tokens per logged prompt nearby. */
  expected: number
}

/** The hover of a machine or region row that includes assigned account history. */
export const assignedNote = (tokens: number, providers: Provider[]) =>
  `includes ${formatCompact(tokens)} estimated from ${providers.map(p => PROVIDER_LABEL[p]).join(' and ') || 'provider'} account totals (no local logs)`

const recorded = (b: PlaceBucket) => b.in + b.cacheW + b.cacheR + b.out
const emptyPlace = (): PlaceBucket => ({ in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 })
const isEmpty = (b: PlaceBucket) => recorded(b) <= 0 && (b.unattributed ?? 0) <= 0 && b.prompts <= 0 && (b.promptsNoUsage ?? 0) <= 0

/**
 * Each machine's prompts without recorded tokens on a provider day. The API's workspace groups count them per
 * machine. Without them (the GitHub Pages dashboard) only the day's total is known: it goes to the one machine
 * with prompts, else to the machines with prompts and no recorded tokens at all, at most their prompts each.
 */
function unloggedPrompts(p: ProviderDay): Map<string, number> {
  const out = new Map<string, number>()
  const groups = Object.values(p.byWorkspace ?? {})
  if (groups.length) {
    for (const group of groups) for (const [id, b] of Object.entries(group.byMachine)) {
      if (id !== UNKNOWN_MACHINE && (b.promptsNoUsage ?? 0) > 0) out.set(id, (out.get(id) ?? 0) + b.promptsNoUsage!)
    }
    return out
  }
  const budget = p.promptsNoUsage
  if (budget <= 0) return out
  const prompting = Object.entries(p.byMachine).filter(([id, b]) => id !== UNKNOWN_MACHINE && b.prompts > 0)
  if (prompting.length === 1) {
    out.set(prompting[0][0], Math.min(budget, prompting[0][1].prompts))
    return out
  }
  const silent = prompting.filter(([, b]) => recorded(b) <= 0)
  const prompts = silent.reduce((sum, [, b]) => sum + b.prompts, 0)
  for (const [id, b] of silent) out.set(id, b.prompts * Math.min(1, budget / prompts))
  return out
}

/** The dates within `radius` days of `date`, both ends included. */
function datesAround(date: string, radius: number) {
  const out: string[] = []
  for (let d = -radius; d <= radius; d++) out.push(addDays(date, d))
  return out
}

/**
 * One provider's activity per local date and machine (the unknown machine and account history excluded): recorded
 * tokens, prompts, and the prompts without recorded tokens with the tokens they most probably used, at the rate of
 * that machine's logged prompts within NEAREST_DAYS (else the fleet's then, else the fleet's overall).
 */
export function providerActivity(days: NormDay[], provider: Provider): Map<string, Map<string, MachineActivity>> {
  const out = new Map<string, Map<string, MachineActivity>>()
  for (const day of days) {
    const p = day.providers[provider]
    if (!p) continue
    const unlogged = unloggedPrompts(p)
    for (const [id, b] of Object.entries(p.byMachine)) {
      if (id === UNKNOWN_MACHINE) continue
      const entry: MachineActivity = { tokens: recorded(b), prompts: b.prompts, unlogged: Math.min(b.prompts, unlogged.get(id) ?? 0), expected: 0 }
      if (entry.tokens <= 0 && entry.prompts <= 0) continue
      const machines = out.get(day.date) ?? new Map<string, MachineActivity>()
      machines.set(id, entry)
      out.set(day.date, machines)
    }
  }
  // Tokens per logged prompt: only machine-days with both, as a day's tokens can belong to another day's prompts.
  const rate = (dates: Iterable<string>, machine: string | null) => {
    let tokens = 0
    let prompts = 0
    for (const date of dates) for (const [id, a] of out.get(date) ?? []) {
      if ((machine === null || id === machine) && a.tokens > 0 && a.prompts > a.unlogged) {
        tokens += a.tokens
        prompts += a.prompts - a.unlogged
      }
    }
    return prompts > 0 ? tokens / prompts : null
  }
  const overall = rate(out.keys(), null)
  for (const [date, machines] of out) for (const [id, a] of machines) {
    if (a.unlogged <= 0) continue
    const near = datesAround(date, NEAREST_DAYS)
    // No logged prompt anywhere: each unlogged prompt weighs one token, so only the prompt counts decide.
    a.expected = a.unlogged * (rate(near, id) ?? rate(near, null) ?? overall ?? 1)
  }
  return out
}

/**
 * The machines `amount` of a date's account history goes to, or null when no machine ever had the provider's
 * activity. Account history is what the provider counted beyond the local records, so the prompts that have no
 * records explain it first: up to the tokens they most probably used, in proportion to those. The rest, which
 * the provider counts but its tools never log, goes in proportion to the recorded tokens. Both come from the same
 * day; else from the adjacent days; else from the smallest window within NEAREST_DAYS with any.
 */
export function attributionFor(activity: Map<string, Map<string, MachineActivity>>, date: string, amount: number): Attribution | null {
  for (let radius = 0; radius <= NEAREST_DAYS; radius++) {
    const tokens = new Map<string, number>()
    const expected = new Map<string, number>()
    const dates = radius === 0 ? [date] : [addDays(date, -radius), addDays(date, radius)]
    // Beyond the adjacent days, weigh every machine by its activity over the whole window, not just its edge.
    if (radius > 1) for (let near = 1 - radius; near < radius; near++) dates.push(addDays(date, near))
    for (const d of dates) for (const [id, a] of activity.get(d) ?? []) {
      if (a.tokens > 0) tokens.set(id, (tokens.get(id) ?? 0) + a.tokens)
      if (a.expected > 0) expected.set(id, (expected.get(id) ?? 0) + a.expected)
    }
    if (!tokens.size && !expected.size) continue
    const rule: AttributionRule = radius === 0 ? 'same-day' : radius === 1 ? 'adjacent-day' : 'nearest'
    const sumOf = (m: Map<string, number>) => [...m.values()].reduce((sum, v) => sum + v, 0)
    if (!expected.size) return { rule, shares: tokens }
    if (!tokens.size) return { rule, shares: expected }
    const byUnlogged = Math.min(amount, sumOf(expected))
    const shares = new Map<string, number>()
    for (const [id, v] of expected) shares.set(id, byUnlogged * v / sumOf(expected))
    const rest = amount - byUnlogged
    for (const [id, v] of tokens) shares.set(id, (shares.get(id) ?? 0) + rest * v / sumOf(tokens))
    return { rule, shares }
  }
  // Most recorded tokens overall; prompts decide when no machine recorded any.
  const overall = new Map<string, [number, number]>()
  for (const machines of activity.values()) for (const [id, a] of machines) {
    const [tokens, prompts] = overall.get(id) ?? [0, 0]
    overall.set(id, [tokens + a.tokens, prompts + a.prompts])
  }
  let top: [string, [number, number]] | null = null
  for (const entry of overall) {
    const [, [tokens, prompts]] = entry
    if (!top || tokens > top[1][0] || (tokens === top[1][0] && (prompts > top[1][1] || (prompts === top[1][1] && entry[0] < top[0])))) top = entry
  }
  return top ? { rule: 'most-used', shares: new Map([[top[0], 1]]) } : null
}

/** `amount` split by weight in whole tokens (largest remainder), summing to exactly `amount`. */
export function splitTokens(amount: number, shares: Map<string, number>): Map<string, number> {
  const entries = [...shares].filter(([, w]) => w > 0).sort(([a], [b]) => a.localeCompare(b))
  const weight = entries.reduce((sum, [, w]) => sum + w, 0)
  const out = new Map<string, number>()
  if (amount <= 0 || weight <= 0) return out
  let left = amount
  const remainders: Array<[string, number]> = []
  for (const [id, w] of entries) {
    const exact = amount * (w / weight)
    const part = Math.floor(exact)
    out.set(id, part)
    left -= part
    remainders.push([id, exact - part])
  }
  remainders.sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  for (let i = 0; left >= 1 && i < remainders.length; i++, left--) out.set(remainders[i][0], out.get(remainders[i][0])! + 1)
  // A fractional total (never sent by the API) or float drift stays with the largest remainder, so the sum holds.
  if (left !== 0) out.set(remainders[0][0], out.get(remainders[0][0])! + left)
  for (const [id, tokens] of out) if (tokens <= 0) out.delete(id)
  return out
}

/** A place bucket with `tokens` more account history, marked as assigned by estimate. */
function withAssigned(b: PlaceBucket | undefined, tokens: number): PlaceBucket {
  const base = b ?? emptyPlace()
  return { ...base, unattributed: (base.unattributed ?? 0) + tokens, assigned: (base.assigned ?? 0) + tokens }
}

/** One provider day (or workspace group) with its unknown machine's account history moved to `shares`. */
function assign<T extends ProviderDay | WorkspaceDay>(p: T, shares: Map<string, number>, countryOf: (id: string) => string): T {
  const from = p.byMachine[UNKNOWN_MACHINE]
  const amount = from?.unattributed ?? 0
  if (amount <= 0) return p
  const byMachine = { ...p.byMachine }
  const rest = { ...from, unattributed: 0 }
  if (isEmpty(rest)) delete byMachine[UNKNOWN_MACHINE]
  else byMachine[UNKNOWN_MACHINE] = rest
  const split = splitTokens(amount, shares)
  for (const [id, tokens] of split) byMachine[id] = withAssigned(byMachine[id], tokens)
  // The country split carries the same account history under ZZ: move what is there, by the same machines.
  const byCountry = { ...p.byCountry }
  const zz = byCountry[UNKNOWN_COUNTRY]
  const moved = Math.min(amount, zz?.unattributed ?? 0)
  if (zz && moved > 0) {
    const left = { ...zz, unattributed: (zz.unattributed ?? 0) - moved }
    if (isEmpty(left)) delete byCountry[UNKNOWN_COUNTRY]
    else byCountry[UNKNOWN_COUNTRY] = left
    const byCc = new Map<string, number>()
    for (const [id, tokens] of split) byCc.set(countryOf(id), (byCc.get(countryOf(id)) ?? 0) + tokens)
    for (const [cc, tokens] of splitTokens(moved, byCc)) byCountry[cc] = withAssigned(byCountry[cc], tokens)
  }
  return { ...p, byMachine, byCountry }
}

/** Machine id → its public country, ZZ when it has none. */
export const countryLookup = (machines: PublicMachine[]) => {
  const byId = new Map(machines.map(m => [m.id, /^[A-Za-z]{2}$/.test(m.cc ?? '') ? m.cc.toUpperCase() : UNKNOWN_COUNTRY]))
  return (id: string) => byId.get(id) ?? UNKNOWN_COUNTRY
}

/**
 * The days with every provider's account history assigned to machines (and through them to regions): first to
 * the machines whose prompts that day have no recorded tokens (deleted or missing logs), up to the tokens those
 * prompts most probably used; the rest in proportion to each machine's recorded tokens of that provider (see
 * attributionFor). The same day decides; else the adjacent days (UTC vs machine-local dates); else the activity
 * within NEAREST_DAYS; else the machine with the most of that provider's tokens. The public data does not link
 * machines to accounts, so the fleet's machines stand in for every account. Only a provider no machine ever used
 * keeps its "unknown" machine. Never mutates the input; returns it as is when nothing needs assigning.
 */
export function attributeAccountHistory(days: NormDay[], machines: PublicMachine[]): NormDay[] {
  const providers = PROVIDERS.filter(provider => days.some(day => (day.providers[provider]?.byMachine[UNKNOWN_MACHINE]?.unattributed ?? 0) > 0))
  if (!providers.length) return days
  const countryOf = countryLookup(machines)
  const activity = new Map(providers.map(provider => [provider, providerActivity(days, provider)]))
  let changed = false
  const out = days.map(day => {
    let next: NormDay | null = null
    for (const provider of providers) {
      const p = day.providers[provider]
      if (!p || (p.byMachine[UNKNOWN_MACHINE]?.unattributed ?? 0) <= 0) continue
      const basis = attributionFor(activity.get(provider)!, day.date, p.byMachine[UNKNOWN_MACHINE]!.unattributed!)
      if (!basis) continue
      const out = assign(p, basis.shares, countryOf)
      if (p.byWorkspace) {
        out.byWorkspace = Object.fromEntries(Object.entries(p.byWorkspace).map(([ws, group]) => [ws, assign(group, basis.shares, countryOf)]))
      }
      next ??= { ...day, providers: { ...day.providers } }
      next.providers[provider] = out
      changed = true
    }
    return next ?? day
  })
  return changed ? out : days
}

const emptyProvider = (): ProviderDay => ({
  exact: { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 },
  estimated: { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 },
  prompts: 0, promptsNoUsage: 0, accts: [], models: { exact: {}, estimated: {} }, byMachine: {}, byCountry: {},
})

/**
 * One machine's own days (a source that scopes exactly, such as the GitHub Pages dashboard) with the account
 * history `fleet` (attributed) assigned to that machine, as account history in its totals and on its machine and
 * country rows. `cc` is the machine's public country (ZZ when it has none).
 */
export function addAssignedHistory(days: NormDay[], fleet: NormDay[], machine: string, cc = UNKNOWN_COUNTRY): NormDay[] {
  const byDate = new Map(days.map(day => [day.date, day]))
  let changed = false
  for (const fleetDay of fleet) {
    for (const provider of PROVIDERS) {
      const tokens = fleetDay.providers[provider]?.byMachine[machine]?.assigned ?? 0
      if (tokens <= 0) continue
      changed = true
      const day = byDate.get(fleetDay.date) ?? { date: fleetDay.date, providers: {} }
      const p = structuredClone(day.providers[provider] ?? emptyProvider())
      p.exact.unattributed = (p.exact.unattributed ?? 0) + tokens
      p.assignedHistory = (p.assignedHistory ?? 0) + tokens
      const history = p.models.exact[ACCOUNT_HISTORY] ?? { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0 }
      p.models.exact[ACCOUNT_HISTORY] = { ...history, unattributed: (history.unattributed ?? 0) + tokens }
      p.byMachine[machine] = withAssigned(p.byMachine[machine], tokens)
      p.byCountry[cc] = withAssigned(p.byCountry[cc], tokens)
      byDate.set(fleetDay.date, { ...day, providers: { ...day.providers, [provider]: p } })
    }
  }
  return changed ? [...byDate.values()].sort((a, b) => a.date.localeCompare(b.date)) : days
}
