import { useState, type ReactNode } from 'react'
import { monthLong, type ActivityMonth } from './activity'
import { assignedNote } from './attribution'
import Flag from './Flag'
import Bar from './UsageBar'
import { ACCOUNT_HISTORY, formatCount, formatDayShort, modelLabel, plural, PROVIDER_LABEL } from './usage'

interface ActivityFeedProps {
  months: ActivityMonth[]
  today: string
  noun: string
}

/** GitHub shows the newest months, then loads older ones on "Show more activity". */
const MONTHS_FIRST = 2
const MONTHS_MORE = 2
/** Long model lists end in a "Show N more" toggle, so one busy month cannot take over the page. */
const ROWS_FIRST = 8

// 16 px line icons in the spirit of GitHub's Octicons, drawn in the badge's muted ink.
const ICONS: Record<string, ReactNode> = {
  tokens: <path d="M3 13.25V8.5M6.5 13.25V3.5M10 13.25V6.5M13.5 13.25V2.75" />,
  machines: (
    <>
      <rect x="1.75" y="2.5" width="12.5" height="8.75" rx="1.5" />
      <path d="M5.5 14h5M8 11.25V14" />
    </>
  ),
  started: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 5.25v5.5M5.25 8h5.5" />
    </>
  ),
  prompts: <path d="M2.75 3.25h10.5v7.25H8l-3 2.5V10.5H2.75z" />,
  first: <path d="M3.75 14.25V2.25M3.75 2.75h8.25l-1.75 2.75 1.75 2.75H3.75" />,
  fold: <path d="M8 1.25v4.5M5.75 3.5 8 5.75l2.25-2.25M8 14.75v-4.5M5.75 12.5 8 10.25l2.25 2.25M1.75 8h1.5M5.25 8h1.5M9.25 8h1.5M12.75 8h1.5" />,
  unfold: <path d="M8 5.75v-4.5M5.75 3.5 8 1.25l2.25 2.25M8 10.25v4.5M5.75 12.5 8 14.75l2.25-2.25M1.75 8h1.5M5.25 8h1.5M9.25 8h1.5M12.75 8h1.5" />,
}

const Icon = ({ name }: { name: keyof typeof ICONS }) => (
  <svg className="gh-icon" width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
    {ICONS[name]}
  </svg>
)

interface ItemProps {
  id: string
  icon: keyof typeof ICONS
  title: ReactNode
  aside?: ReactNode
  children?: ReactNode
  folded: boolean
  onFold?: () => void
}

const Item = ({ id, icon, title, aside, children, folded, onFold }: ItemProps) => (
  <li className="gh-timeline__item">
    <span className="gh-timeline__badge">
      <Icon name={icon} />
    </span>
    <div className="gh-timeline__body">
      <div className="gh-timeline__head">
        <h4 className="gh-timeline__title" id={`${id}-title`}>
          {title}
        </h4>
        {aside}
        {children && onFold && (
          <button
            type="button"
            className="gh-fold"
            aria-expanded={!folded}
            aria-controls={`${id}-rows`}
            aria-label={folded ? 'Expand' : 'Collapse'}
            title={folded ? 'Expand' : 'Collapse'}
            onClick={onFold}
          >
            <Icon name={folded ? 'unfold' : 'fold'} />
          </button>
        )}
      </div>
      {children && !folded && (
        <div id={`${id}-rows`} aria-labelledby={`${id}-title`}>
          {children}
        </div>
      )}
    </div>
  </li>
)

