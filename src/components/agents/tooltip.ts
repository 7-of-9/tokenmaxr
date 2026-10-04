import { ACCOUNT_HISTORY, monthShort, type DayDerivation } from './usage.ts'

export const projectLabel = (workspace: string | null) => workspace === ACCOUNT_HISTORY ? 'Account history' : workspace && workspace !== 'unknown'
  ? workspace.replace(/^workspace/, 'Project ')
  : 'Project unknown'

/** Human dates for a rate's sample, including ranges that cross a year. */
export function samplePeriod(from?: string, to = from) {
  if (!from || !to) return ''
  const month = (value: string) => monthShort(Number(value.slice(5, 7)) - 1)
  if (from === to) return `${month(from)} ${from.slice(0, 4)}`
  if (from.slice(0, 4) === to.slice(0, 4)) return `${month(from)}–${month(to)} ${to.slice(0, 4)}`
  return `${month(from)} ${from.slice(0, 4)}–${month(to)} ${to.slice(0, 4)}`
}

/** Provider fallbacks share a sample; workspace samples remain separately identifiable. */
export function averageSources(derivations: DayDerivation[]) {
  const unique = new Map<string, DayDerivation>()
  for (const d of derivations) {
    const key = JSON.stringify([d.provider, d.basis, d.basis.startsWith('workspace') ? d.workspace : null,
      d.sampleFrom, d.sampleTo, d.samplePrompts, d.rate])
    if (!unique.has(key)) unique.set(key, d)
  }
  return [...unique.values()]
}
