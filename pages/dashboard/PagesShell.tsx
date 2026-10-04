import { useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'

const DEFAULT_TITLE = 'AI token usage'

interface PagesShellProps {
  /** tokenmaxr.json's title, via data/index.json (null until it loads). */
  loadTitle: () => Promise<string | undefined>
  children: ReactNode
}

/** The GitHub Pages frame: a plain header (no sign-in or owner pages: everything here is public) around the page. */
const PagesShell = ({ loadTitle, children }: PagesShellProps) => {
  const [searchParams] = useSearchParams()
  const view = searchParams.get('view') === 'detail' ? 'detail' : 'overview'
  const [title, setTitle] = useState(DEFAULT_TITLE)

  useEffect(() => {
    let live = true
    loadTitle().then(next => { if (live && next?.trim()) setTitle(next.trim().slice(0, 120)) }, () => { /* keep the default */ })
    return () => { live = false }
  }, [loadTitle])

  useEffect(() => {
    document.title = `${title} · tokenmaxr`
  }, [title])

  return (
    <div className="pages-shell">
      <header className="pages-header">
        <h1 className="pages-header__title">{title}</h1>
        <a className="pages-header__by" href="https://github.com/7-of-9/tokenmaxr">Published by tokenmaxr</a>
      </header>
      <main className={`agents-page agents-gh agents-page--${view}`}>{children}</main>
    </div>
  )
}

export default PagesShell
