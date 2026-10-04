// Read-time normalisation for /agents (SPEC "Heatmap normalisation"). Nothing here is stored.
import type {
  Metric,
  InferenceDerivation,
  ModelBucket,
  PlaceBucket,
  Provider,
  ProviderDay,
  RollupDay,
  TokenBucket,
  UsageResponse,
  WorkspaceDay,
} from './types'

export const PROVIDERS: Provider[] = ['anthropic', 'openai', 'xai', 'cursor', 'google']

/** Google's provider is labelled by its CLI, the only Google source (SPEC "Gemini CLI"). */
export const PROVIDER_LABEL: Record<Provider, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  xai: 'xAI',
  cursor: 'Cursor',
  google: 'Gemini',
}

export const METRIC_LABEL: Record<Metric, string> = {
  tokens: 'Total',
  effective: 'Effective',
  output: 'Output',
}

/** What the unit reads as in sentences: "1.2B tokens", "40M effective tokens". */
export const METRIC_NOUN: Record<Metric, string> = {
  tokens: 'tokens',
  effective: 'effective tokens',
  output: 'output tokens',
}

export type ProviderFilter = Provider | 'all'

export interface ViewOptions {
  provider: ProviderFilter
  metric: Metric
  exactOnly: boolean
}

export const DEFAULT_VIEW: ViewOptions = { provider: 'all', metric: 'tokens', exactOnly: false }

// ---- Normalising the wire shape (tolerates v1 rollups, which lack models buckets and places) ----

const ZERO_BUCKET: TokenBucket = { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0, unattributed: 0 }

/** A display category, not a fabricated model or project. */
export const ACCOUNT_HISTORY = 'account-history'
export const modelLabel = (model: string) => model === ACCOUNT_HISTORY ? 'Account history' : model

