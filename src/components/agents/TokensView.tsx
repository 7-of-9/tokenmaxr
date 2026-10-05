import { lazy, Suspense, useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import LoadingIndicator from '../LoadingIndicator'
import Heatmap from './Heatmap'
import MonthCalendar from './MonthCalendar'
import Numeral from './Numeral'
import ActivityFeed from './ActivityFeed'
import { buildActivity } from './activity'
import { dayCost, type DayCost } from './cost'
import { usePriceMode, usePricingTable } from './pricing'
import { useUsageSource } from './source'
import { useUsage } from './useUsage'
import { usePromptCounts } from './usePromptCounts'
import { applyPromptCounts } from './promptCounts'
import UsageLoading from './UsageLoading'
import { machineDays } from './machines'
import { addAssignedHistory, attributeAccountHistory, countryLookup } from './attribution'
import type { Metric } from './types'
import {
  activeDayCount,
  buildCells,
  dayModels,
  DEFAULT_VIEW,
  formatCompact,
  formatCount,
  METRIC_LABEL,
  METRIC_NOUN,
  normaliseDays,
  isRollingPeriod,
  periodRange,
  perActiveDay,
  plural,
  PROVIDER_LABEL,
  PROVIDERS,
  summarise,
  toIsoDate,
  yearsBack,
  type Period,
  type ProviderFilter,
  type ViewOptions,
} from './usage'
import './agents-base.css'
import './AgentsPage.css'

// The /tokens page body, shared by d0m1.com and the tokenmaxr GitHub Pages dashboard (pages/dashboard), both
// through AgentsPage (inside TokensShell). The UsageSource in context says where its data comes from.

// Detail (charts, pricing table, flags) loads only when someone asks for it.
const AgentsDetail = lazy(() => import('./AgentsDetail'))

/** The metric buttons' tooltips: what each count is. */
const METRIC_HELP: Record<Metric, string> = {
  tokens: 'in + out (incl. cached) + account history without a token split',
  effective: 'in + out (cached at 10%)',
  output: 'out only',
}


type View = 'overview' | 'detail'

/** "Counted by tokenmaxr": routed within the site, or out to an absolute URL. */
const CollectorLink = ({ href, children }: { href: string; children: ReactNode }) =>
  href.startsWith('/') ? <Link to={href}>{children}</Link> : <a href={href}>{children}</a>

const TokensView = () => {
  const [searchParams, setSearchParams] = useSearchParams()
  const source = useUsageSource()
  const mock = searchParams.get('mock') === '1'
  const machine = searchParams.get('machine') || 'all'
  const view: View = searchParams.get('view') === 'detail' ? 'detail' : 'overview'
  const [reloadKey, setReloadKey] = useState(0)
  const { data, error, loading, loadedAt, progress, failures } = useUsage(mock, reloadKey)
  const { data: promptCounts } = usePromptCounts(mock, reloadKey)

  const requestedPeriod = searchParams.get('period') ?? '30d'
  const period: Period = requestedPeriod === 'recent' || isRollingPeriod(requestedPeriod) || /^\d{4}$/.test(requestedPeriod) ? requestedPeriod as Period : '30d'
  const [filters, setFilters] = useState<ViewOptions>(DEFAULT_VIEW)
  // Overview uses every provider and the token counts supplied by the API. Account history (filed under no
  // machine) is assigned to the machines that most probably made it, as an estimate the panels mark.
  const normalised = useMemo(() => attributeAccountHistory(normaliseDays(data), data?.machines ?? []), [data])
  // A source that keeps each machine's own records scopes exactly (plus that machine's assigned account history);
  // otherwise split the fleet days.
  const scoped = useMemo(() => {
    const exact = machine === 'all' || !data || mock ? null : source.scopeMachine?.(data, machine)
    const cc = countryLookup(data?.machines ?? [])(machine)
    return exact ? { days: addAssignedHistory(normaliseDays(exact), normalised, machine, cc), partial: false } : machineDays(normalised, machine, cc)
  }, [normalised, machine, data, mock, source])
  const options = useMemo(() => view === 'detail' ? { ...filters, exactOnly: false } : DEFAULT_VIEW, [view, filters])

  const today = toIsoDate(loadedAt ? new Date(loadedAt) : new Date())
  // Missing token records stay missing. Legacy ?estimate=1 links cannot enable inference.
  const days = useMemo(() => applyPromptCounts(scoped.days, promptCounts, machine), [scoped.days, promptCounts, machine])
  const cells = useMemo(() => buildCells(days, options), [days, options])

  const firstDate = data?.firstDate || days[0]?.date || today
  const years = useMemo(() => yearsBack(firstDate, today), [firstDate, today])
  const activePeriod: Period = period === 'recent' || isRollingPeriod(period) || years.includes(period) ? period : '30d'
  const range = useMemo(() => periodRange(activePeriod, today), [activePeriod, today])
  const summary = useMemo(() => summarise(cells, range.from, range.to), [cells, range])
  const activeDays = useMemo(() => activeDayCount(days, options, range.from, range.to), [days, options, range])
  const accountHistory = useMemo(() => days.reduce((total, day) => {
    if (day.date < range.from || day.date > range.to) return total
    return total + (options.provider === 'all' ? PROVIDERS : [options.provider]).reduce((sum, provider) => {
      const p = day.providers[provider]
      return sum + (p?.exact.unattributed ?? 0) + (options.exactOnly ? 0 : p?.estimated.unattributed ?? 0)
    }, 0)
  }, 0), [days, options, range])
  const machines = useMemo(() => data?.machines?.filter(m => machine === 'all' || m.id === machine), [data?.machines, machine])
  const activity = useMemo(() => buildActivity(days, options, machines ?? [], range.from, range.to), [days, options, machines, range])
  // A calendar year is laid out to 31 December, so every period draws the same 53 weeks at the same width.
  const compactCalendar = isRollingPeriod(activePeriod)
  const Calendar = compactCalendar ? MonthCalendar : Heatmap
  const padTo = activePeriod === 'recent' || compactCalendar ? range.to : `${activePeriod}-12-31`

  // Each day's API-equivalent cost for the heatmap tooltip, in the pricing mode the Detail cost tile sets.
  const pricing = usePricingTable()
  const [priceMode] = usePriceMode()
  const dayCosts = useMemo(() => {
    if (!pricing) return null
    const costs = new Map<string, DayCost>()
    for (const day of days) {
      if (day.date < range.from || day.date > range.to) continue
      const models = dayModels(day, options)
      if (models.length > 0) costs.set(day.date, dayCost(day.date, models, pricing, priceMode))
    }
    return costs
  }, [pricing, days, options, range, priceMode])

  const noun = METRIC_NOUN[options.metric]
  const live = machine === 'all' ? data?.totals?.machinesLive ?? 0 : machines?.filter(m => m.live).length ?? 0
  const accounts = summary.accounts || (machine === 'all' ? (options.provider === 'all' ? data?.totals?.accounts : data?.totals?.accountsByProvider?.[options.provider]) : 0) || 0

  const setMachine = (next: string) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'all') params.delete('machine')
    else params.set('machine', next)
    setSearchParams(params, { replace: true })
  }

  const setView = (next: View) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'detail') params.set('view', 'detail')
    else params.delete('view')
    setSearchParams(params, { replace: true })
  }
  const setPeriod = (next: Period) => {
    const params = new URLSearchParams(searchParams)
    params.delete('estimate')
    if (next === '30d') params.delete('period')
    else params.set('period', next)
    setSearchParams(params, { replace: true })
  }
  const setFilter = <K extends keyof ViewOptions>(key: K, value: ViewOptions[K]) => setFilters((prev) => ({ ...prev, [key]: value }))

  const periods: Array<[Period, string]> = [
    ['30d', 'Last 30 days'], ['60d', 'Last 60 days'], ['90d', 'Last 90 days'],
    ['recent', 'Last 12 months'], ...years.map((year): [Period, string] => [year, year]),
  ]

  return (
    <>
      {mock && (
        <div className="agents-testdata" role="status">
          Test data: every number on this page is synthetic, for previewing the layout. None of it is real usage.
        </div>
      )}

      {/* One green sheet over the site background, laid out like a GitHub profile: the graph and its activity feed
          in the content column, the year list beside them. The sheet stays put; only its inside scrolls. */}
      <div className="agents-panel agents-sheet">
        <div className="agents-sheet__scroll">
          <div className="agents-layout">
            <div className="agents-main">
              <section className="agents-overview" aria-label="Tokens per day">
                <div className="agents-top">
                  <div className={`agents-headline-block${loading && data ? ' is-refreshing' : ''}`} aria-live="polite">
                    <h1 className="agents-headline" title="Both averages use days with recorded tokens. Prompts on days without recorded tokens are excluded from the daily average.">
                      <span className="agents-headline__figure"><Numeral text={data ? formatCount(perActiveDay(summary.total, activeDays)) : '–'} /></span> {noun}
                      <span className="agents-headline__separator" aria-hidden="true"> · </span>
                      <span className="agents-headline__figure"><Numeral text={data ? formatCount(perActiveDay(summary.activeDayPrompts, activeDays)) : '–'} /></span> prompts
                      <span className="agents-headline__period"> / active day</span>
                    </h1>
                    <p className="agents-headline__totals">
                      <span className="agents-headline__total">{data ? formatCount(summary.total) : '–'}</span> {noun}
                      {' · '}<span className="agents-headline__total">{data ? formatCount(summary.prompts) : '–'}</span> prompts
                      <span> in {range.phrase}</span>
                      {data && <span className="agents-headline__days" title="Days with recorded tokens. Empty days and days with only prompts are excluded."> · {plural(activeDays, 'active day')}</span>}
                    </p>
                  </div>
                  <div className="agents-top__end">
                    <select className="agents-machine" aria-label="Machine" value={machine} onChange={event => setMachine(event.target.value)}>
                      <option value="all">All machines</option>
                      {(data?.machines ?? []).map(m => <option key={m.id} value={m.id}>{m.label}</option>)}
                      {machine !== 'all' && !data?.machines?.some(m => m.id === machine) && <option value={machine}>Unknown machine</option>}
                    </select>
                    <div className="agents-seg" role="group" aria-label="View">
                      {(['overview', 'detail'] as View[]).map((value) => (
                        <button key={value} type="button" className="agents-seg__btn" aria-pressed={view === value} onClick={() => setView(value)}>
                          {value === 'overview' ? 'Overview' : 'Detail'}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>

                {/* Detail's filters sit over the graph they filter. */}
                {view === 'detail' && data && (
                  <div className="agents-controls" role="group" aria-label="Filters">
                    <div className="agents-seg agents-seg--small" role="group" aria-label="Provider">
                      {(['all', ...PROVIDERS] as ProviderFilter[]).map((value) => (
                        <button key={value} type="button" className="agents-seg__btn" aria-pressed={filters.provider === value} onClick={() => setFilter('provider', value)}>
                          {value !== 'all' && <span className={`agents-key agents-key--${value}`} aria-hidden="true" />}
                          {value === 'all' ? 'All' : PROVIDER_LABEL[value]}
                        </button>
                      ))}
                    </div>
                    <div className="agents-seg agents-seg--small" role="group" aria-label="Metric">
                      {(['tokens', 'effective', 'output'] as Metric[]).map((value) => (
                        <button
                          key={value}
                          type="button"
                          className="agents-seg__btn"
                          aria-pressed={filters.metric === value}
                          title={METRIC_HELP[value]}
                          onClick={() => setFilter('metric', value)}
                        >
                          {METRIC_LABEL[value]}
                        </button>
                      ))}
                    </div>
                  </div>
                )}

                {error && (!data || failures >= 3) && (
                  <div className="agents-notice" role="alert">
                    <p>
                      {data ? 'Showing the last good data. ' : ''}
                      {error}
                    </p>
                    <button type="button" className="agents-btn" onClick={() => setReloadKey((key) => key + 1)}>
                      Try again
                    </button>
                  </div>
                )}

                {scoped.partial && <p className="agents-muted" role="status">Some shared-workspace history has no per-machine model or quality split. Its tokens are included with the model marked unknown.</p>}
                {accountHistory > 0 && options.metric !== 'tokens' && <p className="agents-muted">{formatCompact(accountHistory)} account-history tokens have no input/output split and appear under Total only.</p>}

                <div className={`agents-graph${loading && data ? ' is-refreshing' : ''}`}>
                  {!data ? (
                    loading ? <UsageLoading progress={progress} source={source.loadingLabel} />
                      : <p className="agents-empty">{source.unavailableText ?? 'No usage to show until the API answers.'}</p>
                  ) : (
                    <>
                      <Calendar
                        key={activePeriod}
                        cells={cells}
                        from={range.from}
                        to={range.to}
                        padTo={padTo}
                        today={today}
                        noun={noun}
                        caption={`${noun} per day in ${range.phrase}`}
                        costs={dayCosts}
                        footnote={
                          <span className="agents-subline">
                            <span>in {formatCompact(summary.tokensIn)}</span>
                            <span>out {formatCompact(summary.tokensOut)}</span>
                            {summary.unattributed > 0 && <span title="Source-reported tokens without an input/output split">{formatCompact(summary.unattributed)} account history</span>}
                            {machine === 'all' && <span>{plural(accounts, 'account')}</span>}
                            <span>{plural(summary.providers, 'provider')}</span>
                            <span className="agents-subline__live">
                              {live > 0 && <span className="agents-live" aria-hidden="true" />}
                              {live} live
                            </span>
                          </span>
                        }
                      />
                      {cells.size === 0 && (
                        <p className="agents-empty">
                          Nothing counted yet. <CollectorLink href={source.collectorHref}>Install the collector</CollectorLink> and its history fills in here.
                        </p>
                      )}
                    </>
                  )}
                </div>
              </section>

              {view === 'overview' && data && <ActivityFeed key={activePeriod} months={activity} today={today} noun={noun} />}

              {view === 'detail' && data && (
                <Suspense fallback={<LoadingIndicator label="Loading detail…" />}>
                  <AgentsDetail
                    days={days}
                    cells={cells}
                    view={options}
                    range={range}
                    machines={machines ?? []}
                    today={today}
                    noun={noun}
                    activeDays={activeDays}
                  />
                </Suspense>
              )}
            </div>

            <nav className="agents-years" aria-label="Period">
              {periods.map(([value, label]) => (
                <button key={value} type="button" className="agents-years__btn" aria-pressed={activePeriod === value} onClick={() => setPeriod(value)}>
                  {label}
                </button>
              ))}
            </nav>
          </div>
        </div>
      </div>

      <footer className="agents-foot">
        <CollectorLink href={source.collectorHref}>Counted by tokenmaxr</CollectorLink>
        {source.publicHref && <span> · <a href={source.publicHref} target="_blank" rel="noopener noreferrer">Public page on GitHub ↗</a></span>}
        <span> · {mock ? 'Mock data (no API)' : source.label}</span>
        {(data?.accountUsagePending ?? 0) > 0 && <span role="status"> · Account history: {plural(data!.accountUsagePending!, 'day')} awaiting reconciliation</span>}
        {(data?.accountUsageConflicts ?? 0) > 0 && <span role="status"> · Some account history excluded: totals conflict with local records</span>}
        {data && error && failures < 3 && <span role="status"> · Refresh delayed; retrying…</span>}
        {data && loading && <span> · <LoadingIndicator label="Refreshing…" inline /></span>}
        {data?.generatedAt && (
          <span> · updated {new Date(data.generatedAt).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</span>
        )}
      </footer>
    </>
  )
}

export default TokensView
