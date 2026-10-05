// The tokenmaxr GitHub Pages dashboard: d0m1.com's /tokens and /tokens/agents pages (AgentsPage, LimitsPage, in the
// shared TokensShell) over the files collectors publish to this repository. Built by `npm run build:pages`
// (vite.pages.config.ts) into pages/site/.
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import Dashboard from './Dashboard'
import { githubSource } from './githubSource'
import { consumeUnlockFragment, createOwnerStore, createUnlockHandoff, indexedDbVault, memoryVault, ownerStorageName } from './owner'
import { storedViewToggle } from './site'
// d0m1.com's site-wide styles, which its token pages sit on; pages.css adds the two faces they use.
import '../../src/index.css'
import '../../src/App.css'
import './pages.css'

let storage: Storage | null = null
try {
  storage = window.localStorage
} catch {
  // Storage blocked: the Agents page's choices last for this tab.
}
const name = ownerStorageName(window.location.pathname)
let channel: BroadcastChannel | null = null
try {
  channel = new BroadcastChannel(name)
} catch {
  // No other tabs to tell.
}
const owner = createOwnerStore({
  name,
  // The key as a non-extractable CryptoKey; without IndexedDB, for this tab only.
  vault: typeof indexedDB === 'undefined' ? memoryVault() : indexedDbVault(indexedDB),
  storage,
  channel,
  events: window,
})
// Before the router reads the address: an unlock link's key leaves the address bar at once, and #unlock (tokenmaxr's
// settings page handing the key over by postMessage) asks the window that opened this one for it. So does either
// opened in a tab already showing the dashboard (the same document: no reload, only popstate and hashchange), and
// these listeners come before the router's. Both land on #/agents.
const handoff = createUnlockHandoff({ window, store: owner })
const takeUnlock = () => {
  if (!handoff.consume(window.location, window.history)) consumeUnlockFragment(window.location, window.history, owner)
}
takeUnlock()
window.addEventListener('popstate', takeUnlock)
window.addEventListener('hashchange', takeUnlock)

const source = githubSource({ ownerKey: owner.ready })
// Sign out / Sign in: the public view or the owner's, per dashboard in this browser (site.ts).
const view = storedViewToggle(storage, `${name}:signed-out`, window)

// The earlier dashboard kept its period in the hash (#7, #30, #90, #365, #0). The router owns the hash now
// (a hash router needs no server fallback on Pages): carry the nearest period over.
if (!window.location.hash.startsWith('#/')) {
  const legacy: Record<string, string> = { '#90': '?period=90d', '#365': '?period=recent', '#0': '?period=recent' }
  window.history.replaceState(null, '', `#/${legacy[window.location.hash] ?? ''}`)
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <HashRouter>
      <Dashboard source={source} owner={owner} handoff={handoff} view={view} />
    </HashRouter>
  </StrictMode>,
)
