import type { NormDay } from './usage'

export const CLAUDE_CHANNELS = ['claude-code', 'web-claude', 'native-claude', 'chat-claude'] as const
export type ClaudeChannel = typeof CLAUDE_CHANNELS[number]
export const SOURCE_LABEL: Record<string, string> = {
  'claude-code': 'Claude Code',
  'web-claude': 'Claude Web',
  'native-claude': 'Claude Native (apps)',
  'chat-claude': 'Claude chat, client not recorded',
  codex: 'Codex', 'grok-cli': 'Grok CLI', cursor: 'Cursor', 'gemini-cli': 'Gemini CLI',
}
export const sourceLabel = (source: string) => typeof SOURCE_LABEL[source] === 'string' ? SOURCE_LABEL[source] : source
export const isClaudeChannel = (value: string | null): value is ClaudeChannel =>
  CLAUDE_CHANNELS.some(source => source === value)

/**
 * How a channel's tokens are measured (SPEC "Claude chat"). claude.ai reports
 * no token counts for chat, so Web and unrecorded-client chat are estimates
 * (a lower bound). Native mixes exact counts (the desktop app's Code tab and
 * local Cowork) with estimated app chat.
 */
export const CHANNEL_TOKENS: Record<ClaudeChannel, 'exact' | 'estimated' | 'mixed'> = {
  'claude-code': 'exact', 'web-claude': 'estimated', 'native-claude': 'mixed', 'chat-claude': 'estimated',
}

/** The channel selector's label: estimated channels say so. */
export const channelOptionLabel = (channel: ClaudeChannel) =>
  sourceLabel(channel) + (CHANNEL_TOKENS[channel] === 'estimated' ? ' (estimated)' : '')

/** No guessing the channel of older rollups that lack a source split. */
export function channelDays(days: NormDay[], channel: ClaudeChannel | 'all'): NormDay[] {
  if (channel === 'all') return days
  return days.map(day => {
    const group = day.providers.anthropic?.bySource?.[channel]
    return { date: day.date, providers: group ? { anthropic: { ...group, accts: [] } } : {} }
  })
}

export interface TokenBuckets { in: number; cacheW: number; cacheR: number; out: number }

export interface ChannelCount {
  source: ClaudeChannel
  observed: boolean
  in: number
  cacheW: number
  cacheR: number
  out: number
  prompts: number
  promptsNoUsage: number
  /** Tokens of estimated events among in + cacheW + cacheR + out. */
  estimated: number
  /**
   * True when a machine is selected on a day that mixes exact and estimated
   * tokens: the machine split has no quality split, so `estimated` takes the
   * day's ratio.
   */
  estimatedApprox: boolean
  /**
   * The buckets of exact and of estimated events apart. On a mixed day with a
   * machine selected each bucket takes the day's ratio (estimatedApprox).
   */
  parts: { exact: TokenBuckets; estimated: TokenBuckets }
}

const emptyBuckets = (): TokenBuckets => ({ in: 0, cacheW: 0, cacheR: 0, out: 0 })
export const bucketTotal = (b: TokenBuckets) => b.in + b.cacheW + b.cacheR + b.out

const BUCKETS = ['in', 'cacheW', 'cacheR', 'out'] as const

/** Read the source's own machine split, never apportion a provider's totals. */
export function claudeChannelCounts(days: NormDay[], from: string, to: string, machine = 'all'): ChannelCount[] {
  return CLAUDE_CHANNELS.map(source => {
    const result: ChannelCount = { source, observed: false, in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0, promptsNoUsage: 0,
      estimated: 0, estimatedApprox: false, parts: { exact: emptyBuckets(), estimated: emptyBuckets() } }
    for (const day of days) {
      if (day.date < from || day.date > to) continue
      const group = day.providers.anthropic?.bySource?.[source]
      if (!group) continue
      const place = machine === 'all' ? null : group.byMachine[machine]
      if (machine !== 'all' && !place) continue
      result.observed = true
      const exact = BUCKETS.reduce((n, key) => n + group.exact[key], 0)
      const estimated = BUCKETS.reduce((n, key) => n + group.estimated[key], 0)
      let total = 0
      for (const key of BUCKETS) {
        const v = place ? place[key] : group.exact[key] + group.estimated[key]
        result[key] += v
        total += v
        const both = group.exact[key] + group.estimated[key]
        const est = !place ? group.estimated[key] : both > 0 ? Math.round(v * group.estimated[key] / both) : 0
        result.parts.estimated[key] += est
        result.parts.exact[key] += v - est
      }
      if (!place) result.estimated += estimated
      else if (estimated > 0 && exact === 0) result.estimated += total
      else if (estimated > 0) {
        result.estimated += Math.round(total * estimated / (exact + estimated))
        result.estimatedApprox = true
      }
      result.prompts += place ? place.prompts : group.prompts
      result.promptsNoUsage += place ? place.promptsNoUsage ?? 0 : group.promptsNoUsage
    }
    return result
  })
}
