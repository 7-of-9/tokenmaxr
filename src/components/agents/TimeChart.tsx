import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import ChartTooltip from './ChartTooltip'
import type { Provider } from './types'
import {
  formatCompact,
  formatDayShort,
  formatMonth,
  monthShort,
  PROVIDER_LABEL,
  type Granularity,
  type SeriesBucket,
} from './usage'

interface TimeChartProps {
  buckets: SeriesBucket[]
  granularity: Granularity
  providers: Provider[]
  today: string
  noun: string
}

const HEIGHT = 220
/** Room for 13 px tick labels ("7.5B", "250M") left of the plot, and under it. */
const AXIS_W = 50
const AXIS_H = 24

/** Linear y axis: the smallest readable step (1, 2, 2.5, 4, 5 × 10ⁿ) that covers the peak in at most four steps, so 11.3B reads 0 · 4B · 8B · 12B. */
function linearTicks(peak: number) {
  if (peak <= 0) return { top: 1, ticks: [1] }
  let power = 10 ** Math.floor(Math.log10(peak / 4))
  for (;;) {
    for (const mantissa of [1, 2, 2.5, 4, 5]) {
      const step = mantissa * power
      const count = Math.ceil(peak / step - 1e-9)
      if (count <= 4) return { top: count * step, ticks: Array.from({ length: count }, (_, i) => (i + 1) * step) }
    }
    power *= 10
  }
}

/** Every bucket with tokens stays visible: a quiet week is drawn at least this tall. */
const MIN_BAR = 2

const percent = (share: number) => (share >= 0.995 ? '100%' : share < 0.01 ? '<1%' : `${Math.round(share * 100)}%`)

