import { useMemo, useState } from 'react'
import pricing from '../../data/modelPricing.json'
import { costRows, formatUsd, type CostRow, type PriceMode, type PricingTable } from './cost'
import Flag from './Flag'
import Numeral from './Numeral'
import { usePriceMode } from './pricing'
import TimeChart from './TimeChart'
import type { PublicMachine } from './types'
import {
  ACCOUNT_HISTORY,
  formatCompact,
  inputOf,
  modelLabel,
  modelTotals,
  perActiveDay,
  placeTotals,
  plural,
  PROVIDER_LABEL,
  PROVIDERS,
  timeSeries,
  type DayCell,
  type Granularity,
  type NormDay,
  type PeriodRange,
  type ViewOptions,
} from './usage'
import './AgentsDetail.css'

const TABLE = pricing as unknown as PricingTable

interface AgentsDetailProps {
  days: NormDay[]
  cells: Map<string, DayCell>
  view: ViewOptions
  range: PeriodRange
  machines: PublicMachine[]
  today: string
  noun: string
  activeDays: number
}

const GRANULARITIES: Array<[Granularity, string]> = [
  ['day', 'Day'],
  ['week', 'Week'],
  ['month', 'Month'],
]

let regionNames: Intl.DisplayNames | null = null
function countryName(cc: string) {
  if (!cc || cc === 'ZZ') return 'Unknown'
  try {
    regionNames ??= new Intl.DisplayNames(['en'], { type: 'region' })
    return regionNames.of(cc) ?? cc
  } catch {
    return cc
  }
}

const MODEL_ROWS = 8

const PRICE_MODES: Array<[PriceMode, string]> = [
  ['at-the-time', 'at the time'],
  ['today', 'today'],
]

/** The cost cell's hover: the other mode's figure when it differs, and any days left unpriced. */
function costHint(row: CostRow, mode: PriceMode) {
  const other = mode === 'today' ? row.usdAtTheTime : row.usdToday
  const otherLabel = mode === 'today' ? 'at the time' : 'at today’s prices'
  const parts: string[] = []
  if (row.usd === null) parts.push(row.match && mode === 'today' ? 'No list price today' : 'No list price')
  if (other === null && row.usd !== null) parts.push(mode === 'today' ? 'no list price at the time' : 'no list price today')
  else if (other !== null && (row.usd === null || formatUsd(other) !== formatUsd(row.usd))) parts.push(`${formatUsd(other)} ${otherLabel}`)
  else if (other !== null && row.usd !== null && !row.partial) parts.push('Same cost in both price views')
  if (row.partial) parts.push('some days have no list price')
  return parts.length ? parts.join('; ') : undefined
}

const percent = (share: number) => (share >= 0.995 ? '100%' : share < 0.01 ? '<1%' : `${Math.round(share * 100)}%`)

interface ShareRow {
  key: string
  cc: string
  label: string
  live?: boolean
  value: number
  prompts: number
}

const ShareList = ({ rows, noun, title }: { rows: ShareRow[]; noun: string; title: string }) => {
  const total = rows.reduce((sum, row) => sum + row.value, 0)
  if (rows.length === 0) return <p className="agents-muted">Nothing counted in this period.</p>
  return (
    <ul className="agents-share" aria-label={title}>
      {rows.map((row) => {
        const share = total > 0 ? row.value / total : 0
        return (
          <li key={row.key} className="agents-share__row">
            <Flag cc={row.cc} title={countryName(row.cc)} />
            <span className="agents-share__label">
              <span className="agents-share__name">{row.label}</span>
              {row.live && (
                <span className="agents-share__live" title="Reported in the last few minutes">
                  <span className="sr-only">live</span>
                </span>
              )}
            </span>
            {row.value > 0 ? (
              <>
                <span className="agents-share__value" title={`${formatCompact(row.value)} ${noun}`}>
                  {formatCompact(row.value)}
                </span>
                <span className="agents-share__pct">{percent(share)}</span>
              </>
            ) : (
              <span className="agents-share__value agents-share__value--muted">{plural(row.prompts, 'prompt')}</span>
            )}
            <span className="agents-share__track" aria-hidden="true">
              {share > 0 && <span className={`gh-bar__fill${row === rows[0] ? ' is-lead' : ''}`} style={{ width: `${Math.max(1, share * 100)}%` }} />}
            </span>
          </li>
        )
      })}
    </ul>
  )
}

