import { lazy, Suspense, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import RouteStage from '../../src/components/RouteStage'
import { TokensSiteContext, type Owner } from '../../src/components/agents/site'
import { UsageSourceContext } from '../../src/components/agents/source'
import GithubHeader from './GithubHeader'
import type { GithubSource } from './githubSource'
import type { OwnerStore, UnlockHandoff } from './owner'
import SignInPanel from './SignInPanel'
import { pagesRepository, type DeviceSignIn } from './signin'
import { defaultTitle, githubUser, headerCrumbs, pagesSite, type ViewToggle } from './site'

// Split as App.tsx splits them on d0m1.com, so each page's styles arrive as they do there: a page opened directly
// has only its own sheets (AgentsPage.css restyles .agents-gh, which LimitsPage shares, once /tokens has loaded).
const AgentsPage = lazy(() => import('../../src/components/agents/AgentsPage'))
const LimitsPage = lazy(() => import('../../src/components/agents/LimitsPage'))

/** d0m1.com's token routes on the hash router: #/ is /tokens, #/agents is /tokens/agents. */
interface DashboardProps {
  source: GithubSource
  owner: OwnerStore
  handoff: UnlockHandoff
  view: ViewToggle
  signIn: DeviceSignIn
  /** The dashboard's repository ("owner/name"), or null on a custom domain that names none. */
  repository: () => Promise<string | null>
}

const Dashboard = ({ source, owner, handoff, view, signIn, repository }: DashboardProps) => {
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
  // The header's "<owner> / <repository>": the address says it on <user>.github.io; tokenmaxr.json on a custom domain.
  const [repo, setRepo] = useState(() => pagesRepository(window.location.hostname, window.location.pathname))
  useEffect(() => {
    let live = true
    void repository().then(r => { if (live) setRepo(r) })
    return () => { live = false }
  }, [repository])
  const crumbs = useMemo(() => headerCrumbs(repo, title), [repo, title])
  const Header = useMemo(() => ({ owner: viewer }: { owner: Owner }) => <GithubHeader owner={viewer} crumbs={crumbs} />, [crumbs])
  // One component for the page's life: a new one would remount the panel, which stops a sign-in under way.
  const SignIn = useMemo(() => () => <SignInPanel signIn={signIn} repository={repository} handoff={handoff} />, [signIn, repository, handoff])
  const handoffStatus = useSyncExternalStore(handoff.subscribe, handoff.status)
  // GitHub's header in place of d0m1.com's "< d0m1 / tokens" (owner direction 2026-10-05: "a github-like top
  // banner"); the title stays in the tab.
  const site = useMemo(() => pagesSite(title, owner, handoffStatus, view, githubUser(window.location.hostname), SignIn, Header),
    [title, owner, handoffStatus, view, SignIn, Header])

  return (
    <UsageSourceContext.Provider value={source}>
      <TokensSiteContext.Provider value={site}>
        <div className="App">
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
