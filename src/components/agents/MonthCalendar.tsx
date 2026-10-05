import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import ChartTooltip from './ChartTooltip'
import DayTooltip from './DayTooltip'
import type { HeatmapProps } from './Heatmap'
import { calendarMonths } from './calendar'
import { formatUsd, type DayCost } from './cost'
import { GH_LEVELS, makeHeatScale } from './heat'
import { useTokensSite } from './site'
import { addDays, formatCompact, formatDayShort, formatMonth, plural } from './usage'
import './MonthCalendar.css'

const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']

function costPhrase(cost: DayCost | undefined) {
  if (!cost) return null
  if (cost.usd > 0) return `≈ ${formatUsd(cost.usd)}${cost.unpriced ? ' + unpriced' : ''}`
  return cost.unpriced ? 'unpriced' : null
}

/** A daily calendar for short trailing windows; it uses the same recorded counts and tooltip as the year view. */
export default function MonthCalendar({ cells, from, to, today, noun, caption, costs, footnote }: HeatmapProps) {
  const root = useRef<HTMLDivElement>(null)
  const tooltipId = useId()
  const [focusDate, setFocusDate] = useState(to)
  const [hover, setHover] = useState<{ date: string; anchor: DOMRect; pinned: boolean } | null>(null)
  const months = useMemo(() => calendarMonths(from, to), [from, to])
  const visibleCells = useMemo(() => [...cells.values()].filter(cell => cell.date >= from && cell.date <= to), [cells, from, to])
  const scale = useMemo(() => makeHeatScale(visibleCells.map(cell => cell.total)), [visibleCells])
  const hasUntracked = visibleCells.some(cell => cell.total === 0 && cell.promptsNoUsage > 0)
  const activeDate = focusDate < from || focusDate > to ? to : focusDate
  const levels = useTokensSite().heatLevels ?? GH_LEVELS

  const hoverOpen = hover !== null
  useEffect(() => {
    if (!hoverOpen) return
    const close = () => setHover(null)
    const follow = () => setHover(prev => {
      const target = prev && root.current?.querySelector(`[data-date="${prev.date}"]`)
      return prev && target ? { ...prev, anchor: target.getBoundingClientRect() } : null
    })
    const outside = (event: PointerEvent) => {
      if (!(event.target instanceof Element) || (!root.current?.contains(event.target) && !event.target.closest('.agents-tooltip'))) close()
    }
    const escape = (event: globalThis.KeyboardEvent) => { if (event.key === 'Escape') { event.preventDefault(); close() } }
    document.addEventListener('scroll', follow, { passive: true, capture: true })
    window.addEventListener('resize', follow)
    document.addEventListener('pointerdown', outside)
    document.addEventListener('keydown', escape)
    return () => {
      document.removeEventListener('scroll', follow, { capture: true })
      window.removeEventListener('resize', follow)
      document.removeEventListener('pointerdown', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [hoverOpen])

  const moveFocus = (event: KeyboardEvent<HTMLButtonElement>, date: string) => {
    const moves: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }
    let next = event.key in moves ? addDays(date, moves[event.key]) : event.key === 'Home' ? from : event.key === 'End' ? to : null
    if (!next) return
    event.preventDefault()
    if (next < from) next = from
    if (next > to) next = to
    setFocusDate(next)
    root.current?.querySelector<HTMLButtonElement>(`[data-date="${next}"]`)?.focus()
  }

  return (
    <figure className="agents-heat agents-calendar">
      <p className="agents-calendar__range">{formatDayShort(from, today)} – {formatDayShort(to, today)} · Daily totals</p>
      <div className="agents-calendar__months" ref={root} role="group" aria-label={`${caption}. Use arrow keys to move between days.`}>
        {months.map(month => (
          <section className="agents-calendar__month" key={month.month} aria-label={formatMonth(month.month)}>
            <h2 className="agents-calendar__heading">{formatMonth(month.month)}</h2>
            <div className="agents-calendar__grid">
              {WEEKDAYS.map(day => <span className="agents-calendar__weekday" key={day} aria-hidden="true">{day}</span>)}
              {month.days.map((day, index) => {
                if (!day) return <span key={`blank-${index}`} aria-hidden="true" />
                const number = Number(day.date.slice(8))
                if (!day.inRange) return <span className="agents-calendar__outside" key={day.date} aria-hidden="true">{number}</span>
                const cell = cells.get(day.date)
                const level = scale.level(cell?.total ?? 0)
                const untracked = level === 0 && (cell?.promptsNoUsage ?? 0) > 0
                const description = [
                  formatDayShort(day.date, today),
                  untracked ? `No ${noun} recorded` : `${formatCompact(cell?.total ?? 0)} ${noun}`,
                  (cell?.promptsNoUsage ?? 0) > 0 && `${plural(cell!.promptsNoUsage, 'prompt')} without token records`,
                ].filter(Boolean).join(', ')
                return (
                  <button
                    type="button"
                    key={day.date}
                    data-date={day.date}
                    className={`agents-calendar__day${untracked ? ' is-untracked' : ''}${hover?.date === day.date ? ' is-active' : ''}${level >= 3 ? ' is-bright' : ''}`}
                    style={{ backgroundColor: levels[level] }}
                    tabIndex={day.date === activeDate ? 0 : -1}
                    aria-label={description}
                    aria-current={day.date === today ? 'date' : undefined}
                    aria-describedby={hover?.date === day.date ? tooltipId : undefined}
                    onKeyDown={event => moveFocus(event, day.date)}
                    onPointerEnter={event => {
                      if (event.pointerType === 'mouse' && !hover?.pinned) setHover({ date: day.date, anchor: event.currentTarget.getBoundingClientRect(), pinned: false })
                    }}
                    onPointerLeave={() => setHover(prev => prev?.pinned ? prev : null)}
                    onFocus={event => {
                      setFocusDate(day.date)
                      setHover({ date: day.date, anchor: event.currentTarget.getBoundingClientRect(), pinned: false })
                    }}
                    onBlur={() => setHover(prev => prev?.pinned ? prev : null)}
                    onClick={event => {
                      const anchor = event.currentTarget.getBoundingClientRect()
                      setFocusDate(day.date)
                      setHover(prev => prev?.date === day.date && prev.pinned ? null : { date: day.date, anchor, pinned: true })
                    }}
                  >{number}</button>
                )
              })}
            </div>
          </section>
        ))}
      </div>
      <figcaption className="agents-heat__legend">
        <div className="agents-heat__footnote">{footnote}</div>
        <div className="agents-heat__scale">
          {hasUntracked && <span className="agents-heat__key"><span className="agents-calendar__swatch is-untracked" aria-hidden="true" />tokens not recorded</span>}
          <span>Less</span>
          <span className="agents-heat__swatches" aria-hidden="true">
            {levels.map(color => <span className="agents-calendar__swatch" key={color} style={{ backgroundColor: color }} />)}
          </span>
          <span>More</span>
        </div>
      </figcaption>
      {hover && (
        <ChartTooltip id={tooltipId} anchor={hover.anchor} interactive={hover.pinned}>
          <DayTooltip cell={cells.get(hover.date)} date={hover.date} today={today} noun={noun} cost={costPhrase(costs?.get(hover.date))} />
        </ChartTooltip>
      )}
    </figure>
  )
}
