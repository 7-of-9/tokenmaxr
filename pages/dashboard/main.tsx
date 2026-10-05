// The tokenmaxr GitHub Pages dashboard: d0m1.com's /tokens and /tokens/agents pages (AgentsPage, LimitsPage, in the
// shared TokensShell) over the files collectors publish to this repository, dressed as a page of GitHub's
// (GithubHeader.tsx, github.css). Built by `npm run build:pages` (vite.pages.config.ts) into pages/site/.
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import Dashboard from './Dashboard'
import { githubSource } from './githubSource'
import { consumeUnlockFragment, createOwnerStore, createUnlockHandoff, indexedDbVault, memoryVault, ownerStorageName } from './owner'
import { createDeviceSignIn, pagesRepository, parseDashboardConfig, type DashboardConfig } from './signin'
import { storedViewToggle } from './site'
import { reloadOnStaleChunks } from '../../src/utils/staleChunks'
// d0m1.com's site-wide styles, which its token pages sit on (the same spacing and resets); github.css then gives
// them GitHub's look, under the pages-github class index.html sets.
import '../../src/index.css'
import '../../src/App.css'
import './github.css'

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
// Before the router reads the address: a copied link's key (#unlock=<key>) leaves the address bar at once, and
// #unlock (tokenmaxr's Settings handing the key over by postMessage, "Open dashboard") asks the window that opened
// this one for it. So does either opened in a tab already showing the dashboard (the same document: no reload, only
// popstate and hashchange), and these listeners come before the router's. Both land on #/agents. (The fragment
// names are the collector's: they stay as they are.)
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

// tokenmaxr.json, served beside the page: on a custom domain, the repository. Missing or unreadable: the default.
const config: Promise<DashboardConfig> = fetch('tokenmaxr.json', { cache: 'no-cache', headers: { Accept: 'application/json' } })
  .then(response => (response.ok ? response.json() : null))
  .catch(() => null)
  .then(parseDashboardConfig)
const repository = () => config.then(c => c.repository ?? pagesRepository(window.location.hostname, window.location.pathname))
// Sign in with GitHub (device flow through the relay): the owner key from the repository's fleet key, checked on
// the published owner files, kept as the handoff keeps it. A signed-out view flag left from before is cleared.
const signIn = createDeviceSignIn({
  relay: __TOKENMAXR_RELAY__,
  repository,
  check: (key, signal) => source.checkOwnerKey(key, signal),
  accept: async bytes => {
    await owner.unlock(bytes)
    view.setSignedOut(false)
  },
})

// The earlier dashboard kept its period in the hash (#7, #30, #90, #365, #0). The router owns the hash now
// (a hash router needs no server fallback on Pages): carry the nearest period over.
if (!window.location.hash.startsWith('#/')) {
  const legacy: Record<string, string> = { '#90': '?period=90d', '#365': '?period=recent', '#0': '?period=recent' }
  window.history.replaceState(null, '', `#/${legacy[window.location.hash] ?? ''}`)
}

// As on d0m1.com (App.tsx): console.log and console.info are off unless ?debug=true, before the hash or in it
// (#/?debug=true).
const debug = [window.location.search, window.location.hash.split('?')[1] ?? ''].some(q => new URLSearchParams(q).get('debug') === 'true')
if (!debug) {
  console.log = () => {}
  console.info = () => {}
}

reloadOnStaleChunks()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <HashRouter>
      <Dashboard source={source} owner={owner} handoff={handoff} view={view} signIn={signIn} repository={repository} />
    </HashRouter>
  </StrictMode>,
)
