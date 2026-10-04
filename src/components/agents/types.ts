// Wire types for the d0m1 agents read API. docs/agents/SPEC.md is the source of truth.

export type Provider = 'anthropic' | 'openai' | 'xai' | 'cursor' | 'google'

/** tokens = in + cacheW + cacheR + out (the default); effective = in + cacheW + out + 0.1 × cacheR; output = out. */
export type Metric = 'tokens' | 'effective' | 'output'

/** One quality bucket of a rollup day (SPEC "Rollup data"). */
export interface TokenBucket {
  /** Source-reported total with no input/output/cache split; never estimated. */
  unattributed?: number
  in: number
  cacheW: number
  /** The 1-hour-TTL part of cacheW: a subset, never added. */
  cacheW1h: number
  cacheR: number
  out: number
  /** A subset of out, never added. */
  reasoning: number
  calls: number
  events: number
}

export type ModelBucket = Omit<TokenBucket, 'events'>

/** Per machine or per country: exact + estimated tokens and all activity. */
export interface PlaceBucket {
  /** Account history without a known token split or machine attribution. */
  unattributed?: number
  in: number
  cacheW: number
  cacheR: number
  out: number
  prompts: number
  /** Present on workspace place splits; missing-usage prompts only. */
  promptsNoUsage?: number
}

/** Anonymous workspace aggregate: same token/model/place accounting as its provider. */
export type WorkspaceDay = Omit<ProviderDay, 'accts' | 'byWorkspace'>

export interface InferenceDerivation {
  provider: Provider
  workspace: string | null
  month: string
  basis: 'workspace-month' | 'workspace-history' | 'provider-month' | 'provider-history'
  sampleFrom?: string
  sampleTo?: string
  samplePrompts: number
  perPrompt: TokenBucket
  missingInMonth: number
  affectedDays: number
  missingToday: number
}

export interface ProviderDay {
  exact: TokenBucket
  estimated: TokenBucket
  /** All activity events (user prompts). */
  prompts: number
  /** Activity with hasUsage=false: prompts whose tokens were never recorded. */
  promptsNoUsage: number
  /** Recorded archive prompt counts by model; absent when model metadata is unavailable. */
  promptsByModel?: Record<string, number>
  /** Read-time only (infer.ts, "Estimate unrecorded"): the part of `estimated` inferred from those prompts. A subset, never added. */
  inferred?: TokenBucket
  /** The same inferred tokens per model (a subset of models.estimated, never added). */
  inferredModels?: Record<string, ModelBucket>
  /** Read-time explanation of each workspace's monthly estimate. */
  derivations?: InferenceDerivation[]
  /** Account totals reconciled against local records; the parent date is UTC. */
  accountUsage?: Array<{
    source: string
    timezone: 'UTC'
    totalTokens: number
    matchedTokens: number
    uncertainTokens: number
    recoveredTokens: number
    observedAt: string
  }>
  /** Per-response workspace aliases; no paths, names or persistent hashes. */
  byWorkspace?: Record<string, WorkspaceDay>
  /** Per-response account aliases (never hashes). */
  accts: string[]
  models: { exact: Record<string, ModelBucket>; estimated: Record<string, ModelBucket> }
  /** Keyed by machineId. */
  byMachine: Record<string, PlaceBucket>
  /** Keyed by ISO country code; "ZZ" is unknown. */
  byCountry: Record<string, PlaceBucket>
}

export interface RollupDay {
  v?: number
  /** Local calendar date YYYY-MM-DD (ts + tzOffsetMin). */
  date: string
  providers: Partial<Record<Provider, ProviderDay>>
}

export interface UsageTotals {
  accounts: number
  accountsByProvider: Partial<Record<Provider, number>>
  machines: number
  machinesLive: number
}

/** A public machine row (label and country are public since v1.1). */
export interface PublicMachine {
  id: string
  label: string
  cc: string
  os: string
  live: boolean
  lastSeenAt: string | null
  firstSeenAt?: string | null
}

/** GET /api/usage?days=all */
export interface UsageResponse {
  generatedAt: string
  lastIngestAt: string | null
  firstDate?: string | null
  /** Account snapshots withheld until local overlap coverage is complete. */
  accountUsagePending?: number
  /** Account windows withheld because their totals conflict with local records. */
  accountUsageConflicts?: number
  days: RollupDay[]
  machines?: PublicMachine[]
  totals: UsageTotals
}

export interface PromptItem {
  id: string
  ts: string
  tzOffsetMin?: number
  provider: Provider
  source: string
  model: string
  /** The account label; empty unless the attribution quality is recorded, session, timeline or bounded. */
  acctLabel: string
  /** How the collector attributed the account (SPEC "Accounts"); absent on old mock data. */
  acctQ?: AcctQ
  /** For an unattributed prompt: the label of the inferred candidate account, when one is known. */
  acctProbable?: string
  workspace: string
  machine: string
  text: string
  /** Set by the list endpoint when text is a preview; fetch /api/prompts/{id} for the rest. */
  truncated?: boolean
  /** The UTC month partition (YYYY-MM) that holds the prompt. */
  month?: string
  /**
   * Account facet key: <acct>~<quality> for a labelled attribution, unknown:<provider>:<acct> for one below the
   * label threshold with a known candidate, else unknown:<provider>.
   */
  acct?: string
  /** Workspace facet key (the canonical, flattened root) and its basename label. */
  workspaceKey?: string
  workspaceLabel?: string
}

/** GET /api/prompts[?month=all|YYYY-MM&order=asc|desc&workspace=&provider=&model=&acct=&machine=&q=&limit=&cursor=] */
export interface PromptsResponse {
  /** 'all' or YYYY-MM. */
  month: string
  /** asc = oldest first (the default), desc = newest first. Ties break by id. */
  order?: 'asc' | 'desc'
  items: PromptItem[]
  months: string[]
  /** Continuation token for older matching items; absent or null when done. */
  cursor?: string | null
  /** Matching items across all pages. */
  total?: number
}

/** Account attribution quality, best first. Labels are shown only for recorded, session, timeline and bounded. */
export type AcctQ = 'recorded' | 'session' | 'timeline' | 'bounded' | 'lineage' | 'inferred' | 'unknown'

export interface PromptAccountFacet {
  acct: string
  provider: Provider
  label: string
  /** The attribution quality of a labelled entry; '' when unattributed. */
  quality: AcctQ | ''
  count: number
  /** Below the label threshold (lineage, inferred or unknown): the account is not proven. */
  unattributed: boolean
  /** For an unattributed entry: the label of its candidate account, when one is known. */
  probable?: string
}

export interface PromptWorkspaceFacet {
  key: string
  label: string
  /** The root as most often written. */
  path: string
  /** Raw workspaces folded into this one. */
  paths: string[]
  count: number
}

/** GET /api/prompts/facets: every filter value across the whole archive, with counts. */
export interface PromptFacets {
  total: number
  providers: { provider: Provider; count: number }[]
  accounts: PromptAccountFacet[]
  workspaces: PromptWorkspaceFacet[]
  models: { value: string; count: number }[]
  machines: { value: string; count: number }[]
  months: { month: string; count: number }[]
}

/** SWA /.auth/me principal. */
export interface ClientPrincipal {
  identityProvider: string
  userId: string
  userDetails: string
  userRoles: string[]
}
