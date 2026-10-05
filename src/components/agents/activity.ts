// The /tokens "Token activity" feed (SPEC "Visual design"): GitHub's contribution activity, per month, with
// models in place of repositories. Pure functions, so activity.test.ts runs them under plain Node.
// Project and workspace names never reach this module: rollups carry models and machine ids only.
import type { Provider, PublicMachine } from './types'
import { ACCOUNT_HISTORY, metricOf, promptModelsOf, PROVIDERS, type NormDay, type ViewOptions } from './usage.ts'

export interface ActivityModel {
  model: string
  provider: Provider
  value: number
  prompts: number | null
}

export interface ActivityMachine {
  key: string
  label: string
  cc: string
  value: number
  prompts: number
  /** Part of value: account history assigned to this machine by estimate (attribution.ts). */
  assigned: number
  assignedProviders: Provider[]
}

export interface ActivityStart {
  model: string
  provider: Provider
  /** First day the model has recorded tokens (in the current view). */
  date: string
}

export interface ActivityMonth {
  /** YYYY-MM */
  month: string
  total: number
  prompts: number
  /** Included in total; the account history identifies no model, and its machine split is an estimate. */
  accountHistory: number
  /** Largest first. */
  models: ActivityModel[]
  /** Largest first; a machine with only unrecorded prompts has value 0. */
  machines: ActivityMachine[]
  /** Models whose first recorded tokens fall in this month, oldest first. Empty in the month history starts. */
  started: ActivityStart[]
  /** Prompts whose tokens were never recorded. */
  promptsNoUsage: number
  /** The first day anything was recorded, on the month history starts (GitHub's "Joined GitHub"). */
  firstRecorded?: string
}

/** Placeholder ids a source writes when it cannot name the model: counted, but never "started". */
const UNNAMED = new Set(['unknown', '', ACCOUNT_HISTORY])

const providersOf = (view: ViewOptions) => (view.provider === 'all' ? PROVIDERS : [view.provider])

