import LoadingIndicator from '../LoadingIndicator'
import { formatProjectShare, isProjectRange, PROJECT_RANGES, type ProjectUsage } from './projectUsage'
import type { ProjectExpansionProps } from './projectUsageStore'
import { formatCompact } from './usage'

export type { ProjectExpansionProps } from './projectUsageStore'

const DATE = new Intl.DateTimeFormat('en-GB', {
  weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short',
})
const EXACT_DATE = new Intl.DateTimeFormat('en-GB', { dateStyle: 'full', timeStyle: 'long' })

/** Kept separate so the same real API response can be checked in rendered tests. */
export function ProjectUsageTable({ data }: { data: ProjectUsage }) {
  const since = new Date(data.since)
  // Only the caveats that apply, on one line.
  const caveats = [
    data.projected && 'reset projected weekly from the last reading',
    data.totals.estimatedTokens > 0 && `includes ${formatCompact(data.totals.estimatedTokens)} estimated tokens`,
    data.totals.uncertainTokens > 0 && `${formatCompact(data.totals.uncertainTokens)} tokens with an uncertain account match excluded`,
    data.undecryptablePrompts > 0 && 'some project names could not be read',
    data.timingLimited && 'Only usage with a recorded event time is included; conversation-start timestamps and older Cursor records without timing evidence are excluded',
  ].filter((note): note is string => typeof note === 'string')
  return <>
    <div className="limits-projects__head">
      <p className="limits-projects__period">
        <time dateTime={data.since} title={EXACT_DATE.format(since)}>{DATE.format(since)}</time>
        {' – '}<time dateTime={data.until} title={EXACT_DATE.format(new Date(data.until))}>{DATE.format(new Date(data.until))}</time>
      </p>
      <span title={`${data.totals.tokens.toLocaleString('en-GB')} recorded tokens`}>{formatCompact(data.totals.tokens)} tokens</span>
    </div>
    {data.projects.length ? <table className="limits-projects__table">
      <caption>Projects ordered by recorded token usage in the selected period. Share is a percentage of recorded account tokens, not subscription quota.</caption>
      <thead><tr><th scope="col">Project</th><th scope="col">Share</th><th scope="col">Tokens</th></tr></thead>
      <tbody>{data.projects.map((project, index) => {
        return <tr key={project.key}>
          <th scope="row" title={project.path || 'The collector could not match these tokens to a project.'}>
            <span className="limits-projects__rank" aria-hidden="true">{index + 1}.</span>{project.name}
          </th>
          <td>{formatProjectShare(project.percent)}</td>
          <td title={`${project.tokens.toLocaleString('en-GB')} tokens`}>{formatCompact(project.tokens)}</td>
        </tr>
      })}</tbody>
    </table> : <p className="limits-projects__empty">No project token usage recorded in this period.</p>}
    <p className="limits-projects__note">Project data refreshed <time dateTime={data.generatedAt}>{DATE.format(new Date(data.generatedAt))}</time>. Share of recorded tokens.</p>
    {caveats.length > 0 && <p className="limits-projects__note">{caveats.join(' · ')}</p>}
  </>
}

const UNAVAILABLE = {
  'reset-unavailable': 'No weekly reset time is available. Choose a rolling time period to see recorded usage.',
  'account-unavailable': 'The collector has not reliably identified this account yet.',
  'organisation-unavailable': 'This login has multiple organisations. Its project token records do not identify which organisation paid for the usage.',
} as const

/** One account's panel, from the page's preloaded store. */
export default function AccountProjects({ accountKey, resetsAt, mock, usage }: ProjectExpansionProps & { accountKey: string; resetsAt?: number | null }) {
  if (!mock && !usage.supported) return <p className="limits-projects__note">Project details are not available from this data source.</p>
  const load = usage.get(accountKey)
  return <>
    <div className="limits-projects__head">
      <h3>Projects</h3>
      <label className="limits-projects__filter">Period
        <select aria-label="Project usage period" value={usage.range(accountKey)} onChange={event => {
          if (isProjectRange(event.target.value)) usage.select(accountKey, event.target.value)
        }}>
          {PROJECT_RANGES.map(([range, label]) => <option key={range} value={range} disabled={range === 'reset' && !resetsAt}>{label}</option>)}
        </select>
      </label>
    </div>
    <div aria-live="polite" aria-busy={!load || load.status === 'loading'}>
    {!load || load.status === 'loading' ? <LoadingIndicator label="Loading project usage…" inline /> :
    load.status === 'error' ? <div className="limits-projects__error" role="alert">
    <p>{load.message}</p><button type="button" className="agents-btn" onClick={() => usage.retry(accountKey)}>Try again</button>
    </div> : !load.data.available ? <p className="limits-projects__note">{UNAVAILABLE[load.data.reason]}</p> :
    <ProjectUsageTable data={load.data} />}
    </div>
  </>
}
