import {
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type PointerEvent,
  type ReactNode,
} from 'react'
import ChartTooltip from './ChartTooltip'
import DayTooltip from './DayTooltip'
import { formatDayLong } from './activity'
import { formatUsd, type DayCost } from './cost'
import { makeHeatScale, type HeatLevel } from './heat'
import {
  addDays,
  formatCompact,
  formatCount,
  isEstimatedDominant,
  isInferredDominant,
  isoToUtcMs,
  mondayOf,
  monthShort,
  plural,
  type DayCell,
} from './usage'

export interface HeatmapProps {
  cells: Map<string, DayCell>
  /** First and last visible dates (inclusive). */
  from: string
  to: string
  /** Lay the grid out through this date (31 December for a calendar year): days after `to` are drawn as blank cells. */
  padTo?: string
  today: string
  /** "tokens", "output tokens", … */
  noun: string
  caption: string
  /** Each day's API-equivalent cost in the chosen pricing mode; absent until the price table has loaded. */
  costs?: Map<string, DayCost> | null
  /** The legend row's left slot (GitHub's "Learn how we count contributions"). */
  footnote?: ReactNode
}

/** GitHub's geometry: 10 px cells, 3 px gaps, Monday-first week columns, 12 px labels. The cells grow to fill the
 *  box's width (keeping that ratio), so the fixed 53 columns always span the content column. */
const CELL = 10
const GAP = 3
const LABEL_FONT = 12
/** "Mon" at 12 px plus GitHub's gap before the first column. */
const LEFT = 32
/** Month labels sit on a 12 px line above the cells. */
const TOP = 22
const LABEL_Y = 13
/** Every period lays out this many week columns (a year or the last 12 months), so switching never resizes the graph. */
const COLUMNS = 53
/** The content column's max width caps the cells well before this. */
const MAX_STEP = 24
/** Narrow screens shrink the cells down to GitHub's own 10 px; below that the graph scrolls, starting at the newest week. */
const MIN_STEP = CELL + GAP
const CELL_SHARE = CELL / (CELL + GAP)
/** The scroll box pads 2 px on each side so a highlighted cell's outline is never clipped. */
const SCROLL_PAD = 4

interface Geometry {
  cell: number
  gap: number
  step: number
}

/** Cells fill the box's width (MIN_STEP to MAX_STEP a column); a box too narrow for 53 columns at MIN_STEP scrolls. */
function fitGeometry(avail: number, weeks: number): Geometry {
  if (!(avail > 0) || !Number.isFinite(avail)) return { cell: CELL, gap: GAP, step: CELL + GAP }
  const step = Math.min(MAX_STEP, Math.max(MIN_STEP, Math.floor(((avail - LEFT) / weeks) * 10) / 10))
  const cell = Math.round(step * CELL_SHARE * 10) / 10
  return { cell, gap: step - cell, step }
}

const weekCount = (from: string, to: string) =>
  Math.round((isoToUtcMs(mondayOf(to)) - isoToUtcMs(mondayOf(from))) / (7 * 86_400_000)) + 1

interface Placed {
  date: string
  x: number
  y: number
  cell: DayCell | undefined
  level: HeatLevel
  /** Prompts on a day with no recorded tokens: an empty cell with a green outline. */
  untracked: boolean
  /** Mostly estimated from unrecorded prompts ("Estimate unrecorded"): drawn hollow at its level. */
  inferred: boolean
}

/** "≈ $4.12", "≈ $4.12 + unpriced", or "unpriced" when none of the day's tokens has a list price. */
function costPhrase(cost: DayCost | undefined) {
  if (!cost) return null
  if (cost.usd > 0) return `≈ ${formatUsd(cost.usd)}${cost.unpriced ? ' + unpriced' : ''}`
  return cost.unpriced ? 'unpriced' : null
}

/** GitHub's sentence: "1,234,567 tokens on September 28th." */
function sentence(p: Placed, noun: string, today: string) {
  const when = formatDayLong(p.date, today)
  const total = p.cell?.total ?? 0
  if (total > 0) return `${formatCount(total)} ${total === 1 ? noun.replace(/s$/, '') : noun} on ${when}.`
  if (p.untracked) return `No ${noun} recorded on ${when}.`
  return `No ${noun} on ${when}.`
}

