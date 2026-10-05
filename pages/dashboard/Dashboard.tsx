import { lazy, Suspense, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import RouteStage from '../../src/components/RouteStage'
import { TokensSiteContext } from '../../src/components/agents/site'
import { UsageSourceContext } from '../../src/components/agents/source'
import type { GithubSource } from './githubSource'
import type { OwnerStore, UnlockHandoff } from './owner'
import { defaultTitle, pagesSite } from './site'

// Split as App.tsx splits them on d0m1.com, so each page's styles arrive as they do there: a page opened directly
// has only its own sheets (AgentsPage.css restyles .agents-gh, which LimitsPage shares, once /tokens has loaded).
const AgentsPage = lazy(() => import('../../src/components/agents/AgentsPage'))
const LimitsPage = lazy(() => import('../../src/components/agents/LimitsPage'))

/** d0m1.com's token routes on the hash router: #/ is /tokens, #/agents is /tokens/agents. */
const Dashboard = ({ source, owner, handoff }: { source: GithubSource; owner: OwnerStore; handoff: UnlockHandoff }) => {
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
  const handoffStatus = useSyncExternalStore(handoff.subscribe, handoff.status)
  const site = useMemo(() => pagesSite(title, owner, handoffStatus), [title, owner, handoffStatus])

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