const AgentsDetail = ({ days, cells, view, range, machines, today, noun, activeDays }: AgentsDetailProps) => {
  const [granularity, setGranularity] = useState<Granularity>('week')
  const [allModels, setAllModels] = useState(false)
  const [priceMode, setPriceMode] = usePriceMode()
  const providers = view.provider === 'all' ? PROVIDERS : [view.provider]

  const series = useMemo(() => timeSeries(cells, range.from, range.to, granularity), [cells, range, granularity])

  const cost = useMemo(() => {
    const models = modelTotals(days, view, range.from, range.to)
    return costRows(models, TABLE, priceMode)
  }, [days, view, range, priceMode])
  const daily = perActiveDay(cost.totalUsd, activeDays)
  const hasPricedUsage = cost.rows.some(row => row.usd !== null)
  const hasUnpricedUsage = cost.rows.some(row => row.usd === null || row.partial)
  const accountHistory = cost.rows.reduce((sum, row) => sum + (row.tokens.unattributed ?? 0), 0)
  const otherDaily = perActiveDay(priceMode === 'today' ? cost.totalAtTheTime : cost.totalToday, activeDays)

  const workstations = useMemo<ShareRow[]>(() => {
    const byId = new Map(machines.map((m) => [m.id, m]))
    return placeTotals(days, view, range.from, range.to, 'byMachine').map((t) => {
      const m = byId.get(t.key)
      return { key: t.key, cc: m?.cc || 'ZZ', label: m?.label || (t.key === 'unknown' ? 'Machine unknown' : 'Unnamed machine'), live: m?.live, value: t.value, prompts: t.prompts }
    })
  }, [days, view, range, machines])

  const regions = useMemo<ShareRow[]>(
    () =>
      placeTotals(days, view, range.from, range.to, 'byCountry').map((t) => ({
        key: t.key,
        cc: t.key,
        label: countryName(t.key),
        value: t.value,
        prompts: t.prompts,
      })),
    [days, view, range],
  )

  const shownRows = allModels ? cost.rows : cost.rows.slice(0, MODEL_ROWS)
  const maxUsd = cost.rows.reduce((max, row) => Math.max(max, row.usd ?? 0), 0)

  return (
    <div className="agents-detail">
      <section className="agents-section agents-box" aria-labelledby="agents-cost-title">
        <div className="agents-section__head">
          <h2 id="agents-cost-title">API-equivalent cost</h2>
          <div className="agents-prices" role="group" aria-label="Prices">
            <span className="agents-prices__label" aria-hidden="true">
              Prices
            </span>
            <div className="agents-seg agents-seg--small">
              {PRICE_MODES.map(([value, label]) => (
                <button key={value} type="button" className="agents-seg__btn" aria-pressed={priceMode === value} onClick={() => setPriceMode(value)}>
                  {label}
                </button>
              ))}
            </div>
          </div>
        </div>
        <p className="agents-cost__total">
          <span
            className="agents-figure agents-cost__usd-total"
            title={
              formatUsd(otherDaily) !== formatUsd(daily)
                ? `${formatUsd(otherDaily)} / active day ${priceMode === 'today' ? 'at the time' : 'at today’s prices'}`
                : undefined
            }
          >
            {hasUnpricedUsage && hasPricedUsage && '≥ '}
            <Numeral text={hasPricedUsage ? formatUsd(daily) : '—'} />
            <span className="agents-figure__unit"> / active day</span>
          </span>
          <span className="agents-figure-caption agents-cost__caption">
            {hasPricedUsage ? `${hasUnpricedUsage ? '≥ ' : ''}${formatUsd(cost.totalUsd)}` : 'Unpriced'} {range.phrase === 'the last year' ? 'over the last year' : `in ${range.phrase}`}
            {' · '}{plural(activeDays, 'active day')}
          </span>
        </p>
        {cost.rows.length === 0 ? (
          <p className="agents-muted">No model-level tokens in this period.</p>
        ) : (
          <div className="agents-table-scroll">
            <table className="agents-cost">
              <thead>
                <tr>
                  <th scope="col" className="agents-cost__col-model">
                    Model
                  </th>
                  <th scope="col" className="agents-cost__col-num">
                    In
                  </th>
                  <th scope="col" className="agents-cost__col-num">
                    Out
                  </th>
                  <th scope="col" className="agents-cost__col-num agents-cost__cached" title="Share of input read from cache">
                    Cached
                  </th>
                  <th scope="col" className="agents-cost__col-usd">
                    Cost
                  </th>
                </tr>
              </thead>
              <tbody>
                {shownRows.map((row) => {
                  const input = inputOf(row.tokens)
                  const totalOnly = row.model === ACCOUNT_HISTORY
                  const family = row.match ? (TABLE.models[row.match.id]?.displayName ?? row.match.id) : null
                  const title = totalOnly ? `${formatCompact(row.tokens.unattributed ?? 0)} source-reported tokens; token split and cost unknown` : !family
                    ? `${row.model} · ${PROVIDER_LABEL[row.provider]} · no list price`
                    : row.match?.via === 'family'
                      ? `${row.model} · priced as ${family}`
                      : family
                  const share = row.usd !== null && maxUsd > 0 ? row.usd / maxUsd : 0
                  return (
                    <tr key={`${row.provider}|${row.model}`}>
                      <th scope="row">
                        <span className="agents-cost__name">
                          <span className={`agents-key agents-key--${row.provider}`} aria-hidden="true" />
                          <span className="agents-cost__model" title={title}>
                            {modelLabel(row.model)}{totalOnly && ` · ${formatCompact(row.tokens.unattributed ?? 0)}`}
                          </span>
                        </span>
                      </th>
                      <td>{totalOnly ? '—' : formatCompact(input)}</td>
                      <td>{totalOnly ? '—' : formatCompact(row.tokens.out)}</td>
                      <td className="agents-cost__cached" title={row.provider === 'cursor' ? 'Cursor does not report cache reads' : undefined}>
                        {input > 0 && row.provider !== 'cursor' ? percent(row.tokens.cacheR / input) : '—'}
                      </td>
                      <td className="agents-cost__usd" title={costHint(row, priceMode)}>
                        <span className="agents-cost__usd-cell">
                          <span className="agents-cost__meter" aria-hidden="true">
                            {share > 0 && (
                              <span className={`gh-bar__fill${share >= 1 ? ' is-lead' : ''}`} style={{ width: `${Math.max(1.5, share * 100)}%` }} />
                            )}
                          </span>
                          <span className="agents-cost__usd-value">{row.usd === null ? '—' : formatUsd(row.usd)}</span>
                        </span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
            {cost.rows.length > MODEL_ROWS && (
              <button type="button" className="agents-more" aria-expanded={allModels} onClick={() => setAllModels((open) => !open)}>
                {allModels ? 'Show fewer models' : `Show all ${cost.rows.length} models`}
              </button>
            )}
          </div>
        )}
        <p className="agents-footnote">
          {priceMode === 'today'
            ? 'Standard API pricing at today’s list prices; long-context surcharges not applied.'
            : 'Standard API list prices at the time of use; long-context surcharges not applied.'}
          {hasUnpricedUsage && ' Unpriced usage is excluded from the cost totals and daily average.'}
          {accountHistory > 0 && ` ${formatCompact(accountHistory)} account-history tokens cannot be priced without a model and token split.`}
        </p>
      </section>

      <section className="agents-section agents-box" aria-labelledby="agents-time-title">
        <div className="agents-section__head">
          <h2 id="agents-time-title">Over time</h2>
          <div className="agents-seg agents-seg--small" role="group" aria-label="Granularity">
            {GRANULARITIES.map(([value, label]) => (
              <button key={value} type="button" className="agents-seg__btn" aria-pressed={granularity === value} onClick={() => setGranularity(value)}>
                {label}
              </button>
            ))}
          </div>
        </div>
        <TimeChart buckets={series} granularity={granularity} providers={providers} today={today} noun={noun} />
      </section>

      <div className="agents-places">
        <section className="agents-section agents-box" aria-labelledby="agents-machines-title">
          <div className="agents-section__head">
            <h2 id="agents-machines-title">By workstation</h2>
          </div>
          <ShareList rows={workstations} noun={noun} title="Workstations" />
        </section>
        <section className="agents-section agents-box" aria-labelledby="agents-regions-title">
          <div className="agents-section__head">
            <h2 id="agents-regions-title">By region</h2>
          </div>
          <ShareList rows={regions} noun={noun} title="Regions" />
        </section>
      </div>
    </div>
  )
}

export default AgentsDetail
