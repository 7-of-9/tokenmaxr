import LoadingIndicator from '../LoadingIndicator'
import { formatProjectShare, type ProjectUsage } from './projectUsage'
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
  const displaySince = new Date(Math.round(since.getTime() / 60_000) * 60_000)
  // Only the caveats that apply, on one line.
  const caveats = [
    data.projected && 'reset projected weekly from the last reading',
    data.totals.estimatedTokens > 0 && `includes ${formatCompact(data.totals.estimatedTokens)} estimated tokens`,
    data.totals.uncertainTokens > 0 && `${formatCompact(data.totals.uncertainTokens)} tokens with an uncertain account match excluded`,
    data.undecryptablePrompts > 0 && 'some project names could not be read',
  ].filter((note): note is string => typeof note === 'string')
  return <>
    <div className="limits-projects__head">
      <h3>Projects since <time dateTime={data.since} title={EXACT_DATE.format(since)}>{DATE.format(displaySince)}</time></h3>
      <span title={`${data.totals.tokens.toLocaleString('en-GB')} recorded tokens`}>{formatCompact(data.totals.tokens)} tokens</span>
    </div>
    {data.projects.length ? <table className="limits-projects__table">
      <caption>Projects ordered by recorded token usage since the weekly reset</caption>
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
    </table> : <p className="limits-projects__empty">No project token usage recorded since this reset.</p>}
    {caveats.length > 0 && <p className="limits-projects__note">{caveats.join(' · ')}</p>}
  </>
}

const UNAVAILABLE = {
  'reset-unavailable': 'A weekly reset time is needed to show usage for this account.',
  'account-unavailable': 'The collector has not reliably identified this account yet.',
  'organisation-unavailable': 'This login has multiple organisations. Its project token records do not identify which organisation paid for the usage.',
} as const

/** One account's panel, from the page's preloaded store. */
export default function AccountProjects({ meterId, resetsAt, mock, usage }: ProjectExpansionProps & { meterId?: string; resetsAt?: number | null }) {
  if (!meterId || !resetsAt) return <p className="limits-projects__note">{UNAVAILABLE['reset-unavailable']}</p>
  if (!mock && !usage.supported) return <p className="limits-projects__note">Project details are not available from this data source.</p>
  const load = usage.get(meterId)
  if (!load || load.status === 'loading') return <LoadingIndicator label="Loading project usage…" inline />
  if (load.status === 'error') return <div className="limits-projects__error" role="alert">
    <p>{load.message}</p><button type="button" className="agents-btn" onClick={() => usage.retry(meterId)}>Try again</button>
  </div>
  if (!load.data.available) return <p className="limits-projects__note">{UNAVAILABLE[load.data.reason]}</p>
  return <ProjectUsageTable data={load.data} />
}
