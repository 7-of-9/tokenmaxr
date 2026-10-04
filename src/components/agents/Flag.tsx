import { useEffect, useState } from 'react'

// One lazy chunk per country (country-flag-icons, MIT): the activity feed and Detail fetch only the flags
// they show, so no flag artwork sits in the page bundle. SVG rather than emoji, which Windows cannot draw.
const FLAGS = import.meta.glob<string>('../../../node_modules/country-flag-icons/string/3x2/[A-Z][A-Z].js', {
  import: 'default',
})

const loaders = new Map<string, () => Promise<string>>()
for (const [path, load] of Object.entries(FLAGS)) {
  const code = path.slice(-5, -3)
  loaders.set(code, load)
}

const cache = new Map<string, string>()

interface FlagProps {
  /** ISO 3166-1 alpha-2; "ZZ" or anything unknown draws a neutral globe. */
  cc: string
  title?: string
}

const Flag = ({ cc, title }: FlagProps) => {
  const code = (cc || '').toUpperCase()
  const [svg, setSvg] = useState(() => cache.get(code) ?? null)

  useEffect(() => {
    const load = loaders.get(code)
    if (!load) {
      setSvg(null)
      return
    }
    const hit = cache.get(code)
    if (hit) {
      setSvg(hit)
      return
    }
    let cancelled = false
    load()
      .then((markup) => {
        const url = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(markup)}`
        cache.set(code, url)
        if (!cancelled) setSvg(url)
      })
      .catch(() => {
        if (!cancelled) setSvg(null)
      })
    return () => {
      cancelled = true
    }
  }, [code])

  if (!loaders.has(code)) {
    return (
      <svg className="agents-flag agents-flag--none" viewBox="0 0 18 12" role={title ? 'img' : undefined} aria-label={title} aria-hidden={title ? undefined : true}>
        <circle cx="9" cy="6" r="4.25" />
        <path d="M4.75 6h8.5M9 1.75c1.4 1.3 1.4 7.2 0 8.5M9 1.75c-1.4 1.3-1.4 7.2 0 8.5" />
      </svg>
    )
  }
  return svg ? (
    <img className="agents-flag" src={svg} alt={title ?? ''} width={18} height={12} decoding="async" />
  ) : (
    <span className="agents-flag agents-flag--loading" aria-hidden="true" />
  )
}

export default Flag
