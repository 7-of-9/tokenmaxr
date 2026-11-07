import LoadingIndicator from '../LoadingIndicator'
import { formatProjectShare, isProjectRange, PROJECT_RANGES, type ProjectUsage } from './projectUsage'
import type { ProjectExpansionProps } from './projectUsageStore'
import { formatCompact } from './usage'

export type { ProjectExpansionProps } from './projectUsageStore'

const DATE = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' })

/** Kept separate so the same real API response can be checked in rendered tests. */
export function ProjectUsageTable({ data }: { data: ProjectUsage }) {
  // Only what changes how the numbers read.
  const caveats = [
    data.projected && 'reset projected',
    data.totals.estimatedTokens > 0 && `${formatCompact(data.totals.estimatedTokens)} estimated`,
    data.totals.uncertainTokens > 0 && `${formatCompact(data.totals.uncertainTokens)} excluded, account unclear`,
    data.undecryptablePrompts > 0 && 'some names unreadable',
    data.timingLimited && 'untimed events excluded',
  ].filter((note): note is string => typeof note === 'string')
  return <>
    {data.projects.length ? <table className="limits-projects__table">
      <caption>Recorded tokens by project. Share is of this account's recorded tokens, not its quota.</caption>
      <thead><tr><th scope="col">Project</th><th scope="col">Share</th><th scope="col">Tokens</th></tr></thead>
      <tbody>{data.projects.map(project => <tr key={project.key}>
        <th scope="row" title={project.path || undefined}>{project.name}</th>
        <td>{formatProjectShare(project.percent)}</td>
        <td title={`${project.tokens.toLocaleString('en-GB')} tokens`}>{formatCompact(project.tokens)}</td>
      </tr>)}</tbody>
    </table> : <p className="limits-projects__empty">No usage recorded in this period.</p>}
    {caveats.length > 0 && <p className="limits-projects__note">{caveats.join(' · ')}</p>}
  </>
}

const UNAVAILABLE = {
  'reset-unavailable': 'No weekly reset known. Choose a time period.',
  'account-unavailable': 'Account not identified yet.',
  'organisation-unavailable': 'Usage can’t be split between this login’s organisations.',
} as const

/** One account's panel, from the page's preloaded store. */
export default function AccountProjects({ accountKey, resetsAt, mock, usage }: ProjectExpansionProps & { accountKey: string; resetsAt?: number | null }) {
  if (!mock && !usage.supported) return <p className="limits-projects__note">Not available from this data source.</p>
  const load = usage.get(accountKey)
  const data = load?.status === 'ok' && load.data.available ? load.data : null
  return <>
    <div className="limits-projects__head">
      <h3>Projects</h3>
      {data && data.totals.tokens > 0 && <span className="limits-projects__total" title={`${data.totals.tokens.toLocaleString('en-GB')} tokens · ${DATE.format(new Date(data.since))} – ${DATE.format(new Date(data.until))}`}>{formatCompact(data.totals.tokens)}</span>}
      <label className="limits-projects__filter">
        <select aria-label="Project usage period" value={usage.range(accountKey)} onChange={event => {
          if (isProjectRange(event.target.value)) usage.select(accountKey, event.target.value)
        }}>
          {PROJECT_RANGES.map(([range, label]) => <option key={range} value={range} disabled={range === 'reset' && !resetsAt}>{label}</option>)}
        </select>
      </label>
    </div>
    <div aria-live="polite" aria-busy={!load || load.status === 'loading'}>
    {!load || load.status === 'loading' ? <LoadingIndicator label="Loading…" inline /> :
    load.status === 'error' ? <div className="limits-projects__error" role="alert">
    <p>{load.message}</p><button type="button" className="agents-btn" onClick={() => usage.retry(accountKey)}>Try again</button>
    </div> : !load.data.available ? <p className="limits-projects__note">{UNAVAILABLE[load.data.reason]}</p> :
    <ProjectUsageTable data={load.data} />}
    </div>
  </>
}