function num(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function bucket(value: unknown): TokenBucket {
  if (!isRecord(value)) return ZERO_BUCKET
  return {
    unattributed: Math.max(0, num(value.unattributed)),
    in: num(value.in),
    cacheW: num(value.cacheW),
    cacheW1h: num(value.cacheW1h),
    cacheR: num(value.cacheR),
    out: num(value.out),
    reasoning: num(value.reasoning),
    calls: num(value.calls),
    events: num(value.events),
  }
}

function modelMap(value: unknown): Record<string, ModelBucket> {
  const out: Record<string, ModelBucket> = {}
  if (!isRecord(value)) return out
  for (const [model, raw] of Object.entries(value)) {
    // v1 stored a bare effective number per model; there is nothing to price in that.
    if (!isRecord(raw)) continue
    const b = bucket(raw)
    out[model || 'unknown'] = {
      in: b.in,
      cacheW: b.cacheW,
      cacheW1h: b.cacheW1h,
      cacheR: b.cacheR,
      out: b.out,
      reasoning: b.reasoning,
      calls: b.calls,
    }
    if (b.unattributed) {
      const history = out[ACCOUNT_HISTORY] ?? { ...ZERO_BUCKET }
      history.unattributed = (history.unattributed ?? 0) + b.unattributed
      out[ACCOUNT_HISTORY] = history
    }
  }
  for (const [model, b] of Object.entries(out)) if (metricOf(b, 'tokens') <= 0 && b.calls <= 0) delete out[model]
  return out
}

function placeMap(value: unknown): Record<string, PlaceBucket> {
  const out: Record<string, PlaceBucket> = {}
  if (!isRecord(value)) return out
  for (const [key, raw] of Object.entries(value)) {
    if (!isRecord(raw)) continue
    out[key] = { in: num(raw.in), cacheW: num(raw.cacheW), cacheR: num(raw.cacheR), out: num(raw.out), unattributed: Math.max(0, num(raw.unattributed)), prompts: num(raw.prompts), ...(typeof raw.promptsNoUsage === 'number' ? { promptsNoUsage: num(raw.promptsNoUsage) } : {}) }
  }
  return out
}

function providerDay(value: unknown, includeWorkspaces = true): ProviderDay | null {
  if (!isRecord(value)) return null
  const models = isRecord(value.models) ? value.models : {}
  return {
    exact: bucket(value.exact),
    estimated: bucket(value.estimated),
    prompts: num(value.prompts),
    promptsNoUsage: num(value.promptsNoUsage),
    ...(isRecord(value.promptsByModel) ? { promptsByModel: Object.fromEntries(Object.entries(value.promptsByModel)
      .filter(([, count]) => typeof count === 'number' && Number.isSafeInteger(count) && count >= 0)) as Record<string, number> } : {}),
    accts: Array.isArray(value.accts) ? value.accts.filter((a): a is string => typeof a === 'string') : [],
    models: { exact: modelMap(models.exact), estimated: modelMap(models.estimated) },
    byMachine: placeMap(value.byMachine),
    byCountry: placeMap(value.byCountry),
    ...(Array.isArray(value.accountUsage) ? { accountUsage: value.accountUsage.filter(isRecord).map(raw => ({
      source: typeof raw.source === 'string' ? raw.source : 'unknown', timezone: 'UTC' as const,
      totalTokens: num(raw.totalTokens), matchedTokens: num(raw.matchedTokens), uncertainTokens: num(raw.uncertainTokens),
      recoveredTokens: num(raw.recoveredTokens), observedAt: typeof raw.observedAt === 'string' ? raw.observedAt : '',
    })) } : {}),
    ...(includeWorkspaces && isRecord(value.byWorkspace) ? { byWorkspace: workspaceMap(value.byWorkspace) } : {}),
  }
}

function workspaceMap(value: Record<string, unknown>): Record<string, WorkspaceDay> {
  const out: Record<string, WorkspaceDay> = {}
  for (const [key, raw] of Object.entries(value)) {
    const p = providerDay(raw, false)
    if (p) out[key] = { exact: p.exact, estimated: p.estimated, prompts: p.prompts, promptsNoUsage: p.promptsNoUsage,
      promptsByModel: p.promptsByModel, models: p.models, byMachine: p.byMachine, byCountry: p.byCountry }
  }
  return out
}

export interface NormDay {
  date: string
  providers: Partial<Record<Provider, ProviderDay>>
}

/** Validated days, oldest first. */
export function normaliseDays(data: UsageResponse | null): NormDay[] {
  const days: NormDay[] = []
  for (const day of Array.isArray(data?.days) ? data!.days : []) {
    const raw = day as RollupDay | undefined
    if (!raw || typeof raw.date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(raw.date)) continue
    const providers: NormDay['providers'] = {}
    for (const provider of PROVIDERS) {
      const p = providerDay(raw.providers?.[provider])
      if (p) providers[provider] = p
    }
    days.push({ date: raw.date, providers })
  }
  return days.sort((a, b) => a.date.localeCompare(b.date))
}

// ---- Metrics ----

type Tokens = Pick<TokenBucket, 'in' | 'cacheW' | 'cacheR' | 'out' | 'unattributed'>

export function metricOf(b: Tokens, metric: Metric) {
  switch (metric) {
    case 'output':
      return b.out
    case 'effective':
      return b.in + b.cacheW + b.out + 0.1 * b.cacheR
    default:
      return b.in + b.cacheW + b.cacheR + b.out + (b.unattributed ?? 0)
  }
}

/** All input, cached included (the headline's "in"). */
export const inputOf = (b: Tokens) => b.in + b.cacheW + b.cacheR

const providersOf = (view: ViewOptions) => (view.provider === 'all' ? PROVIDERS : [view.provider])

/** Model prompt counts come from recorded prompt metadata, never model API calls. */
export function promptModelsOf(p: ProviderDay | WorkspaceDay): Record<string, number | null> {
  const counts: Record<string, number | null> = Object.create(null)
  for (const model of new Set([...Object.keys(p.models.exact), ...Object.keys(p.models.estimated)])) {
    counts[model] = model === ACCOUNT_HISTORY ? null : p.promptsByModel ? p.promptsByModel[model] ?? 0 : p.prompts === 0 ? 0 : null
  }
  if (p.promptsByModel) for (const [model, count] of Object.entries(p.promptsByModel)) counts[model || 'unknown'] = count
  return counts
}

// ---- Day cells ----

export interface ProviderCell {
  total: number
  inferred: number
  prompts: number
  promptsNoUsage: number
  projects: ProjectCell[]
  accountUsage?: ProviderDay['accountUsage']
}

export interface ProjectCell {
  workspace: string | null
  total: number
  inferred: number
  prompts: number
  promptsNoUsage: number
}

export type DayDerivation = InferenceDerivation & { rate: number; dailyEstimate: number }

export interface DayCell {
  derivations?: DayDerivation[]
  date: string
  /** Recorded tokens in the chosen metric (exact + estimated, or exact only). */
  total: number
  exact: number
  estimated: number
  /** The part of `estimated` inferred from unrecorded prompts ("Estimate unrecorded"); a subset, never added. */
  inferred: number
  /** Raw input (cached included) and output, for the headline split. */
  tokensIn: number
  tokensOut: number
  /** Account-history tokens with no known input/output split. */
  unattributed: number
  prompts: number
  /** Prompts whose tokens were never recorded (an outlined empty cell when nothing was). */
  promptsNoUsage: number
  byProvider: Partial<Record<Provider, ProviderCell>>
  /** [model, value in the chosen metric, provider], largest first. */
  models: Array<[string, number, Provider]>
  /** Keyed by provider|model; null means unavailable, not zero. */
  modelPrompts?: Record<string, number | null>
  /** Token coverage before applying the displayed metric. */
  recordedTokenDay?: boolean
  accts: string[]
}

/** One cell per day that has any tokens or prompts, keyed by local date. */
export function buildCells(days: NormDay[], view: ViewOptions): Map<string, DayCell> {
  const cells = new Map<string, DayCell>()
  for (const day of days) {
    const cell: DayCell = {
      date: day.date,
      total: 0,
      exact: 0,
      estimated: 0,
      inferred: 0,
      tokensIn: 0,
      tokensOut: 0,
      unattributed: 0,
      prompts: 0,
      promptsNoUsage: 0,
      byProvider: {},
      models: [],
      modelPrompts: {},
      recordedTokenDay: false,
      accts: [],
    }
    for (const provider of providersOf(view)) {
      const p = day.providers[provider]
      if (!p) continue
      const exact = metricOf(p.exact, view.metric)
      const recordedTokens = metricOf(p.exact, 'tokens') + (view.exactOnly ? 0
        : metricOf(p.estimated, 'tokens') - metricOf(p.inferred ?? ZERO_BUCKET, 'tokens'))
      if (recordedTokens > 0) cell.recordedTokenDay = true
      const estimated = view.exactOnly ? 0 : metricOf(p.estimated, view.metric)
      const inferred = view.exactOnly ? 0 : metricOf(p.inferred ?? ZERO_BUCKET, view.metric)
      const workspaces: Array<[string | null, WorkspaceDay]> = Object.entries(p.byWorkspace ?? {})
      // Older rollups have no project split: preserve their totals as unassigned.
      if (!workspaces.length) workspaces.push([null, p])
      const projects = workspaces.map(([workspace, group]): ProjectCell => ({
        workspace,
        total: metricOf(group.exact, view.metric) + (view.exactOnly ? 0 : metricOf(group.estimated, view.metric))
          - (view.metric === 'tokens' ? (group.exact.unattributed ?? 0) + (view.exactOnly ? 0 : group.estimated.unattributed ?? 0) : 0),
        inferred: view.exactOnly ? 0 : metricOf(group.inferred ?? ZERO_BUCKET, view.metric),
        prompts: group.prompts,
        promptsNoUsage: group.promptsNoUsage,
      })).filter(project => project.total > 0 || project.prompts > 0).sort((a, b) => b.total - a.total)
      const unattributed = view.metric === 'tokens' ? (p.exact.unattributed ?? 0) + (view.exactOnly ? 0 : p.estimated.unattributed ?? 0) : 0
      if (unattributed > 0) projects.push({ workspace: ACCOUNT_HISTORY, total: unattributed, inferred: 0, prompts: 0, promptsNoUsage: 0 })
      cell.byProvider[provider] = { total: exact + estimated, inferred, prompts: p.prompts, promptsNoUsage: p.promptsNoUsage, projects, accountUsage: p.accountUsage }
      cell.exact += exact
      cell.estimated += estimated
      cell.inferred += inferred
      if (!view.exactOnly && p.derivations?.length) {
        cell.derivations = [...(cell.derivations ?? []), ...p.derivations.map(d => {
          const rate = metricOf(d.perPrompt, view.metric)
          return { ...d, rate, dailyEstimate: rate * d.missingInMonth / d.affectedDays }
        })]
      }
      cell.tokensIn += inputOf(p.exact) + (view.exactOnly ? 0 : inputOf(p.estimated))
      cell.tokensOut += p.exact.out + (view.exactOnly ? 0 : p.estimated.out)
      cell.unattributed += unattributed
      cell.prompts += p.prompts
      cell.promptsNoUsage += p.promptsNoUsage
      cell.accts.push(...p.accts)
      const perModel = new Map<string, number>()
      const tiers = view.exactOnly ? [p.models.exact] : [p.models.exact, p.models.estimated]
      for (const tier of tiers) {
        for (const [model, b] of Object.entries(tier)) perModel.set(model, (perModel.get(model) ?? 0) + metricOf(b, view.metric))
      }
      const promptModels = promptModelsOf(p)
      for (const [model, count] of Object.entries(promptModels)) {
        cell.modelPrompts![`${provider}|${model}`] = count
        if ((count ?? 0) > 0 && !perModel.has(model)) perModel.set(model, 0)
      }
      for (const [model, value] of perModel) if (value > 0 || (promptModels[model] ?? 0) > 0) cell.models.push([model, value, provider])
    }
    cell.total = cell.exact + cell.estimated
    cell.models.sort((a, b) => b[1] - a[1])
    if (cell.total > 0 || cell.prompts > 0) cells.set(day.date, cell)
  }
  return cells
}

/** Mostly inferred from unrecorded prompts: drawn hollow, at its level. */
export const isInferredDominant = (cell: DayCell) => cell.total > 0 && cell.inferred * 2 > cell.total

/** Mostly estimated: the day keeps its level, and its tooltip says so. */
export const isEstimatedDominant = (cell: DayCell) => cell.total > 0 && cell.estimated > cell.exact

// ---- Dates (local calendar dates as YYYY-MM-DD, arithmetic in UTC to dodge DST) ----

const DAY_MS = 86_400_000

export function toIsoDate(date: Date) {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

export function isoToUtcMs(iso: string) {
  const [y, m, d] = iso.split('-').map(Number)
  return Date.UTC(y, m - 1, d)
}

export function utcMsToIso(ms: number) {
  return new Date(ms).toISOString().slice(0, 10)
}

export function addDays(iso: string, days: number) {
  return utcMsToIso(isoToUtcMs(iso) + days * DAY_MS)
}

/** 0 = Monday … 6 = Sunday. */
export function weekdayIndex(iso: string) {
  return (new Date(isoToUtcMs(iso)).getUTCDay() + 6) % 7
}

export function mondayOf(iso: string) {
  return addDays(iso, -weekdayIndex(iso))
}

export function nextMonth(iso: string) {
  const [y, m] = iso.split('-').map(Number)
  return utcMsToIso(Date.UTC(y, m, 1))
}

const MONTHS_SHORT = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

export function monthShort(monthIndex: number) {
  return MONTHS_SHORT[monthIndex]
}

/** "Sep 28", with the year when it is not the current one: "Dec 3, 2025". */
export function formatDayShort(iso: string, today: string) {
  const [y, m, d] = iso.split('-').map(Number)
  const base = `${MONTHS_SHORT[m - 1]} ${d}`
  return iso.slice(0, 4) === today.slice(0, 4) ? base : `${base}, ${y}`
}

export function formatMonth(month: string) {
  const [y, m] = month.split('-').map(Number)
  return `${MONTHS_SHORT[m - 1]} ${y}`
}

// ---- Periods (GitHub's year selector) ----

export type RollingPeriod = '30d' | '60d' | '90d'
export type Period = 'recent' | RollingPeriod | `${number}`

export const isRollingPeriod = (period: string): period is RollingPeriod => ['30d', '60d', '90d'].includes(period)

export interface PeriodRange {
  from: string
  to: string
  /** Completes "N tokens in …": "the last 30 days", "the last year" or "2025". */
  phrase: string
  /** The period's length in months, for per-month figures: 12 for the last 12 months, else its days / 30.44. */
  months: number
}

/** Average days in a month (365.25 / 12). */
export const DAYS_PER_MONTH = 30.44

/** Days from `from` to `to`, both included, over the average month. A current year counts only the days so far. */
export const monthsBetween = (from: string, to: string) => ((isoToUtcMs(to) - isoToUtcMs(from)) / DAY_MS + 1) / DAYS_PER_MONTH

export function periodRange(period: Period, today: string): PeriodRange {
  if (isRollingPeriod(period)) {
    const count = parseInt(period, 10)
    return { from: addDays(today, 1 - count), to: today, phrase: `the last ${count} days`, months: count / DAYS_PER_MONTH }
  }
  if (period === 'recent') return { from: addDays(mondayOf(today), -52 * 7), to: today, phrase: 'the last year', months: 12 }
  const end = `${period}-12-31`
  const to = end > today ? today : end
  const from = `${period}-01-01`
  return { from, to, phrase: period, months: monthsBetween(from, to) }
}

/** A period total per month (the cost tile's headline figure). */
export const perMonth = (total: number, range: Pick<PeriodRange, 'months'>) => (range.months > 0 ? total / range.months : 0)

/** Recorded-token days in the selected scope, independent of the displayed token metric. */
export function activeDayCount(days: NormDay[], view: ViewOptions, from: string, to: string): number {
  const active = new Set<string>()
  for (const day of days) {
    if (day.date < from || day.date > to) continue
    for (const provider of providersOf(view)) {
      const p = day.providers[provider]
      if (!p) continue
      const recorded = metricOf(p.exact, 'tokens') + (view.exactOnly ? 0
        : metricOf(p.estimated, 'tokens') - metricOf(p.inferred ?? ZERO_BUCKET, 'tokens'))
      if (recorded > 0) {
        active.add(day.date)
        break
      }
    }
  }
  return active.size
}

export const perActiveDay = (total: number, activeDays: number) => activeDays > 0 ? total / activeDays : 0

/** Calendar years from the current one back to the first data. */
export function yearsBack(firstDate: string | null | undefined, today: string) {
  const first = Number((firstDate || today).slice(0, 4))
  const years: Period[] = []
  for (let year = Number(today.slice(0, 4)); year >= first; year -= 1) years.push(String(year) as Period)
  return years
}

// ---- Aggregates ----

export interface PeriodSummary {
  total: number
  prompts: number
  /** Prompts on recorded-token days only, for the matching active-day average. */
  activeDayPrompts: number
  tokensIn: number
  tokensOut: number
  unattributed: number
  /** Average per month over months with recorded tokens. */
  perMonth: number
  accounts: number
  providers: number
}

export function summarise(cells: Map<string, DayCell>, from: string, to: string): PeriodSummary {
  let total = 0
  let prompts = 0
  let activeDayPrompts = 0
  let tokensIn = 0
  let tokensOut = 0
  let unattributed = 0
  const months = new Set<string>()
  const accts = new Set<string>()
  const providers = new Set<Provider>()
  for (const cell of cells.values()) {
    if (cell.date < from || cell.date > to) continue
    total += cell.total
    prompts += cell.prompts
    if (cell.recordedTokenDay ?? cell.total - cell.inferred > 0) activeDayPrompts += cell.prompts
    tokensIn += cell.tokensIn
    tokensOut += cell.tokensOut
    unattributed += cell.unattributed ?? 0
    if (cell.total > 0) months.add(cell.date.slice(0, 7))
    for (const acct of cell.accts) accts.add(acct)
    for (const provider of PROVIDERS) if (cell.byProvider[provider]) providers.add(provider)
  }
  return {
    total,
    tokensIn,
    tokensOut,
    unattributed,
    perMonth: months.size ? total / months.size : 0,
    prompts,
    activeDayPrompts,
    accounts: accts.size,
    providers: providers.size,
  }
}

export type Granularity = 'day' | 'week' | 'month'

export interface SeriesBucket {
  /** First day of the bucket (a Monday for weeks, the 1st for months). */
  start: string
  end: string
  total: number
  byProvider: Record<Provider, number>
}

/** Buckets covering [from, to] with recorded tokens per provider (activity-only days add nothing). */
export function timeSeries(cells: Map<string, DayCell>, from: string, to: string, granularity: Granularity): SeriesBucket[] {
  const buckets: SeriesBucket[] = []
  let cursor = granularity === 'week' ? mondayOf(from) : granularity === 'month' ? `${from.slice(0, 7)}-01` : from
  while (cursor <= to) {
    const next = granularity === 'day' ? addDays(cursor, 1) : granularity === 'week' ? addDays(cursor, 7) : nextMonth(cursor)
    const end = addDays(next, -1)
    buckets.push({ start: cursor, end: end > to ? to : end, total: 0, byProvider: { anthropic: 0, openai: 0, xai: 0, cursor: 0, google: 0 } })
    cursor = next
  }
  const keyOf = (date: string) =>
    granularity === 'day' ? date : granularity === 'week' ? mondayOf(date) : `${date.slice(0, 7)}-01`
  const index = new Map(buckets.map((b) => [b.start, b]))
  for (const cell of cells.values()) {
    if (cell.date < from || cell.date > to) continue
    const b = index.get(keyOf(cell.date))
    if (!b) continue
    for (const provider of PROVIDERS) {
      const value = cell.byProvider[provider]?.total ?? 0
      b.byProvider[provider] += value
      b.total += value
    }
  }
  return buckets
}

const EMPTY_MODEL: ModelBucket = { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, unattributed: 0 }

export interface ModelTotal {
  model: string
  provider: Provider
  tokens: ModelBucket
  /** The same tokens per local day, oldest first, so each day can be priced at its own list price. */
  byDay: Array<{ date: string; tokens: ModelBucket }>
}

const addInto = (into: ModelBucket, b: ModelBucket) => {
  for (const field of Object.keys(EMPTY_MODEL) as Array<keyof ModelBucket>) into[field] = (into[field] ?? 0) + (b[field] ?? 0)
}

/** One day's model buckets in the view (exact, plus estimated unless exact only), summed per provider and model. */
export function dayModels(day: NormDay, view: ViewOptions): Array<{ model: string; provider: Provider; tokens: ModelBucket }> {
  const out = new Map<string, { model: string; provider: Provider; tokens: ModelBucket }>()
  for (const provider of providersOf(view)) {
    const p = day.providers[provider]
    if (!p) continue
    const tiers = view.exactOnly ? [p.models.exact] : [p.models.exact, p.models.estimated]
    for (const tier of tiers) {
      for (const [model, b] of Object.entries(tier)) {
        const key = `${provider}|${model}`
        const entry = out.get(key) ?? { model, provider, tokens: { ...EMPTY_MODEL } }
        addInto(entry.tokens, b)
        out.set(key, entry)
      }
    }
  }
  return [...out.values()]
}

/** Summed model buckets for pricing: raw token fields, whatever the metric, with the per-day split kept. */
export function modelTotals(days: NormDay[], view: ViewOptions, from: string, to: string): ModelTotal[] {
  const totals = new Map<string, ModelTotal>()
  for (const day of days) {
    if (day.date < from || day.date > to) continue
    for (const { model, provider, tokens } of dayModels(day, view)) {
      const key = `${provider}|${model}`
      const entry = totals.get(key) ?? { model, provider, tokens: { ...EMPTY_MODEL }, byDay: [] }
      addInto(entry.tokens, tokens)
      entry.byDay.push({ date: day.date, tokens })
      totals.set(key, entry)
    }
  }
  return [...totals.values()].filter((m) => metricOf(m.tokens, 'tokens') > 0)
}

export interface PlaceTotal {
  key: string
  value: number
  prompts: number
}

/** Tokens per machine or per country in the chosen metric (these buckets combine exact and estimated). */
export function placeTotals(
  days: NormDay[],
  view: ViewOptions,
  from: string,
  to: string,
  field: 'byMachine' | 'byCountry',
): PlaceTotal[] {
  const totals = new Map<string, PlaceTotal>()
  for (const day of days) {
    if (day.date < from || day.date > to) continue
    for (const provider of providersOf(view)) {
      const p = day.providers[provider]
      if (!p) continue
      for (const [key, b] of Object.entries(p[field])) {
        const id = field === 'byCountry' ? (key || 'ZZ').toUpperCase() : key
        const entry = totals.get(id) ?? { key: id, value: 0, prompts: 0 }
        entry.value += metricOf(b, view.metric)
        entry.prompts += b.prompts
        totals.set(id, entry)
      }
    }
  }
  return [...totals.values()].filter((t) => t.value > 0 || t.prompts > 0).sort((a, b) => b.value - a.value || b.prompts - a.prompts)
}

// ---- Formatting ----

export function formatCompact(value: number) {
  if (!Number.isFinite(value) || value <= 0) return '0'
  const units: Array<[number, string]> = [
    [1e12, 'T'],
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'K'],
  ]
  for (const [size, suffix] of units) {
    // 0.9995 so 999,960 reads 1M rather than 1000K.
    if (value >= size * 0.9995) {
      const scaled = value / size
      // Three significant figures up to 100 (41.7B, 20.6B), so rounded parts visibly add up to their total.
      const digits = scaled < 100 ? 1 : 0
      return `${scaled.toFixed(digits).replace(/\.0$/, '')}${suffix}`
    }
  }
  return String(Math.round(value))
}

/** Counts read like the headline: plain below 10,000, then K / M / B (12.3M, 45.3B). */
export function formatCount(value: number) {
  return Math.abs(value) < 10_000 ? Math.round(value).toLocaleString('en-GB') : formatCompact(value)
}

export function plural(count: number, one: string, many = `${one}s`) {
  return `${formatCount(count)} ${count === 1 ? one : many}`
}
