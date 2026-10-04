// The tokenmaxr GitHub Pages dashboard: d0m1.com's /tokens page (TokensView) over the files collectors publish
// to this repository. Built by `npm run build:pages` (vite.pages.config.ts) into pages/site/.
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import { UsageSourceContext } from '../../src/components/agents/source'
import TokensView from '../../src/components/agents/TokensView'
import { githubSource } from './githubSource'
import PagesShell from './PagesShell'
import QuotaCard from './QuotaCard'
import './pages.css'

const source = githubSource()
const loadTitle = () => source.fetchIndex().then(index => index.title)
const loadMeters = (signal: AbortSignal) => source.fetchMeters(signal)

// The earlier dashboard kept its period in the hash (#7, #30, #90, #365, #0). The router owns the hash now
// (a hash router needs no server fallback on Pages): carry the nearest period over.
if (!window.location.hash.startsWith('#/')) {
  const legacy: Record<string, string> = { '#90': '?period=90d', '#365': '?period=recent', '#0': '?period=recent' }
  window.history.replaceState(null, '', `#/${legacy[window.location.hash] ?? ''}`)
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <HashRouter>
      <UsageSourceContext.Provider value={source}>
        <PagesShell loadTitle={loadTitle}>
          <TokensView aside={machine => <QuotaCard load={loadMeters} machine={machine} pollMs={source.pollMs} />} />
        </PagesShell>
      </UsageSourceContext.Provider>
    </HashRouter>
  </StrictMode>,
)
