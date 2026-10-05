import { Fragment, useEffect, useRef, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
// The GitHub Pages build (vite.pages.config.ts) swaps d0m1.com's footer for nothing.
import Footer from '../Footer'
import { useTokensSite } from './site'
import '../GalleryPage.css'
import './agents-base.css'

export interface Crumb {
  label: string
  /** Where the crumb links; the last crumb is the current page and never links. */
  to?: string
}

interface TokensShellProps {
  /** Path after the site's root ("d0m1 /"), e.g. [{ label: 'tokens', to: '/tokens' }, { label: 'prompts' }]. */
  crumbs: Crumb[]
  className?: string
  children: ReactNode
}

/**
 * The /projects frame for the token pages: the same fixed "< d0m1 / …" header (GalleryPage's own classes),
 * the same full-width scrolling page over the untouched background, and the site footer. One shell for both
 * sites: the TokensSite in context (site.ts) gives the breadcrumb's root, the owner's pages and the sign-in
 * (d0m1.com) or lock (the GitHub Pages dashboard) controls.
 */
const TokensShell = ({ crumbs, className = '', children }: TokensShellProps) => {
  const navigate = useNavigate()
  const location = useLocation()
  const site = useTokensSite()
  const owner = site.useOwner()
  const { home, Controls } = site
  const tokenRoute = location.pathname === home || location.pathname.startsWith(home.endsWith('/') ? home : `${home}/`)
  const showPrivatePages = owner.status === 'owner'
  const headerCrumbs = tokenRoute && showPrivatePages ? [{ label: 'tokens', to: home }] : crumbs
  const parent = crumbs.length > 1 ? (crumbs[crumbs.length - 2].to ?? site.root.to) : site.root.to

  // As on /projects, "<" is the browser's back; a page opened directly goes up one level instead. Above the Pages
  // dashboard's first page is the GitHub profile (site.root.href): "<" goes there, as it goes home on d0m1.com.
  // Esc (leave false) never leaves the site.
  const back = (leave = true) => {
    if (location.key !== 'default') navigate(-1)
    else if (parent !== location.pathname) navigate(parent)
    else if (leave && site.root.href) window.location.assign(site.root.href)
  }

  // Esc does what "<" does, as on /projects. It yields to text fields, to an open tooltip
  // (whose own Escape listener closes it) and to dialogs, and runs after their document listeners.
  const backRef = useRef(back)
  backRef.current = back
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return
      const target = event.target
      if (target instanceof HTMLElement && (target.isContentEditable || target.closest('input, textarea, select'))) return
      if (document.querySelector('.agents-tooltip, dialog[open], [role="dialog"]')) return
      event.preventDefault()
      backRef.current(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  return (
    <div className="gallery-page tokens-shell">
      <div className={`gallery-header header-visible${owner.controls ? ' tokens-header--auth' : ''}`}>
        <button type="button" className="back-button" onClick={() => back()} aria-label="Back">
          &lt;
        </button>
        <nav className="gallery-title" aria-label="Breadcrumb">
          {site.root.href ? (
            <a className="gallery-d0m1" href={site.root.href}>
              {site.root.label}
            </a>
          ) : (
            <Link className="gallery-d0m1" to={site.root.to}>
              {site.root.label}
            </Link>
          )}
          <span className="gallery-path-display">
            <span className="path-component root">/</span>
            {headerCrumbs.map((crumb, index) => {
              const last = index === headerCrumbs.length - 1
              const current = last && !(tokenRoute && showPrivatePages && location.pathname !== home)
              return (
                <Fragment key={crumb.label}>
                  {crumb.to && (!last || (tokenRoute && showPrivatePages)) ? (
                    <Link className="path-component" to={crumb.to} aria-current={current ? 'page' : undefined}>
                      {crumb.label}
                    </Link>
                  ) : (
                    <span className={`path-component${last ? ' last' : ''}`} aria-current={current ? 'page' : undefined}>
                      {crumb.label}
                    </span>
                  )}
                  {!last && <span className="path-separator">/</span>}
                </Fragment>
              )
            })}
          </span>
          {showPrivatePages && (
            <span className={`tokens-private-pages${tokenRoute ? '' : ' tokens-private-pages--aside'}`}>
              {tokenRoute && <span className="tokens-private-pages__separator" aria-hidden="true">/</span>}
              {site.ownerPages.map(page => (
                <Link key={page.to} to={page.to} aria-current={location.pathname === page.to ? 'page' : undefined}>{page.label}</Link>
              ))}
            </span>
          )}
        </nav>
        {owner.controls && (
          <div className="tokens-auth">
            <Controls owner={owner} />
          </div>
        )}
      </div>
      <main className={`agents-page ${className}`.trim()}>{children}</main>
      <Footer />
    </div>
  )
}

export default TokensShell
