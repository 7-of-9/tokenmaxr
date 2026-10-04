import { useEffect, useMemo, useState } from 'react'
import type { PublishedMeter } from './githubSource'
import { quotaMeters } from './quota'

interface QuotaCardProps {
  load: (signal: AbortSignal) => Promise<PublishedMeter[]>
  /** The page's selected machine, or 'all'. */
  machine: string
  pollMs: number
}

/** Plan quota as each tool last reported it. Optional: a failed load keeps the last meters and never blocks usage. */
const QuotaCard = ({ load, machine, pollMs }: QuotaCardProps) => {
  const [meters, setMeters] = useState<PublishedMeter[]>([])
  const [now, setNow] = useState(Date.now)

  useEffect(() => {
    let controller: AbortController | null = null
    const refresh = () => {
      controller?.abort()
      const request = (controller = new AbortController())
      load(request.signal).then(next => { if (!request.signal.aborted) setMeters(next) }, () => { /* keep the last meters */ })
    }
    refresh()
    const timer = window.setInterval(refresh, pollMs)
    return () => {
      window.clearInterval(timer)
      controller?.abort()
    }
  }, [load, pollMs])

  // "read 4 min ago" and countdowns stay honest on a page left open.
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(timer)
  }, [])

  const views = useMemo(() => quotaMeters(meters, now, machine), [meters, now, machine])
  if (!views.length) return null
  return (
    <section className="pages-quota" aria-label="Plan quota">
      <h2>Plan quota <span className="pages-quota__hint">as each tool last reported it</span></h2>
      <ul className="pages-quota__list">
        {views.map(m => (
          <li key={m.key} className="pages-meter">
            <div className="pages-meter__who">
              <span className="pages-meter__name">{m.name}</span>
              {m.plan && <span className="pages-pill">{m.plan}</span>}
              {m.account && <span className="pages-pill" title="Account alias on this page">{m.account}</span>}
            </div>
            <div className="pages-meter__use">
              <span className="pages-meter__pct">{m.usedPercent === null ? '–' : `${Math.round(m.usedPercent)}%`}</span>
              <span className="pages-meter__track" role="meter" aria-label={`${m.name}: used`} aria-valuemin={0} aria-valuemax={100}
                aria-valuenow={m.usedPercent ?? undefined}>
                <span className="pages-meter__fill" style={{ width: `${m.usedPercent ?? 0}%`, background: m.color }} />
              </span>
            </div>
            <div className="pages-meter__when">
              <span>{m.resetSinceRead ? 'reset since read' : m.resets}</span>
              <span className={m.stale ? 'pages-meter__stale' : undefined}>{m.read}</span>
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}

export default QuotaCard
