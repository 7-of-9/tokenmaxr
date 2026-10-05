// The GitHub Pages dashboard's header (owner direction 2026-10-05: "render it NEUTRALLY, so a github-like top
// banner, no background at all; idea is to make it look as much as possible like a part of GH"): github.com's dark
// bar with the GitHub mark, "<owner> / <repository>" as GitHub writes it (each a link to it on GitHub), and Sign in
// or Sign out as a GitHub button. Below it, where there is more than one page to show (the owner signed in, or a
// visitor on #/agents), the repository's tabs: Usage and Agents. TokensShell shows it in place of d0m1.com's
// "< d0m1 / tokens" bar (TokensSite.Header); github.css styles it.
import { Link, useLocation } from 'react-router-dom'
import { useTokensSite, type Owner } from '../../src/components/agents/site'
import type { HeaderCrumbs } from './site'

/** Octicons (MIT, github.com/primer/octicons), 16 px. */
const MARK_GITHUB = 'M8 0c4.42 0 8 3.58 8 8a8.013 8.013 0 0 1-5.45 7.59c-.4.08-.55-.17-.55-.38 0-.27.01-1.13.01-2.2 0-.75-.25-1.23-.54-1.48 1.78-.2 3.65-.88 3.65-3.95 0-.88-.31-1.59-.82-2.15.08-.2.36-1.02-.08-2.12 0 0-.67-.22-2.2.82-.64-.18-1.32-.27-2-.27-.68 0-1.36.09-2 .27-1.53-1.03-2.2-.82-2.2-.82-.44 1.1-.16 1.92-.08 2.12-.51.56-.82 1.28-.82 2.15 0 3.06 1.86 3.75 3.64 3.95-.23.2-.44.55-.51 1.07-.46.21-1.61.55-2.33-.66-.15-.24-.6-.83-1.23-.82-.67.01-.27.38.01.53.34.19.73.9.82 1.13.16.45.68 1.31 2.69.94 0 .67.01 1.3.01 1.49 0 .21-.15.45-.55.38A7.995 7.995 0 0 1 0 8c0-4.42 3.58-8 8-8Z'
const GRAPH = 'M1.5 1.75V13.5h13.75a.75.75 0 0 1 0 1.5H.75a.75.75 0 0 1-.75-.75V1.75a.75.75 0 0 1 1.5 0Zm14.28 2.53-5.25 5.25a.75.75 0 0 1-1.06 0L7 7.06 4.28 9.78a.751.751 0 0 1-1.042-.018.751.751 0 0 1-.018-1.042l3.25-3.25a.75.75 0 0 1 1.06 0L10 7.94l4.72-4.72a.751.751 0 0 1 1.042.018.751.751 0 0 1 .018 1.042Z'
const LOCK = 'M4 4a4 4 0 0 1 8 0v2h.25c.966 0 1.75.784 1.75 1.75v5.5A1.75 1.75 0 0 1 12.25 15h-8.5A1.75 1.75 0 0 1 2 13.25v-5.5C2 6.784 2.784 6 3.75 6H4Zm8.25 3.5h-8.5a.25.25 0 0 0-.25.25v5.5c0 .138.112.25.25.25h8.5a.25.25 0 0 0 .25-.25v-5.5a.25.25 0 0 0-.25-.25ZM10.5 6V4a2.5 2.5 0 1 0-5 0v2Z'

const Octicon = ({ path, size = 16, className }: { path: string; size?: number; className?: string }) => (
  <svg className={className} width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" focusable="false">
    <path d={path} />
  </svg>
)

export default function GithubHeader({ owner, crumbs }: { owner: Owner; crumbs: HeaderCrumbs }) {
  const { home, ownerPages, Controls } = useTokensSite()
  const { pathname } = useLocation()
  const tabs = owner.status === 'owner' || pathname !== home
    ? [{ label: 'Usage', to: home, icon: GRAPH }, ...ownerPages.map(page => ({ ...page, icon: LOCK }))]
    : []
  const { owner: user, repository } = crumbs

  return (
    <>
      <header className="pages-gh-header">
        <a className="pages-gh-header__mark" href="https://github.com" aria-label="GitHub">
          <Octicon path={MARK_GITHUB} size={32} />
        </a>
        <nav className="pages-gh-header__crumbs" aria-label="Breadcrumb">
          {user && (
            <>
              <a className="pages-gh-header__owner" href={user.href}>{user.label}</a>
              <span className="pages-gh-header__separator" aria-hidden="true">/</span>
            </>
          )}
          {repository.href
            ? <a className="pages-gh-header__repo" href={repository.href}>{repository.label}</a>
            : <Link className="pages-gh-header__repo" to={home}>{repository.label}</Link>}
        </nav>
        {owner.controls && (
          <div className="pages-gh-header__actions">
            <Controls owner={owner} />
          </div>
        )}
      </header>
      {tabs.length > 1 && (
        <nav className="pages-gh-tabs" aria-label="Dashboard">
          <ul>
            {tabs.map(tab => (
              <li key={tab.to}>
                <Link to={tab.to} aria-current={pathname === tab.to ? 'page' : undefined}>
                  <Octicon path={tab.icon} />
                  <span data-content={tab.label}>{tab.label}</span>
                </Link>
              </li>
            ))}
          </ul>
        </nav>
      )}
    </>
  )
}
