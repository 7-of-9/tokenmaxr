import type { Provider } from './types'
import type { NormDay } from './usage'

export interface ArchivedModelCounts {
  prompts: number
  byModel: Record<string, number>
}

export interface ArchivedProviderCounts extends ArchivedModelCounts {
  byMachine: Record<string, ArchivedModelCounts>
  /** False when any archive machine could not be mapped uniquely to a public ID. */
  byMachineComplete: boolean
}

export interface PromptCountsResponse {
  source: 'archive'
  generatedAt: string
  total: number
  /** Archive records omitted because they could not be read. */
  undecryptable: number
  days: Array<{ date: string; providers: Partial<Record<Provider, ArchivedProviderCounts>> }>
}

const PROVIDERS: Provider[] = ['anthropic', 'openai', 'xai', 'cursor', 'google']
const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)
const isCount = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0

function invalid(): never {
  throw new Error('The prompt counts API returned an invalid response.')
}

function modelCounts(value: unknown): ArchivedModelCounts {
  if (!isRecord(value) || !isCount(value.prompts) || !isRecord(value.byModel)) return invalid()
  const byModel: Record<string, number> = Object.create(null)
  for (const [rawModel, count] of Object.entries(value.byModel)) {
    if (!isCount(count)) return invalid()
    const model = rawModel || 'unknown'
    byModel[model] = (byModel[model] ?? 0) + count
  }
  if (Object.values(byModel).reduce((sum, count) => sum + count, 0) !== value.prompts) return invalid()
  return { prompts: value.prompts, byModel }
}

/** Keep only public count fields, never archive records or other response fields. */
export function readPromptCounts(value: unknown): PromptCountsResponse {
  if (!isRecord(value) || value.source !== 'archive' || typeof value.generatedAt !== 'string'
    || !Number.isFinite(Date.parse(value.generatedAt)) || !isCount(value.total)
    || !isCount(value.undecryptable) || !Array.isArray(value.days)) return invalid()
  const days: PromptCountsResponse['days'] = []
  const seen = new Set<string>()
  let total = 0
  for (const day of value.days) {
    if (!isRecord(day) || typeof day.date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(day.date)
      || !Number.isFinite(Date.parse(day.date)) || new Date(day.date).toISOString().slice(0, 10) !== day.date
      || seen.has(day.date) || !isRecord(day.providers)) return invalid()
    seen.add(day.date)
    const providers: PromptCountsResponse['days'][number]['providers'] = {}
    for (const provider of PROVIDERS) {
      const value = day.providers[provider]
      if (value === undefined) continue
      const counts = modelCounts(value)
      if (!isRecord(value) || !isRecord(value.byMachine) || typeof value.byMachineComplete !== 'boolean') return invalid()
      const byMachine: ArchivedProviderCounts['byMachine'] = Object.create(null)
      for (const [machine, row] of Object.entries(value.byMachine)) byMachine[machine] = modelCounts(row)
      if (Object.values(byMachine).reduce((sum, row) => sum + row.prompts, 0) !== counts.prompts) return invalid()
      if (value.byMachineComplete && (byMachine.unknown?.prompts ?? 0) > 0) return invalid()
      providers[provider] = { ...counts, byMachine, byMachineComplete: value.byMachineComplete }
      total += counts.prompts
    }
    days.push({ date: day.date, providers })
  }
  if (total !== value.total) return invalid()
  return { source: 'archive', generatedAt: value.generatedAt, total, undecryptable: value.undecryptable,
    days: days.sort((a, b) => a.date.localeCompare(b.date)) }
}

/**
 * Archive model counts are a separate population from activity events. Overlay
 * them after machine filtering, keeping activity totals, token counts and dates.
 */
export function applyPromptCounts(days: NormDay[], counts: PromptCountsResponse | null, machine = 'all'): NormDay[] {
  if (!counts) return days
  const byDate = new Map(counts.days.map(day => [day.date, day.providers]))
  return days.map(day => {
    const providers = { ...day.providers }
    for (const provider of PROVIDERS) {
      const current = providers[provider]
      if (!current) continue
      const archived = byDate.get(day.date)?.[provider]
      // Unmapped archive machines could belong to the selected machine. Avoid
      // presenting a partial assignment as its complete per-model counts.
      const selected = machine === 'all' ? archived?.byModel ?? {}
        : archived && !archived.byMachineComplete ? undefined
          : archived?.byMachine[machine]?.byModel ?? {}
      providers[provider] = { ...current, promptsByModel: selected ? { ...selected } : undefined }
    }
    return { ...day, providers }
  })
}