/** A bar segment with rounded top corners (the data end); square where it sits on another segment. */
function segmentPath(x: number, y: number, w: number, h: number, roundTop: boolean) {
  const r = roundTop ? Math.min(3, w / 2, h) : 0
  if (r <= 0) return `M${x},${y}h${w}v${h}h${-w}z`
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}z`
}

const TimeChart = ({ buckets, granularity, providers, today, noun }: TimeChartProps) => {
  const boxRef = useRef<HTMLDivElement>(null)
  const plotRef = useRef<SVGSVGElement>(null)
  const [width, setWidth] = useState(0)
  // The box may be taller than HEIGHT (a Detail tile stretched to its row-mate's height): the plot fills it.
  const [boxH, setBoxH] = useState(0)
  const [hover, setHover] = useState<{ index: number; anchor: DOMRect } | null>(null)

  useLayoutEffect(() => {
    const el = boxRef.current
    if (!el) return
    setWidth(el.clientWidth)
    setBoxH(el.clientHeight)
    const observer = new ResizeObserver(([entry]) => {
      setWidth(Math.floor(entry.contentRect.width))
      setBoxH(Math.floor(entry.contentRect.height))
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  useEffect(() => setHover(null), [buckets])

  const plotW = Math.max(0, width - AXIS_W)
  const chartH = Math.max(HEIGHT, boxH)
  const plotH = chartH - AXIS_H
  const { top, ticks: yTicks } = linearTicks(Math.max(0, ...buckets.map((b) => b.total)))
  const band = buckets.length ? plotW / buckets.length : 0
  const gap = band >= 8 ? 2 : band >= 3 ? 1 : 0
  const barW = Math.max(1, band - gap)
  const y = (value: number) => plotH - (Math.max(0, value) / top) * plotH
  /** Drawn bar height: linear, but never under MIN_BAR for a bucket with tokens. */
  const barHeight = (total: number) => (total > 0 ? Math.max(MIN_BAR, (total / top) * plotH) : 0)

  const anchorFor = (index: number) => {
    const svg = plotRef.current
    if (!svg) return null
    const box = svg.getBoundingClientRect()
    const b = buckets[index]
    const barTop = plotH - barHeight(b.total)
    return new DOMRect(box.left + AXIS_W + index * band, box.top + barTop, Math.max(band, 1), Math.max(1, plotH - barTop))
  }

  const show = (index: number) => {
    const anchor = anchorFor(index)
    if (anchor) setHover({ index, anchor })
  }

  const onPointerMove = (event: PointerEvent<SVGSVGElement>) => {
    const box = event.currentTarget.getBoundingClientRect()
    const index = Math.floor((event.clientX - box.left - AXIS_W) / band)
    if (index >= 0 && index < buckets.length && index !== hover?.index) show(index)
  }

  const onKeyDown = (event: KeyboardEvent<SVGSVGElement>) => {
    const current = hover?.index ?? buckets.length - 1
    const moves: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, Home: -Infinity, End: Infinity }
    if (!(event.key in moves)) return
    event.preventDefault()
    show(Math.min(buckets.length - 1, Math.max(0, current + moves[event.key])))
  }

  // Month ticks: the first bucket of each month; thinned so labels never collide.
  const ticks: Array<{ index: number; text: string }> = []
  let previous = ''
  buckets.forEach((b, index) => {
    const month = (granularity === 'week' ? b.end : b.start).slice(0, 7)
    if (month === previous) return
    previous = month
    const m = Number(month.slice(5, 7)) - 1
    ticks.push({ index, text: m === 0 ? month.slice(0, 4) : monthShort(m) })
  })
  const shown: typeof ticks = []
  for (const tick of ticks) {
    const last = shown[shown.length - 1]
    if (!last || (tick.index - last.index) * band >= 42) shown.push(tick)
  }

  const when = (b: SeriesBucket) =>
    granularity === 'day'
      ? `on ${formatDayShort(b.start, today)}`
      : granularity === 'week'
        ? `in the week of ${formatDayShort(b.start, today)}`
        : `in ${formatMonth(b.start.slice(0, 7))}`
  const hovered = hover ? buckets[hover.index] : null

  // Each provider's share of the range, so a sliver too thin to see still reads as present.
  const rangeTotal = buckets.reduce((sum, b) => sum + b.total, 0)
  const shares = providers
    .map((provider): [Provider, number] => [provider, rangeTotal > 0 ? buckets.reduce((sum, b) => sum + b.byProvider[provider], 0) / rangeTotal : 0])
    .filter(([, share]) => share > 0)
  // One series is drawn in GitHub green; only a real stack needs the (muted) provider hues.
  const single = shares.length <= 1

  return (
    <figure className="agents-chart">
      <div className="agents-chart__box" ref={boxRef}>
        {width > 0 && (
          <svg
            ref={plotRef}
            width={width}
            height={chartH}
            className="agents-chart__svg"
            role="img"
            tabIndex={0}
            aria-label={`${noun} per ${granularity}${single ? '' : ', stacked by provider'}, on a linear scale up to ${formatCompact(top)}. Use arrow keys to read each ${granularity}.`}
            onPointerMove={onPointerMove}
            onPointerLeave={() => setHover(null)}
            onKeyDown={onKeyDown}
            onBlur={() => setHover(null)}
          >
            <line x1={AXIS_W} x2={width} y1={plotH} y2={plotH} className="agents-chart__base" />
            <text x={AXIS_W - 8} y={plotH + 4.5} className="agents-chart__tick" textAnchor="end">
              0
            </text>
            {yTicks.map((value) => (
              <g key={value}>
                <line x1={AXIS_W} x2={width} y1={y(value)} y2={y(value)} className="agents-chart__grid" />
                <text x={AXIS_W - 8} y={y(value) + 4.5} className="agents-chart__tick" textAnchor="end">
                  {formatCompact(value)}
                </text>
              </g>
            ))}
            {hover && <rect x={AXIS_W + hover.index * band} y={0} width={band} height={plotH} className="agents-chart__hover" />}
            {buckets.map((b, index) => {
              if (b.total <= 0) return null
              const x = AXIS_W + index * band + (band - barW) / 2
              const parts = providers.filter((p) => b.byProvider[p] > 0)
              // Linear stacking: each segment's height is its own share of the (at least MIN_BAR tall) bar.
              const perToken = barHeight(b.total) / b.total
              let base = plotH
              return (
                <g key={b.start}>
                  {parts.map((provider, i) => {
                    const h = b.byProvider[provider] * perToken
                    const segY = base - h
                    base = segY
                    // A surface gap between stacked segments, where the bar is wide enough to show one.
                    const seam = i > 0 && barW >= 4 && h > 2 ? 1 : 0
                    return (
                      <path
                        key={provider}
                        d={segmentPath(x, segY, barW, Math.max(0.5, h - seam), i === parts.length - 1 && barW >= 4)}
                        className={`agents-chart__seg agents-chart__seg--${single ? 'single' : provider}`}
                      />
                    )
                  })}
                </g>
              )
            })}
            {shown.map((tick) => (
              <text key={tick.index} x={AXIS_W + tick.index * band} y={chartH - 5} className="agents-chart__tick">
                {tick.text}
              </text>
            ))}
          </svg>
        )}
      </div>
      {shares.length > 1 && (
        <figcaption className="agents-legend">
          {shares.map(([provider, share]) => (
            <span key={provider}>
              <span className={`agents-swatch agents-swatch--${provider}`} aria-hidden="true" />
              {PROVIDER_LABEL[provider]}
              <span className="agents-legend__share">{percent(share)}</span>
            </span>
          ))}
        </figcaption>
      )}
      {hover && hovered && (
        <ChartTooltip id="agents-chart-tip" anchor={hover.anchor}>
          <p className="agents-tooltip__lead">
            <strong>
              {formatCompact(hovered.total)} {noun}
            </strong>{' '}
            {when(hovered)}
          </p>
          {hovered.total > 0 && (
            <ul className="agents-tooltip__rows">
              {providers
                .filter((p) => hovered.byProvider[p] > 0)
                .map((provider) => (
                  <li key={provider}>
                    <span className={`agents-key agents-key--${provider}`} aria-hidden="true" />
                    <span className="agents-tooltip__row-label">{PROVIDER_LABEL[provider]}</span>
                    <span className="agents-tooltip__row-value">{formatCompact(hovered.byProvider[provider])}</span>
                  </li>
                ))}
            </ul>
          )}
        </ChartTooltip>
      )}
    </figure>
  )
}

export default TimeChart