const Heatmap = ({ cells, from, to, padTo, today, noun, caption, costs, footnote }: HeatmapProps) => {
  const uid = useId().replace(/:/g, '')
  const scrollRef = useRef<HTMLDivElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const [focusDate, setFocusDate] = useState(to)
  const [hover, setHover] = useState<{ date: string; anchor: DOMRect; pinned: boolean } | null>(null)
  const [avail, setAvail] = useState(Infinity)

  // The box takes the layout's width (never the graph's), so the cells can size to fill it.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const measure = () => setAvail(el.clientWidth - SCROLL_PAD)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  const layoutTo = padTo && padTo > to ? padTo : to
  const gridStart = mondayOf(from)
  const weeks = Math.max(COLUMNS, weekCount(from, layoutTo))
  const { cell: size, gap, step } = useMemo(() => fitGeometry(avail, weeks), [avail, weeks])
  const left = LEFT
  const top = TOP
  const gridW = weeks * step - gap
  const width = left + gridW
  const height = top + 7 * step - gap
  const scrollable = width > avail + 0.5
  // GitHub rounds its 10 px cells by 2 px; larger cells keep that proportion.
  const radius = Math.max(2, Math.round(size * 0.2 * 2) / 2)

  const scale = useMemo(() => {
    const values: number[] = []
    for (const cell of cells.values()) if (cell.date >= from && cell.date <= to && cell.total > 0) values.push(cell.total)
    return makeHeatScale(values)
  }, [cells, from, to])

  const placed = useMemo(() => {
    const list: Placed[] = []
    for (let col = 0; col < weeks; col += 1) {
      for (let row = 0; row < 7; row += 1) {
        const date = addDays(gridStart, col * 7 + row)
        if (date < from || date > to) continue
        const cell = cells.get(date)
        const level = cell ? scale.level(cell.total) : 0
        list.push({
          date,
          x: left + col * step,
          y: top + row * step,
          cell,
          level,
          untracked: cell !== undefined && level === 0 && cell.promptsNoUsage > 0,
          inferred: cell !== undefined && level > 0 && isInferredDominant(cell),
        })
      }
    }
    return list
  }, [cells, from, to, weeks, gridStart, scale, left, top, step])

  // The rest of a calendar year still to come: blank, inert cells that keep the year's shape.
  const pads = useMemo(() => {
    const list: Array<{ date: string; x: number; y: number }> = []
    if (layoutTo <= to) return list
    for (let col = 0; col < weeks; col += 1) {
      for (let row = 0; row < 7; row += 1) {
        const date = addDays(gridStart, col * 7 + row)
        if (date > to && date <= layoutTo) list.push({ date, x: left + col * step, y: top + row * step })
      }
    }
    return list
  }, [to, layoutTo, weeks, gridStart, left, top, step])

  // GitHub labels a month over the first week column that starts in it; a clipped first month gives way.
  const monthLabels = useMemo(() => {
    const labels: Array<{ col: number; text: string }> = []
    let labelled = ''
    for (let col = 0; col < weeks; col += 1) {
      const monday = addDays(gridStart, col * 7)
      const first = col === 0 ? (from > monday ? from : monday) : monday
      if (first > layoutTo) break
      const month = first.slice(0, 7)
      if (month === labelled) continue
      labelled = month
      const text = monthShort(Number(month.slice(5, 7)) - 1)
      const last = labels[labels.length - 1]
      if (last && (col - last.col) * step < LABEL_FONT * 2.6) labels[labels.length - 1] = { col, text }
      else labels.push({ col, text })
    }
    return labels
  }, [gridStart, weeks, from, layoutTo, step])

  // Start at the newest week when the graph is wider than its box.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollLeft = el.scrollWidth
  }, [from, to, width, scrollable])

  useEffect(() => {
    setFocusDate((current) => (current < from || current > to ? to : current))
  }, [from, to])

  // The tooltip follows its cell on scroll and closes on an outside tap or Escape.
  const hoverOpen = hover !== null
  useEffect(() => {
    if (!hoverOpen) return
    const close = () => setHover(null)
    const follow = () =>
      setHover((prev) => {
        const target = prev && svgRef.current?.querySelector(`[data-date="${prev.date}"]`)
        return prev && target ? { ...prev, anchor: target.getBoundingClientRect() } : prev
      })
    const onPointerDown = (event: globalThis.PointerEvent) => {
      if (!(event.target instanceof Element) || !event.target.closest('.agents-heat__svg, .agents-tooltip')) close()
    }
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); close() }
    }
    // Capture, so every scroller counts: the page's own scroll box (TokensShell) as well as the graph's.
    document.addEventListener('scroll', follow, { passive: true, capture: true })
    window.addEventListener('resize', follow)
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('scroll', follow, { capture: true })
      window.removeEventListener('resize', follow)
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [hoverOpen])

  const dayAt = (target: EventTarget) => (target instanceof Element ? target.closest<SVGGElement>('[data-date]') : null)

  /** The day under a point, counting half of each gap as the neighbouring cell's, so small cells stay easy to tap. */
  const nearestDay = (clientX: number, clientY: number) => {
    const svg = svgRef.current
    if (!svg) return null
    const box = svg.getBoundingClientRect()
    const col = Math.floor((clientX - box.left - left + gap / 2) / step)
    const row = Math.floor((clientY - box.top - top + gap / 2) / step)
    if (col < 0 || col >= weeks || row < 0 || row > 6) return null
    return svg.querySelector<SVGGElement>(`[data-date="${addDays(gridStart, col * 7 + row)}"]`)
  }

  const onPointerOver = (event: PointerEvent<SVGSVGElement>) => {
    if (event.pointerType !== 'mouse' || hover?.pinned) return
    const day = dayAt(event.target)
    if (day?.dataset.date && day.dataset.date !== hover?.date) setHover({ date: day.dataset.date, anchor: day.getBoundingClientRect(), pinned: false })
  }

  const onPointerLeave = (event: PointerEvent<SVGSVGElement>) => {
    if (event.pointerType === 'mouse') setHover((prev) => (prev && !prev.pinned ? null : prev))
  }

  const onClick = (event: MouseEvent<SVGSVGElement>) => {
    const day = dayAt(event.target) ?? nearestDay(event.clientX, event.clientY)
    const date = day?.dataset.date
    if (!day || !date) return
    setFocusDate(date)
    setHover((prev) => (prev?.date === date && prev.pinned ? null : { date, anchor: day.getBoundingClientRect(), pinned: true }))
  }

  const onKeyDown = (event: KeyboardEvent<SVGSVGElement>) => {
    if (event.key === 'Enter' || event.key === ' ') {
      const target = svgRef.current?.querySelector<SVGGElement>(`[data-date="${focusDate}"]`)
      if (target) {
        event.preventDefault()
        setHover(previous => previous?.date === focusDate && previous.pinned ? null : { date: focusDate, anchor: target.getBoundingClientRect(), pinned: true })
      }
      return
    }
    const moves: Record<string, number> = { ArrowUp: -1, ArrowDown: 1, ArrowLeft: -7, ArrowRight: 7 }
    let next: string | null = null
    if (event.key in moves) next = addDays(focusDate, moves[event.key])
    else if (event.key === 'Home') next = from
    else if (event.key === 'End') next = to
    if (!next) return
    event.preventDefault()
    if (next < from) next = from
    if (next > to) next = to
    setFocusDate(next)
    svgRef.current?.querySelector<SVGGElement>(`[data-date="${next}"]`)?.focus()
  }

  const describe = (p: Placed) => {
    const parts = [sentence(p, noun, today).replace(/\.$/, '')]
    if (p.cell && p.cell.inferred > 0) parts.push(`about ${formatCompact(p.cell.inferred)} estimated; ${plural(p.cell.promptsNoUsage, 'unrecorded prompt')} on this day`)
    else if (p.cell && p.cell.promptsNoUsage > 0) parts.push(`${plural(p.cell.promptsNoUsage, 'prompt')} with tokens not recorded`)
    if (p.cell && !p.inferred && isEstimatedDominant(p.cell)) parts.push('mostly estimated')
    const cost = costPhrase(costs?.get(p.date))
    if (cost) parts.push(cost)
    return parts.join(', ')
  }

  const hasUntracked = placed.some((p) => p.untracked)
  const hasInferred = placed.some((p) => p.inferred)
  const tooltipId = `agents-heat-tip-${uid}`
  const hovered = hover ? placed.find((p) => p.date === hover.date) : undefined
  // The legend's squares are GitHub's: the graph's own cells, capped at 12 px.
  const key = Math.min(12, Math.max(CELL, Math.round(size)))
  const weekdayLabels = (
    [
      [0, 'Mon'],
      [2, 'Wed'],
      [4, 'Fri'],
    ] as const
  ).map(([row, text]) => (
    <text key={text} x={0} y={top + row * step + size / 2} dominantBaseline="central" className="agents-heat__label">
      {text}
    </text>
  ))

  return (
    <figure className="agents-heat" style={{ ['--heat-left' as string]: `${left}px` }}>
      {/* A graph that scrolls keeps its weekday labels pinned at the left while the weeks slide under a fade. */}
      {scrollable && (
        <svg className="agents-heat__weekdays" width={left} height={height} aria-hidden="true">
          {weekdayLabels}
        </svg>
      )}
      <div className={`agents-heat__scroll${scrollable ? ' is-scrollable' : ''}`} ref={scrollRef}>
        <svg
          ref={svgRef}
          className="agents-heat__svg"
          width={width}
          height={height}
          viewBox={`0 0 ${width} ${height}`}
          role="group"
          aria-label={`${caption}. Use arrow keys to move between days.`}
          onKeyDown={onKeyDown}
          onPointerOver={onPointerOver}
          onPointerLeave={onPointerLeave}
          onClick={onClick}
        >
          {monthLabels.map((label) => (
            <text key={`${label.col}-${label.text}`} x={left + label.col * step} y={LABEL_Y} className="agents-heat__label">
              {label.text}
            </text>
          ))}
          {!scrollable && weekdayLabels}

          {pads.length > 0 && (
            <g className="agents-heat__pads" aria-hidden="true">
              {pads.map((p) => (
                <rect key={p.date} x={p.x + 0.5} y={p.y + 0.5} width={size - 1} height={size - 1} rx={radius - 0.5} className="agents-heat__cell agents-heat__cell--l0" />
              ))}
            </g>
          )}

          {placed.map((p) => {
            const active = hover?.date === p.date
            return (
              <g
                key={p.date}
                data-date={p.date}
                data-level={p.level}
                className={`agents-heat__day${active ? ' is-active' : ''}`}
                tabIndex={p.date === focusDate ? 0 : -1}
                role="img"
                aria-label={describe(p)}
                aria-describedby={active ? tooltipId : undefined}
                onFocus={(event) => {
                  setFocusDate(p.date)
                  setHover({ date: p.date, anchor: event.currentTarget.getBoundingClientRect(), pinned: false })
                }}
                onBlur={() => setHover((prev) => (prev && !prev.pinned ? null : prev))}
              >
                {/* Inset by half the 1 px outline, so the outline sits inside the cell as GitHub's does. */}
                <rect
                  x={p.x + 0.5}
                  y={p.y + 0.5}
                  width={size - 1}
                  height={size - 1}
                  rx={radius - 0.5}
                  className={`agents-heat__cell agents-heat__cell--l${p.level}${p.untracked ? ' agents-heat__cell--untracked' : ''}${p.inferred ? ' agents-heat__cell--inferred' : ''}`}
                />
              </g>
            )
          })}
        </svg>
      </div>

      <figcaption className="agents-heat__legend">
        <span className="agents-heat__footnote">{footnote}</span>
        <span className="agents-heat__scale">
          {hasInferred && (
            <span className="agents-heat__key">
              <svg width={key} height={key} aria-hidden="true">
                <rect x={0.5} y={0.5} width={key - 1} height={key - 1} rx={1.5} className="agents-heat__cell agents-heat__cell--l2 agents-heat__cell--inferred" />
              </svg>
              estimated
            </span>
          )}
          {hasUntracked && (
            <span className="agents-heat__key">
              <svg width={key} height={key} aria-hidden="true">
                <rect x={0.5} y={0.5} width={key - 1} height={key - 1} rx={1.5} className="agents-heat__cell agents-heat__cell--l0 agents-heat__cell--untracked" />
              </svg>
              <span className="agents-heat__key-long">tokens </span>not recorded
            </span>
          )}
          <span className="agents-heat__word">Less</span>
          <span className="agents-heat__swatches" aria-hidden="true">
            {([0, 1, 2, 3, 4] as const).map((level) => (
              <svg key={level} width={key} height={key}>
                <rect x={0.5} y={0.5} width={key - 1} height={key - 1} rx={1.5} className={`agents-heat__cell agents-heat__cell--l${level}`} />
              </svg>
            ))}
          </span>
          <span className="agents-heat__word">More</span>
        </span>
      </figcaption>

      {hover && hovered && (
        <ChartTooltip id={tooltipId} anchor={hover.anchor} interactive={hover.pinned}>
          <DayTooltip cell={hovered.cell} date={hovered.date} today={today} noun={noun} cost={costPhrase(costs?.get(hovered.date))} />
        </ChartTooltip>
      )}
    </figure>
  )
}

export default Heatmap