/** Newest month first, only months with tokens or prompts in [from, to]. */
export function buildActivity(days: NormDay[], view: ViewOptions, machines: PublicMachine[], from: string, to: string): ActivityMonth[] {
  const providers = providersOf(view)
  const byId = new Map(machines.map((m) => [m.id, m]))

  // First use per model, and the first day with anything at all, over the whole history (not just the period).
  const firstUse = new Map<string, ActivityStart>()
  let historyStart: string | null = null
  for (const day of days) {
    for (const provider of providers) {
      const p = day.providers[provider]
      if (!p) continue
      const tiers = view.exactOnly ? [p.models.exact] : [p.models.exact, p.models.estimated]
      let any = p.prompts > 0
      for (const tier of tiers) {
        for (const [model, b] of Object.entries(tier)) {
          if (metricOf(b, view.metric) <= 0) continue
          any = true
          const key = `${provider}|${model}`
          if (!firstUse.has(key)) firstUse.set(key, { model, provider, date: day.date })
        }
      }
      if (any && (historyStart === null || day.date < historyStart)) historyStart = day.date
    }
  }
  const historyMonth = historyStart?.slice(0, 7) ?? null

  interface Acc {
    total: number
    models: Map<string, ActivityModel>
    machines: Map<string, ActivityMachine>
    promptsNoUsage: number
    prompts: number
  }
  const months = new Map<string, Acc>()
  for (const day of days) {
    if (day.date < from || day.date > to) continue
    const month = day.date.slice(0, 7)
    let acc = months.get(month)
    if (!acc) {
      acc = { total: 0, models: new Map(), machines: new Map(), promptsNoUsage: 0, prompts: 0 }
      months.set(month, acc)
    }
    for (const provider of providers) {
      const p = day.providers[provider]
      if (!p) continue
      acc.promptsNoUsage += p.promptsNoUsage
      acc.prompts += p.prompts
      const tiers = view.exactOnly ? [p.models.exact] : [p.models.exact, p.models.estimated]
      for (const tier of tiers) {
        for (const [model, b] of Object.entries(tier)) {
          const value = metricOf(b, view.metric)
          if (value <= 0) continue
          const key = `${provider}|${model}`
          const entry = acc.models.get(key) ?? { model, provider, value: 0, prompts: model === ACCOUNT_HISTORY ? null : 0 }
          entry.value += value
          acc.models.set(key, entry)
        }
      }
      // Prompt counts are independent of token quality, and are counted only once.
      for (const [model, count] of Object.entries(promptModelsOf(p))) {
        const key = `${provider}|${model}`
        if (!acc.models.has(key) && !(count && count > 0)) continue
        const entry = acc.models.get(key) ?? { model, provider, value: 0, prompts: 0 }
        entry.prompts = count === null || entry.prompts === null ? null : entry.prompts + count
        acc.models.set(key, entry)
      }
      // Model buckets are what the total is made of, so the headline and the rows always add up. Account history
      // counts on a machine only once assigned to it (attribution.ts); what no machine could take stays out.
      for (const [id, b] of Object.entries(p.byMachine)) {
        const m = byId.get(id)
        const entry = acc.machines.get(id) ?? { key: id, label: m?.label || (id === 'unknown' ? 'Unknown' : 'Unnamed machine'), cc: m?.cc || 'ZZ',
          value: 0, prompts: 0, assigned: 0, assignedProviders: [] }
        const assigned = view.metric === 'tokens' ? b.assigned ?? 0 : 0
        entry.value += metricOf(b, view.metric) - (view.metric === 'tokens' ? (b.unattributed ?? 0) - assigned : 0)
        entry.prompts += b.prompts
        if (assigned > 0) {
          entry.assigned += assigned
          if (!entry.assignedProviders.includes(provider)) entry.assignedProviders.push(provider)
        }
        acc.machines.set(id, entry)
      }
    }
  }

  const started = new Map<string, ActivityStart[]>()
  for (const start of firstUse.values()) {
    const month = start.date.slice(0, 7)
    if (month === historyMonth || UNNAMED.has(start.model) || start.date < from || start.date > to) continue
    const list = started.get(month) ?? []
    list.push(start)
    started.set(month, list)
  }

  const out: ActivityMonth[] = []
  for (const [month, acc] of months) {
    const models = [...acc.models.values()].sort((a, b) => b.value - a.value || a.model.localeCompare(b.model))
    const total = models.reduce((sum, m) => sum + m.value, 0)
    const machineRows = [...acc.machines.values()]
      .filter((m) => m.value > 0 || m.prompts > 0)
      .sort((a, b) => b.value - a.value || b.prompts - a.prompts)
    const firstRecorded = historyStart && month === historyMonth && historyStart >= from && historyStart <= to ? historyStart : undefined
    if (total <= 0 && acc.prompts <= 0 && !firstRecorded) continue
    out.push({
      month,
      total,
      prompts: acc.prompts,
      accountHistory: models.filter(m => m.model === ACCOUNT_HISTORY).reduce((sum, m) => sum + m.value, 0),
      models,
      machines: machineRows,
      started: (started.get(month) ?? []).sort((a, b) => a.date.localeCompare(b.date) || a.model.localeCompare(b.model)),
      promptsNoUsage: acc.promptsNoUsage,
      firstRecorded,
    })
  }
  return out.sort((a, b) => b.month.localeCompare(a.month))
}

// ---- GitHub's phrasing ----

const MONTHS_LONG = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']

export function monthLong(monthIndex: number) {
  return MONTHS_LONG[monthIndex]
}

/** 1st, 2nd, 3rd, 4th … 11th, 12th, 13th … 21st, 22nd, 23rd. */
export function ordinal(n: number) {
  const tens = n % 100
  if (tens >= 11 && tens <= 13) return `${n}th`
  return `${n}${['th', 'st', 'nd', 'rd'][n % 10] ?? 'th'}`
}

/** GitHub's tooltip date: "September 28th", with the year only when it is not the current one. */
export function formatDayLong(iso: string, today: string) {
  const [y, m, d] = iso.split('-').map(Number)
  const base = `${MONTHS_LONG[m - 1]} ${ordinal(d)}`
  return iso.slice(0, 4) === today.slice(0, 4) ? base : `${base}, ${y}`
}
