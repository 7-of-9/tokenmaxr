import { Fragment, useMemo } from 'react'
import { bucketTotal, claudeChannelCounts, sourceLabel, type ChannelCount, type TokenBuckets } from './channels'
import { formatCount, type NormDay } from './usage'
import './ClaudeChannels.css'

const total = (row: ChannelCount) => row.in + row.cacheW + row.cacheR + row.out

/** Estimated numbers carry a "~": they are lower-bound estimates, not counts. */
function count(row: ChannelCount, value: number) {
  if (!row.observed) return '—'
  const all = total(row)
  return (value > 0 && row.estimated * 2 > all ? '~' : '') + formatCount(value)
}

function estimateNote(row: ChannelCount) {
  const all = total(row)
  if (!row.observed || row.estimated <= 0 || all <= 0) return null
  const share = Math.round(100 * row.estimated / all)
  const note = share >= 100 ? 'all estimated' : `${share < 1 ? '<1' : share}% estimated${row.estimatedApprox ? ' (approx.)' : ''}`
  return <small>{note}{row.source === 'native-claude' && ', origin inferred'}</small>
}

const BUCKET_KEYS = ['in', 'cacheW', 'cacheR', 'out'] as const

/** Sub-row labels of a channel that mixes exact and estimated tokens. */
function partLabels(row: ChannelCount): [string, string] {
  return row.source === 'native-claude'
    ? ['Code tab and Cowork (exact)', 'App chat (estimated, origin inferred)']
    : ['Exact', 'Estimated']
}

/** A mixed channel's exact and estimated parts, each on its own sub-row. */
function PartRows({ row }: { row: ChannelCount }) {
  const { exact, estimated } = row.parts
  if (!row.observed || bucketTotal(exact) <= 0 || bucketTotal(estimated) <= 0) return null
  const [exactLabel, estimatedLabel] = partLabels(row)
  const cells = (b: TokenBuckets, mark: string) => <>
    {BUCKET_KEYS.map(key => <td key={key}>{b[key] > 0 ? mark : ''}{formatCount(b[key])}</td>)}
    <td>{mark}{formatCount(bucketTotal(b))}{mark && row.estimatedApprox && <small>approx.</small>}</td>
    <td />
  </>
  return <>
    <tr className="claude-channels__part"><th scope="row">{exactLabel}</th>{cells(exact, '')}</tr>
    <tr className="claude-channels__part"><th scope="row">{estimatedLabel}</th>{cells(estimated, '~')}</tr>
  </>
}

export default function ClaudeChannels({ days, from, to, machine }: { days: NormDay[]; from: string; to: string; machine: string }) {
  const rows = useMemo(() => claudeChannelCounts(days, from, to, machine), [days, from, to, machine])
  return <section className="claude-channels" aria-label="Claude usage by channel">
    <h2>Claude by channel</h2>
    <div className="claude-channels__scroll">
      <table>
        <thead><tr><th scope="col">Channel</th><th scope="col">Input</th><th scope="col">Cache write</th><th scope="col">Cache read</th><th scope="col">Output</th><th scope="col">Total tokens</th><th scope="col">Prompts</th></tr></thead>
        <tbody>{rows.map(row => <Fragment key={row.source}><tr>
          <th scope="row">{sourceLabel(row.source)}{row.observed && row.estimated > 0 && <span className="claude-channels__badge" title="Estimated: the conversation text, plus the fixed context each model call re-reads as cache reads. A lower bound">Estimated</span>}</th>
          {(['in', 'cacheW', 'cacheR', 'out'] as const).map(key => <td key={key}>{count(row, row[key])}</td>)}
          <td>{row.observed ? count(row, total(row)) : 'No records'}{estimateNote(row)}</td>
          <td>{row.observed ? formatCount(row.prompts) : '—'}{row.promptsNoUsage > 0 && <small>{formatCount(row.promptsNoUsage)} without token records</small>}</td>
        </tr><PartRows row={row} /></Fragment>)}</tbody>
      </table>
    </div>
    <p className="agents-muted">Collected records in the selected period and machine. No records means coverage is unavailable, not zero account usage.
      Claude Code, and under Claude Native the desktop app&apos;s Code tab and local Cowork tasks, are provider-reported counts. claude.ai reports no token counts for chat, so Claude Web and app chat are
      estimated (~): the conversation text as input and output, plus the fixed context every model call re-reads, shown as cache reads: the tool
      definitions the page sends (live chats only) and a 2,000-token floor for the system prompt, which is not visible. It is still a lower bound: memory,
      project knowledge and hidden thinking are not counted. App chat is inferred: a turn no browser recorded. Chat from before capture started is listed as client not recorded.</p>
  </section>
}