const ActivityFeed = ({ months, today, noun }: ActivityFeedProps) => {
  const [shown, setShown] = useState(MONTHS_FIRST)
  const [folded, setFolded] = useState<Set<string>>(() => new Set())
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const toggle = (set: Set<string>, key: string) => {
    const next = new Set(set)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    return next
  }
  const fold = (key: string) => () => setFolded((prev) => toggle(prev, key))

  if (months.length === 0) {
    return (
      <section className="gh-activity" aria-labelledby="gh-activity-title">
        <h2 className="gh-activity__title" id="gh-activity-title">
          Token activity
        </h2>
        <p className="gh-activity__empty">No {noun} recorded in this period.</p>
      </section>
    )
  }

  const counts = (value: number, prompts: number | null, archived = false) => <>
    <strong>{formatCount(value)}</strong> {value === 1 ? noun.replace(/s$/, '') : noun}
    {' · '}<span title={prompts === null ? 'Prompt count unavailable for this model' : archived ? 'Archived prompts attributed to this model. Totals include all observed prompts.' : undefined}>
      <strong>{prompts === null ? '—' : formatCount(prompts)}</strong> {prompts === 1 ? 'prompt' : 'prompts'}
    </span>
  </>

  return (
    <section className="gh-activity" aria-labelledby="gh-activity-title">
      <h2 className="gh-activity__title" id="gh-activity-title">
        Token activity
      </h2>
      {months.slice(0, shown).map((month) => {
        const [y, m] = month.month.split('-').map(Number)
        const modelsKey = `${month.month}-models`
        const allModels = expanded.has(modelsKey)
        const models = allModels ? month.models : month.models.slice(0, ROWS_FIRST)
        const machineTotal = month.machines.reduce((sum, row) => sum + row.value, 0)
        const machinePrompts = month.machines.reduce((sum, row) => sum + row.prompts, 0)
        const topModel = month.models[0]?.value ?? 0
        const modelCount = month.models.filter(row => row.model !== ACCOUNT_HISTORY).length
        const topMachine = month.machines[0]?.value ?? 0
        const machinesEstimated = month.machines.some(row => row.assigned > 0)
        return (
          <div key={month.month} className="gh-activity__month">
            <h3 className="gh-activity__divider">
              <span>
                <strong>{monthLong(m - 1)}</strong> {y}
              </span>
            </h3>
            <ol className="gh-timeline">
              {(month.total > 0 || month.prompts > 0) && (
                <Item
                  id={`gh-${modelsKey}`}
                  icon="tokens"
                  title={<span className="gh-timeline__context">{counts(month.total, month.prompts)}{modelCount > 0 ? ` across ${plural(modelCount, 'model')}${month.accountHistory > 0 ? ' + account history' : ''}` : month.accountHistory > 0 ? ' from account history' : ''}</span>}
                  folded={folded.has(modelsKey)}
                  onFold={fold(modelsKey)}
                >
                  <ul className="gh-rows">
                    {models.map((row) => (
                      <li key={`${row.provider}|${row.model}`} className="gh-row">
                        <span className="gh-row__main">
                          <span className="gh-row__name" title={`${modelLabel(row.model)} · ${PROVIDER_LABEL[row.provider]}`}>
                            {modelLabel(row.model)}
                          </span>
                          <span className="gh-row__count">{counts(row.value, row.prompts, true)}</span>
                        </span>
                        <Bar share={month.total > 0 ? row.value / month.total : 0} lead={row.value === topModel} />
                      </li>
                    ))}
                  </ul>
                  {month.accountHistory > 0 && (
                    <p className="agents-footnote">
                      {machinesEstimated
                        ? 'Account history has no model or project breakdown; its machine split below is an estimate.'
                        : 'Account history has no model, project or machine breakdown.'}
                    </p>
                  )}
                  {month.models.length > ROWS_FIRST && (
                    <button
                      type="button"
                      className="gh-link-btn"
                      aria-expanded={allModels}
                      onClick={() => setExpanded((prev) => toggle(prev, modelsKey))}
                    >
                      {allModels ? 'Show fewer models' : `Show ${month.models.length - ROWS_FIRST} more models`}
                    </button>
                  )}
                </Item>
              )}

              {month.machines.length > 0 && (
                <Item
                  id={`gh-${month.month}-machines`}
                  icon="machines"
                  title={`Ran on ${plural(month.machines.length, 'machine')}`}
                  folded={folded.has(`${month.month}-machines`)}
                  onFold={fold(`${month.month}-machines`)}
                >
                  <ul className="gh-rows">
                    {month.machines.map((row) => {
                      const share = machineTotal > 0 ? row.value / machineTotal : machinePrompts > 0 ? row.prompts / machinePrompts : 0
                      return (
                        <li key={row.key} className="gh-row">
                          <span className="gh-row__main">
                            <Flag cc={row.cc} />
                            <span className="gh-row__name">{row.label}</span>
                            {row.assigned > 0 && (
                              <span className="gh-row__est" title={`Partly estimated: ${assignedNote(row.assigned, row.assignedProviders)}`}>
                                est.
                              </span>
                            )}
                            <span className="gh-row__count">{counts(row.value, row.prompts)}</span>
                          </span>
                          <Bar share={share} lead={machineTotal > 0 ? row.value === topMachine : row === month.machines[0]} />
                        </li>
                      )
                    })}
                  </ul>
                </Item>
              )}

              {month.started.length > 0 && (
                <Item
                  id={`gh-${month.month}-started`}
                  icon="started"
                  title={month.started.length === 1 ? `Started using ${month.started[0].model}` : `Started using ${month.started.length} models`}
                  folded={folded.has(`${month.month}-started`)}
                  onFold={fold(`${month.month}-started`)}
                >
                  <ul className="gh-rows">
                    {month.started.map((row) => (
                      <li key={`${row.provider}|${row.model}`} className="gh-row gh-row--started">
                        <span className="gh-row__main">
                          <span className="gh-row__name">{row.model}</span>
                        </span>
                        <span className="gh-row__lang">
                          <span className={`gh-row__dot agents-swatch--${row.provider}`} aria-hidden="true" />
                          {PROVIDER_LABEL[row.provider]}
                        </span>
                        <time className="gh-row__date" dateTime={row.date}>
                          {formatDayShort(row.date, today).replace(/, \d{4}$/, '')}
                        </time>
                      </li>
                    ))}
                  </ul>
                </Item>
              )}

              {month.promptsNoUsage > 0 && (
                <Item
                  id={`gh-${month.month}-prompts`}
                  icon="prompts"
                  title={`${plural(month.promptsNoUsage, 'prompt')} with tokens not recorded`}
                  folded={false}
                />
              )}

              {month.firstRecorded && (
                <Item
                  id={`gh-${month.month}-first`}
                  icon="first"
                  title="First activity recorded"
                  aside={
                    <time className="gh-row__date" dateTime={month.firstRecorded}>
                      {formatDayShort(month.firstRecorded, today).replace(/, \d{4}$/, '')}
                    </time>
                  }
                  folded={false}
                />
              )}
            </ol>
          </div>
        )
      })}
      {shown < months.length && (
        <button type="button" className="gh-more" onClick={() => setShown((count) => count + MONTHS_MORE)}>
          Show more activity
        </button>
      )}
    </section>
  )
}

export default ActivityFeed
