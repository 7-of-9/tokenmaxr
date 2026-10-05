import { lazy, Suspense, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import RouteStage from '../../src/components/RouteStage'
import { TokensSiteContext } from '../../src/components/agents/site'
import { UsageSourceContext } from '../../src/components/agents/source'
import type { GithubSource } from './githubSource'
import type { OwnerStore, UnlockHandoff } from './owner'
import SignInPanel from './SignInPanel'
import type { DashboardConfig, DeviceSignIn } from './signin'
import { defaultTitle, githubUser, pagesSite, type ViewToggle } from './site'

// Split as App.tsx splits them on d0m1.com, so each page's styles arrive as they do there: a page opened directly
// has only its own sheets (AgentsPage.css restyles .agents-gh, which LimitsPage shares, once /tokens has loaded).
const AgentsPage = lazy(() => import('../../src/components/agents/AgentsPage'))
const LimitsPage = lazy(() => import('../../src/components/agents/LimitsPage'))

/**
 * d0m1.com's animated background and its controls (Background.tsx), streamed from d0m1.com's CDN: only where the
 * repository's tokenmaxr.json says "background": true. Otherwise the page is plain dark and loads none of it.
 */
const Background = lazy(() => import('./Background'))

/** d0m1.com's token routes on the hash router: #/ is /tokens, #/agents is /tokens/agents. */
interface DashboardProps {
  source: GithubSource
  owner: OwnerStore
  handoff: UnlockHandoff
  view: ViewToggle
  /** tokenmaxr.json's settings (signin.ts parseDashboardConfig). */
  config: Promise<DashboardConfig>
  signIn: DeviceSignIn
  /** The dashboard's repository ("owner/name"), or null on a custom domain that names none. */
  repository: () => Promise<string | null>
}

const Dashboard = ({ source, owner, handoff, view, config, signIn, repository }: DashboardProps) => {
  const [title, setTitle] = useState(() => defaultTitle(window.location.pathname))
  useEffect(() => {
    let live = true
    source.fetchIndex().then(index => {
      const next = typeof index.title === 'string' ? index.title.trim().slice(0, 120) : ''
      if (live && next) setTitle(next)
    }, () => { /* keep the default */ })
    return () => { live = false }
  }, [source])
  useEffect(() => {
    document.title = `${title} · tokenmaxr`
  }, [title])
  const [background, setBackground] = useState(false)
  useEffect(() => {
    let live = true
    void config.then(c => { if (live) setBackground(c.background) })
    return () => { live = false }
  }, [config])
  // One component for the page's life: a new one would remount the panel, which stops a sign-in under way.
  const SignIn = useMemo(() => () => <SignInPanel signIn={signIn} repository={repository} handoff={handoff} />, [signIn, repository, handoff])
  const handoffStatus = useSyncExternalStore(handoff.subscribe, handoff.status)
  // The header starts with the GitHub user, as d0m1.com's starts with "d0m1" (owner direction 2026-10-05:
  // "7-of-9 / tokens"); the title stays in the tab.
  const site = useMemo(() => pagesSite(title, owner, handoffStatus, view, githubUser(window.location.hostname), SignIn), [title, owner, handoffStatus, view, SignIn])

  return (
    <UsageSourceContext.Provider value={source}>
      <TokensSiteContext.Provider value={site}>
        <div className="App">
          {background && <Suspense fallback={null}><Background /></Suspense>}
          {/* d0m1.com's page transitions: #/ and #/agents slide between each other as /tokens and /tokens/agents do. */}
          <RouteStage>
            <Routes>
              <Route path="/" element={<Suspense fallback={null}><AgentsPage /></Suspense>} />
              <Route path="/agents" element={<Suspense fallback={null}><LimitsPage /></Suspense>} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </RouteStage>
        </div>
      </TokensSiteContext.Provider>
    </UsageSourceContext.Provider>
  )
}

export default Dashboard
