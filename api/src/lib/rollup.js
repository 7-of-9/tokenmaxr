// Day rollups (SPEC "Rollup data", v3). Every figure is an integer sum of
// disjoint token buckets, so byMachine, byCountry and models always add up to
// the provider totals exactly.
import { TOKEN_FIELDS } from './merge.js'
import { PROVIDERS } from './validate.js'

export const ROLLUP_VERSION = 3

// Keys for events without a reporter (rows written before v1.1) or country.
export const UNKNOWN_MACHINE = 'unknown'
export const UNKNOWN_COUNTRY = 'ZZ'
export const UNKNOWN_MODEL = 'unknown'

const TIERS = ['exact', 'estimated']
// byMachine / byCountry carry the four summed buckets plus a prompt count.
const SPLIT_FIELDS = ['in', 'cacheW', 'cacheR', 'out']

export const emptyBucket = () => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 })

const emptyModel = () => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0 })

const emptySplit = () => ({ in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 })

const sortedObject = (obj) => Object.fromEntries(Object.keys(obj).sort().map((k) => [k, obj[k]]))

const slot = (obj, key, make) => obj[key] ?? (obj[key] = make())

const emptyGroup = () => ({
  exact: emptyBucket(), estimated: emptyBucket(), prompts: 0, promptsNoUsage: 0,
  models: { exact: {}, estimated: {} }, byMachine: {}, byCountry: {},
})

function addEvent(p, ev, workspace = false) {
  const makePlace = () => workspace ? { ...emptySplit(), promptsNoUsage: 0 } : emptySplit()
  const machine = slot(p.byMachine, ev.machine || UNKNOWN_MACHINE, makePlace)
  const country = slot(p.byCountry, ev.cc || UNKNOWN_COUNTRY, makePlace)
  if (ev.kind === 'activity') {
    p.prompts++
    if (!ev.hasUsage) {
      p.promptsNoUsage++
      if (workspace) { machine.promptsNoUsage++; country.promptsNoUsage++ }
    }
    machine.prompts++
    country.prompts++
    return
  }
  const tier = ev.q === 'estimated' ? 'estimated' : 'exact'
  const model = slot(p.models[tier], ev.model || UNKNOWN_MODEL, emptyModel)
  for (const f of TOKEN_FIELDS) {
    p[tier][f] += ev[f] || 0
    model[f] += ev[f] || 0
  }
  p[tier].events++
  for (const f of SPLIT_FIELDS) {
    machine[f] += ev[f] || 0
    country[f] += ev[f] || 0
  }
}

const finishGroup = (p) => ({
  ...p,
  models: Object.fromEntries(TIERS.map((t) => [t, sortedObject(p.models[t])])),
  byMachine: sortedObject(p.byMachine), byCountry: sortedObject(p.byCountry),
})

// Workspace hashes are storage-only. The public response substitutes anonymous aliases.
export function computeRollup(date, events) {
  const acc = {}
  for (const ev of events) {
    if (!PROVIDERS.includes(ev.provider)) continue
    const p = slot(acc, ev.provider, () => ({ ...emptyGroup(), accts: new Set(), byWorkspace: {} }))
    if (ev.acct) p.accts.add(ev.acct)
    addEvent(p, ev)
    addEvent(slot(p.byWorkspace, ev.ws || 'unknown', emptyGroup), ev, true)
  }
  const providers = {}
  for (const name of Object.keys(acc).sort()) {
    const p = acc[name]
    providers[name] = {
      ...finishGroup(p), accts: [...p.accts].sort(),
      byWorkspace: Object.fromEntries(Object.keys(p.byWorkspace).sort().map((ws) => [ws, finishGroup(p.byWorkspace[ws])])),
    }
  }
  return { v: ROLLUP_VERSION, date, providers }
}

export const isCurrentRollup = (data) => data?.v === ROLLUP_VERSION

// Serves a v1 rollup (whose models were bare effective numbers) in the v2
// shape until it is recomputed: `v` stays 1, and the model, machine and
// country splits are empty rather than wrong.
export function legacyShape(day) {
  if (isCurrentRollup(day) || day.v === 2) return day
  const providers = {}
  for (const [name, p] of Object.entries(day.providers || {})) {
    providers[name] = {
      exact: { ...emptyBucket(), ...p.exact },
      estimated: { ...emptyBucket(), ...p.estimated },
      prompts: p.prompts || 0,
      promptsNoUsage: p.promptsNoUsage || 0,
      accts: p.accts || [],
      models: { exact: {}, estimated: {} },
      byMachine: {},
      byCountry: {},
    }
  }
  return { v: 1, date: day.date, providers }
}

// The public endpoint never exposes account hashes (SPEC "Read API"): each
// hash becomes a per-response alias, stable across days, so the page can still
// count distinct accounts over any range. Workspace tags also become aliases
// shared across providers and days, without publishing paths or persistent hashes.
export function aliasAccts(days) {
  const workspaces = new Set()
  for (const d of days) for (const p of Object.values(d.providers || {})) {
    for (const ws of Object.keys(p.byWorkspace || {})) if (ws !== 'unknown') workspaces.add(ws)
  }
  const workspaceAliases = new Map([...workspaces].sort().map((ws, i) => [ws, `workspace${i + 1}`]))
  const all = new Set()
  for (const d of days) for (const p of Object.values(d.providers || {})) for (const a of p.accts || []) all.add(a)
  const alias = new Map([...all].sort().map((a, i) => [a, `acct${i + 1}`]))
  return days.map(({ accountLedger: _privateLedger, ...d }) => ({
    ...d,
    providers: Object.fromEntries(
      Object.entries(d.providers || {}).map(([name, p]) => [name, {
        ...p, accts: (p.accts || []).map((a) => alias.get(a)),
        ...(p.byWorkspace ? { byWorkspace: Object.fromEntries(Object.entries(p.byWorkspace).map(([ws, group]) => [workspaceAliases.get(ws) || 'unknown', group])) } : {}),
      }]),
    ),
  }))
}

export const hasData = (day) => Object.keys(day.providers || {}).length > 0
