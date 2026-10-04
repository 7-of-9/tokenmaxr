// Deterministic synthetic data for /tokens?mock=1 and /tokens/prompts?mock=1. No network.
import type {
  AcctQ,
  ModelBucket,
  PlaceBucket,
  PromptFacets,
  PromptItem,
  PromptsResponse,
  Provider,
  ProviderDay,
  PublicMachine,
  RollupDay,
  TokenBucket,
  UsageResponse,
} from './types'
import { addDays, isoToUtcMs, PROVIDERS, toIsoDate } from './usage.ts'

function mulberry32(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function hashString(value: string) {
  let h = 2166136261
  for (let i = 0; i < value.length; i += 1) {
    h ^= value.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

const EMPTY: TokenBucket = { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 }

interface MockMachine extends PublicMachine {
  /** Where this machine's tokens come from, as [from, to] month-day windows; the rest go home. */
  trips?: Array<[string, string]>
}

const MACHINES: MockMachine[] = [
  { id: 'm_mock_studio', label: 'Studio Mac', cc: 'TH', os: 'darwin', live: true, lastSeenAt: null },
  { id: 'm_mock_desk', label: 'desktop-win', cc: 'TH', os: 'windows', live: true, lastSeenAt: null },
  { id: 'm_mock_ldn', label: 'London MBP', cc: 'GB', os: 'darwin', live: false, lastSeenAt: null, trips: [['07-05', '07-24']] },
  { id: 'm_mock_sg', label: 'sg-devbox', cc: 'SG', os: 'windows', live: false, lastSeenAt: null, trips: [['08-01', '08-14']] },
  { id: 'm_mock_air', label: 'Travel Air', cc: 'US', os: 'darwin', live: false, lastSeenAt: null },
]

/** Model mix per provider: [id, weight, first month-day it appears, last month-day]. Real ids from modelPricing.json plus one unpriced. */
const MODEL_MIX: Record<Provider, Array<[string, number, string, string]>> = {
  anthropic: [
    ['claude-opus-5-5', 0.58, '07-01', '12-31'],
    ['claude-opus-5', 0.5, '01-01', '07-10'],
    ['claude-sonnet-5', 0.16, '01-01', '12-31'],
    ['claude-fable-5-1', 0.07, '08-01', '12-31'],
    ['claude-haiku-4-5', 0.06, '01-01', '12-31'],
    ['claude-opus-4-8', 0.12, '01-01', '06-20'],
    ['unknown', 0.004, '01-01', '12-31'],
  ],
  openai: [
    ['gpt-5.5', 0.4, '01-01', '12-31'],
    ['gpt-5.3-codex', 0.34, '01-01', '08-15'],
    ['gpt-6-sol', 0.3, '08-01', '12-31'],
    ['gpt-5.6-terra', 0.14, '06-01', '12-31'],
    ['gpt-5.4-mini', 0.06, '01-01', '12-31'],
  ],
  xai: [
    ['grok-4.7', 0.5, '01-01', '12-31'],
    ['grok-build-0.1', 0.3, '01-01', '12-31'],
    ['grok-4.7-fast', 0.2, '01-01', '12-31'],
  ],
  // Cursor's own catalogue ids (the IDE names the underlying model); priced by family fallback where one exists.
  cursor: [
    ['claude-4.5-sonnet-thinking', 0.42, '01-01', '12-31'],
    ['claude-4.5-sonnet', 0.18, '01-01', '12-31'],
    ['gemini-3-pro', 0.14, '01-01', '12-31'],
    ['gpt-5.1-codex-max-xhigh', 0.1, '01-01', '12-31'],
    ['claude-4.5-opus-high-thinking', 0.08, '01-01', '12-31'],
    ['grok-code-fast-1', 0.05, '01-01', '12-31'],
    ['gemini-3-flash', 0.03, '01-01', '12-31'],
  ],
  // Gemini CLI: the models its session files record (SPEC "Gemini CLI"); both have verified list prices.
  google: [
    ['gemini-2.5-pro', 0.7, '01-01', '12-31'],
    ['gemini-2.5-flash', 0.3, '01-01', '12-31'],
  ],
}

const ACCTS: Record<Provider, string[]> = {
  anthropic: ['acct1', 'acct2'],
  openai: ['acct3', 'acct4'],
  xai: ['acct5'],
  cursor: ['acct6'],
  google: ['acct7'],
}

/** A day of calls whose raw size is roughly `scale` tokens; cache reads dominate, as with real agents. */
function tokens(rand: () => number, provider: Provider, scale: number): TokenBucket {
  const calls = Math.max(1, Math.round(scale / 380_000))
  // Cursor reports only inputTokens/outputTokens per message: no cache split (SPEC "Cursor").
  if (provider === 'cursor') {
    const out = Math.round(scale * (0.006 + rand() * 0.006))
    return { in: Math.max(0, scale - out), cacheW: 0, cacheW1h: 0, cacheR: 0, out, reasoning: 0, calls, events: calls }
  }
  const readShare = provider === 'anthropic' ? 0.87 + rand() * 0.06 : provider === 'openai' ? 0.72 + rand() * 0.12 : 0.62 + rand() * 0.16
  const cacheR = Math.round(scale * readShare)
  // Neither OpenAI nor Gemini charges or reports cache writes separately (Gemini's `cached` is a read count).
  const cacheW = provider === 'openai' || provider === 'google' ? 0 : Math.round(scale * (0.03 + rand() * 0.05))
  const out = Math.round(scale * (provider === 'anthropic' ? 0.003 + rand() * 0.004 : 0.008 + rand() * 0.012))
  const input = Math.max(0, scale - cacheR - cacheW - out)
  const cacheW1h = provider === 'anthropic' ? Math.round(cacheW * (0.35 + rand() * 0.4)) : 0
  return { in: input, cacheW, cacheW1h, cacheR, out, reasoning: Math.round(out * 0.4), calls, events: calls }
}

const FIELDS = ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'calls'] as const

function splitModels(rand: () => number, provider: Provider, date: string, b: TokenBucket) {
  const md = date.slice(5)
  const mix = MODEL_MIX[provider]
    .filter(([, , from, to]) => md >= from && md <= to)
    .map(([id, weight]) => [id, weight * (0.6 + rand() * 0.8)] as const)
  const sum = mix.reduce((s, [, w]) => s + w, 0)
  const models: Record<string, ModelBucket> = {}
  const left = { ...b }
  mix.forEach(([id, weight], index) => {
    const share = weight / sum
    const m = {} as ModelBucket
    for (const field of FIELDS) {
      m[field] = index === mix.length - 1 ? left[field] : Math.round(b[field] * share)
      left[field] -= m[field]
    }
    models[id] = m
  })
  return models
}

function place(b: Pick<TokenBucket, 'in' | 'cacheW' | 'cacheR' | 'out'>, prompts: number, share: number): PlaceBucket {
  return {
    in: Math.round(b.in * share),
    cacheW: Math.round(b.cacheW * share),
    cacheR: Math.round(b.cacheR * share),
    out: Math.round(b.out * share),
    prompts: Math.round(prompts * share),
  }
}

function addPlace(target: Record<string, PlaceBucket>, key: string, value: PlaceBucket) {
  const t = target[key] ?? { in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 }
  target[key] = { in: t.in + value.in, cacheW: t.cacheW + value.cacheW, cacheR: t.cacheR + value.cacheR, out: t.out + value.out, prompts: t.prompts + value.prompts }
}

/** Which machines worked on a date, with their shares. */
function machinesFor(date: string, rand: () => number, year: number): Array<[MockMachine, number]> {
  if (Number(date.slice(0, 4)) < year) return [[MACHINES[4], 1]]
  const md = date.slice(5)
  const away = MACHINES.find((m) => m.trips?.some(([from, to]) => md >= from && md <= to))
  if (away) return [[away, 1]]
  const studio = 0.45 + rand() * 0.3
  return [
    [MACHINES[0], studio],
    [MACHINES[1], 1 - studio],
  ]
}

function mockDay(date: string, year: number): RollupDay | null {
  const rand = mulberry32(hashString(`d0m1-agents-v2|${date}`))
  const weekday = new Date(isoToUtcMs(date)).getUTCDay()
  const weekend = weekday === 0 || weekday === 6
  const md = date.slice(5)
  const thisYear = Number(date.slice(0, 4)) === year
  const providers: Partial<Record<Provider, ProviderDay>> = {}
  const machines = machinesFor(date, rand, year)

  // Quiet stretches: a June holiday and a week off in August.
  if (thisYear && ((md >= '06-14' && md <= '06-24') || (md >= '08-18' && md <= '08-23'))) return null
  if (rand() < (weekend ? 0.45 : 0.06)) return null

  const add = (provider: Provider, scale: number, opts: { estimated?: boolean; noUsage?: number } = {}) => {
    const b = tokens(rand, provider, Math.round(scale))
    const prompts = Math.max(1, Math.round(scale / (1.8e6 + rand() * 2.5e6))) + (opts.noUsage ?? 0)
    const byMachine: Record<string, PlaceBucket> = {}
    const byCountry: Record<string, PlaceBucket> = {}
    for (const [machine, share] of machines) {
      const p = place(b, prompts, share)
      addPlace(byMachine, machine.id, p)
      addPlace(byCountry, machine.cc, p)
    }
    const models = splitModels(rand, provider, date, b)
    providers[provider] = {
      exact: opts.estimated ? EMPTY : b,
      estimated: opts.estimated ? b : EMPTY,
      prompts,
      promptsNoUsage: opts.noUsage ?? 0,
      accts: ACCTS[provider].filter((_, i) => i === 0 || rand() < 0.4),
      models: { exact: opts.estimated ? {} : models, estimated: opts.estimated ? models : {} },
      byMachine,
      byCountry,
    }
  }

  // Last autumn: a little Codex on the travel laptop, and Cursor sessions from October.
  if (!thisYear) {
    if (weekend || rand() < 0.45) return null
    add('openai', 6e6 + rand() * 40e6)
    if (rand() < 0.6) add('cursor', 2e6 + rand() * 10e6)
    return { v: 2, date, providers }
  }

  // Jan–May: Claude Code prompt history only (tokens never recorded), plus exact Codex Feb–Apr.
  if (md < '06-01') {
    const prompts = Math.round((weekend ? 4 : 12) + rand() * (weekend ? 40 : 240) + (rand() < 0.04 ? 400 : 0))
    const byMachine: Record<string, PlaceBucket> = {}
    const byCountry: Record<string, PlaceBucket> = {}
    for (const [machine, share] of machines) {
      const p: PlaceBucket = { in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: Math.round(prompts * share) }
      addPlace(byMachine, machine.id, p)
      addPlace(byCountry, machine.cc, p)
    }
    providers.anthropic = {
      exact: EMPTY,
      estimated: EMPTY,
      prompts,
      promptsNoUsage: prompts,
      accts: ['acct1'],
      models: { exact: {}, estimated: {} },
      byMachine,
      byCountry,
    }
    if (md >= '02-01' && md <= '04-30' && rand() < 0.75) add('openai', (weekend ? 0.3 : 1) * (18e6 + rand() * 140e6))
    // Cursor carries on until the end of February, then stops.
    if (md <= '02-28' && rand() < (weekend ? 0.2 : 0.55)) add('cursor', (weekend ? 0.4 : 1) * (2e6 + rand() * 12e6))
    // A few Gemini CLI sessions in the second half of January, small and short-lived.
    if (md >= '01-12' && md <= '01-30' && !weekend && rand() < 0.35) add('google', 1e6 + rand() * 5e6)
    return { v: 2, date, providers }
  }

  // June onwards: measured Claude Code, growing into a heavy September.
  const month = Number(md.slice(0, 2))
  const growth = month >= 9 ? 3.2 : month === 8 ? 1.6 : month === 7 ? 1.25 : 1
  const burst = rand() < 0.1 ? 3 + rand() * 3 : 1
  let base = (weekend ? 0.3 : 1) * growth * burst
  // Two extreme days to prove the clipping, and two tiny ones at the cool end.
  if (md === '08-27') base = 60
  if (md === '09-17') base = 34
  if (md === '06-03' || md === '07-30') base = 0.004

  // A few late-July weekends are only Grok web chats: estimated tokens, drawn faint.
  if (md === '07-18' || md === '07-25' || md === '07-26') {
    add('xai', 20e6 + rand() * 60e6, { estimated: true })
    return { v: 2, date, providers }
  }

  // Early June still has some prompts from before the transcripts (a ring over the fill).
  const noUsage = md <= '06-12' && rand() < 0.6 ? Math.round(10 + rand() * 90) : 0
  add('anthropic', base * (35e6 + rand() * 190e6), { noUsage })
  if (month >= 7 && rand() < 0.5) add('openai', base * 0.5 * (10e6 + rand() * 110e6))
  if (md >= '07-10' && rand() < 0.4) add('xai', (weekend ? 0.4 : 1) * (4e6 + rand() * 36e6), { estimated: md >= '07-18' && md <= '07-29' })
  return { v: 2, date, providers }
}

export function buildMockUsage(now: Date): UsageResponse {
  const today = toIsoDate(now)
  const year = now.getFullYear()
  const start = `${year - 1}-10-20`
  const days: RollupDay[] = []
  for (let date = start; date <= today; date = addDays(date, 1)) {
    const day = mockDay(date, year)
    if (day) days.push(day)
  }

  const minutesAgo = (min: number) => new Date(now.getTime() - min * 60_000).toISOString()
  const machines: PublicMachine[] = MACHINES.map((m, index) => ({
    id: m.id,
    label: m.label,
    cc: m.cc,
    os: m.os,
    live: m.live,
    lastSeenAt: m.live ? minutesAgo(1 + index) : minutesAgo(60 * 24 * (20 + index * 9)),
    firstSeenAt: `${index === 4 ? year - 1 : year}-${index === 4 ? '10-20' : '01-02'}T09:00:00.000Z`,
  }))

  return {
    generatedAt: now.toISOString(),
    lastIngestAt: minutesAgo(0.75),
    firstDate: days[0]?.date ?? null,
    days,
    machines,
    totals: {
      accounts: 7,
      accountsByProvider: { anthropic: 2, openai: 2, xai: 1, cursor: 1, google: 1 },
      machines: machines.length,
      machinesLive: machines.filter((m) => m.live).length,
    },
  }
}

const MOCK_PROMPTS = [
  'Refactor the heatmap legend so the ring key sits beside the colour scale.',
  'Why does the outbox keep one batch file after every tick? Walk through upload.go.',
  'Write a node:test case for the rollup merge when pv goes backwards.',
  'Summarise the diff in collector/internal/sources/codex and flag anything that could double count.',
  'Add a phone layout for the year selector. It should fit 390px with a 16px gutter.',
  'Rename machineLabel to label everywhere in the heartbeat handler, keep the wire field.',
  'Explain the epoch rule for legacy token_count lines with an example file.',
  'Draft the install page copy: what it changes on a machine and how to uninstall.',
]

const MOCK_WORKSPACES = ['C:\\Users\\dev\\src\\site', '/Users/dev/src/collector', 'C:\\Users\\dev\\src\\notes']
const MOCK_ACCOUNTS = ['personal · Studio', 'work · Team', 'side · Personal']
const MOCK_SOURCE: Record<Provider, string> = { anthropic: 'claude-code', openai: 'codex', xai: 'grok-cli', cursor: 'cursor', google: 'gemini-cli' }

export function buildMockPrompts(now: Date, month?: string): PromptsResponse {
  const currentMonth = toIsoDate(now).slice(0, 7)
  const months: string[] = []
  const [y, m] = currentMonth.split('-').map(Number)
  for (let i = 0; i < 4; i += 1) {
    const d = new Date(Date.UTC(y, m - 1 - i, 1))
    months.push(d.toISOString().slice(0, 7))
  }
  const selected = month && months.includes(month) ? month : currentMonth
  const rand = mulberry32(hashString(`d0m1-prompts|${selected}`))
  const [sy, sm] = selected.split('-').map(Number)
  const monthStart = Date.UTC(sy, sm - 1, 1)
  const monthEnd = Math.min(Date.UTC(sy, sm, 1), now.getTime())

  const items: PromptItem[] = []
  for (let i = 0; i < 140; i += 1) {
    const provider = PROVIDERS[Math.floor(rand() * PROVIDERS.length)]
    const ts = new Date(monthStart + rand() * (monthEnd - monthStart))
    const base = MOCK_PROMPTS[Math.floor(rand() * MOCK_PROMPTS.length)]
    const long = rand() < 0.12
    const models = MODEL_MIX[provider].filter(([id]) => id !== 'unknown')
    items.push({
      id: `mock${String(i).padStart(4, '0')}${selected.replace('-', '')}`,
      ts: ts.toISOString(),
      tzOffsetMin: rand() < 0.5 ? 60 : 420,
      provider,
      source: MOCK_SOURCE[provider],
      model: models[Math.floor(rand() * models.length)][0],
      acctLabel: MOCK_ACCOUNTS[Math.floor(rand() * MOCK_ACCOUNTS.length)],
      workspace: MOCK_WORKSPACES[Math.floor(rand() * MOCK_WORKSPACES.length)],
      machine: MACHINES[Math.floor(rand() * 3)].label,
      text: long ? Array.from({ length: 14 }, (_, n) => `${n + 1}. ${base}`).join('\n') : base,
    })
  }
  items.sort((a, b) => b.ts.localeCompare(a.ts))
  return { month: selected, items, months }
}

export interface PromptFilters {
  month: string
  workspace: string
  provider: string
  model: string
  acct: string
  machine: string
  q: string
}

/**
 * The whole synthetic archive for /tokens/prompts?mock=1, in the API's shapes: every month's prompts with their
 * facet keys, and the facets computed over all of them. Older prompts are partly unattributed, like history from
 * before a collector was installed.
 */
export function buildMockArchive(now: Date): { items: PromptItem[]; months: string[]; facets: PromptFacets } {
  const { months } = buildMockPrompts(now)
  const items: PromptItem[] = []
  months.forEach((month, mi) => {
    for (const item of buildMockPrompts(now, month).items) {
      const n = hashString(item.id)
      // Recent prompts carry the tool's own record; older ones are bounded by logins, and some fall below the
      // label threshold: an inferred candidate ("probably ...") or no candidate at all.
      const unattributed = mi > 0 && n % 3 === 0
      const candidate = unattributed && n % 2 === 0
      const labelled: AcctQ = mi === 0 ? (n % 5 === 0 ? 'timeline' : 'recorded') : 'bounded'
      const below: AcctQ = candidate ? (n % 4 === 0 ? 'lineage' : 'inferred') : 'unknown'
      const quality = unattributed ? below : labelled
      const acctLabel = unattributed ? '' : item.acctLabel
      const sep = item.workspace.includes('\\') ? '\\' : '/'
      const workspace = n % 4 === 0 ? `${item.workspace}${sep}sub` : item.workspace
      const root = item.workspace
      items.push({
        ...item,
        month,
        acctLabel,
        acctQ: quality,
        acctProbable: candidate ? item.acctLabel : '',
        acct: unattributed
          ? candidate
            ? `unknown:${item.provider}:mock:${item.acctLabel}`
            : `unknown:${item.provider}`
          : `mock:${item.provider}:${item.acctLabel}~${quality}`,
        workspace,
        workspaceKey: root.toLowerCase(),
        workspaceLabel: root.split(/[\\/]/).filter(Boolean).pop() ?? root,
      })
    }
  })
  items.sort((a, b) => b.ts.localeCompare(a.ts))
  return { items, months, facets: mockFacets(items, months) }
}

function countBy<T>(items: T[], keyOf: (item: T) => string) {
  const m = new Map<string, number>()
  for (const item of items) {
    const k = keyOf(item)
    if (k) m.set(k, (m.get(k) ?? 0) + 1)
  }
  return m
}

function mockFacets(items: PromptItem[], months: string[]): PromptFacets {
  const byCount = (a: { count: number; value: string }, b: { count: number; value: string }) =>
    b.count - a.count || a.value.localeCompare(b.value)
  const accounts = new Map<string, PromptFacets['accounts'][number]>()
  for (const item of items) {
    const key = item.acct ?? ''
    const unattributed = key.startsWith('unknown:')
    const a = accounts.get(key) ?? {
      acct: key,
      provider: item.provider,
      label: item.acctLabel,
      quality: unattributed ? '' : (item.acctQ ?? ''),
      count: 0,
      unattributed,
      ...(unattributed && item.acctProbable ? { probable: item.acctProbable } : {}),
    }
    a.count += 1
    accounts.set(key, a)
  }
  const workspaces = new Map<string, PromptFacets['workspaces'][number]>()
  for (const item of items) {
    const key = item.workspaceKey ?? ''
    const w = workspaces.get(key) ?? { key, label: item.workspaceLabel ?? '', path: '', paths: [], count: 0 }
    w.count += 1
    if (!w.paths.includes(item.workspace)) w.paths.push(item.workspace)
    if (!w.path || item.workspace.length < w.path.length) w.path = item.workspace
    workspaces.set(key, w)
  }
  const monthCounts = countBy(items, (item) => item.month ?? '')
  const rank = (p: Provider) => PROVIDERS.indexOf(p)
  return {
    total: items.length,
    providers: [...countBy(items, (item) => item.provider)]
      .map(([provider, count]) => ({ provider: provider as Provider, count }))
      .sort((a, b) => rank(a.provider) - rank(b.provider)),
    accounts: [...accounts.values()].sort(
      (a, b) =>
        rank(a.provider) - rank(b.provider) ||
        Number(a.unattributed) - Number(b.unattributed) ||
        Number(!a.probable) - Number(!b.probable) ||
        b.count - a.count,
    ),
    workspaces: [...workspaces.values()].sort((a, b) => b.count - a.count),
    models: [...countBy(items, (item) => item.model)].map(([value, count]) => ({ value, count })).sort(byCount),
    machines: [...countBy(items, (item) => item.machine)].map(([value, count]) => ({ value, count })).sort(byCount),
    months: months.map((month) => ({ month, count: monthCounts.get(month) ?? 0 })),
  }
}

/** List order of the prompts API: oldest first (asc, the default) or newest first (desc). */
export type PromptOrder = 'asc' | 'desc'

/**
 * The API's filter rules (exact facet values, substring search) over the synthetic archive, in the API's order:
 * ts, then id, ascending for asc and the exact reverse for desc. The archive is kept newest first.
 */
export function queryMockArchive(items: PromptItem[], f: PromptFilters, order: PromptOrder = 'asc'): PromptItem[] {
  const q = f.q.toLowerCase()
  const matches = items.filter(
    (item) =>
      (f.month === 'all' || item.month === f.month) &&
      (!f.workspace || item.workspaceKey === f.workspace) &&
      (!f.provider || item.provider === f.provider) &&
      (!f.model || item.model === f.model) &&
      (!f.acct || item.acct === f.acct) &&
      (!f.machine || item.machine === f.machine) &&
      (!q || item.text.toLowerCase().includes(q)),
  )
  const newestFirst = (a: PromptItem, b: PromptItem) => b.ts.localeCompare(a.ts) || b.id.localeCompare(a.id)
  matches.sort(newestFirst)
  return order === 'asc' ? matches.reverse() : matches
}
