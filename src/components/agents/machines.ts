import type { ModelBucket, PlaceBucket, Provider, ProviderDay, TokenBucket, WorkspaceDay } from './types'
import type { NormDay } from './usage'

const empty = (): TokenBucket => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 })
const add = <T extends object>(into: T, from: T) => {
  for (const key of Object.keys(from) as Array<keyof T>) {
    if (typeof from[key] === 'number') into[key] = ((Number(into[key]) || 0) + Number(from[key])) as T[keyof T]
  }
}

/** Production already supplies workspace/model/quality splits with reporter IDs.
 * Keep those splits only when the workspace/day has one reporter; a shared
 * workspace keeps its exact machine totals but leaves its model unassigned.
 */
export function machineDays(days: NormDay[], machine: string): { days: NormDay[]; partial: boolean } {
  if (machine === 'all') return { days, partial: false }
  let partial = false
  const filtered = days.map(day => {
    const providers: NormDay['providers'] = {}
    for (const [provider, p] of Object.entries(day.providers) as Array<[Provider, ProviderDay]>) {
      const place = p.byMachine[machine]
      if (!place) continue
      if (Object.keys(p.byMachine).length === 1) { providers[provider] = p; continue }
      const out: ProviderDay = { exact: empty(), estimated: empty(), prompts: place.prompts, promptsNoUsage: 0,
        models: { exact: {}, estimated: {} }, byMachine: { [machine]: { ...place } }, byCountry: {}, byWorkspace: {}, accts: [] }
      const groups = Object.entries(p.byWorkspace ?? {})
      if (!groups.length) groups.push(['unknown', p])
      for (const [ws, group] of groups) {
        const split = group.byMachine[machine]
        if (!split) continue
        let scoped: WorkspaceDay
        if (Object.keys(group.byMachine).length === 1) scoped = group
        else {
          partial = true
          const tokens = { ...empty(), in: split.in, cacheW: split.cacheW, cacheR: split.cacheR, out: split.out }
          scoped = { exact: tokens, estimated: empty(), prompts: split.prompts, promptsNoUsage: split.promptsNoUsage ?? 0,
            models: { exact: { unknown: tokens }, estimated: {} }, byMachine: { [machine]: { ...split } }, byCountry: { ZZ: { ...split } } }
        }
        out.byWorkspace![ws] = scoped
        out.promptsNoUsage += scoped.promptsNoUsage
        for (const tier of ['exact', 'estimated'] as const) {
          add(out[tier], scoped[tier])
          for (const [name, b] of Object.entries(scoped.models[tier])) {
            const dest = out.models[tier][name] ?? { ...empty() }
            add<ModelBucket>(dest, b)
            out.models[tier][name] = dest
          }
        }
        for (const [cc, b] of Object.entries(scoped.byCountry)) {
          const dest = out.byCountry[cc] ?? { in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 }
          add<PlaceBucket>(dest, b)
          out.byCountry[cc] = dest
        }
      }
      providers[provider] = out
    }
    return { ...day, providers }
  })
  return { days: filtered, partial }
}
