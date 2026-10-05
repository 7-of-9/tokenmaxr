// The GitHub Pages dashboard as a TokensSite (src/components/agents/site.ts): the same shell and pages as
// d0m1.com/tokens, under a GitHub-style header (GithubHeader.tsx, "<owner> / <repository>" where d0m1.com says
// "< d0m1 / tokens") in GitHub's colours (github.css), the pages on the hash
// router (#/ and #/agents for /tokens and /tokens/agents), and the owner signed in by the owner key this browser
// keeps (owner.ts): from GitHub (signin.ts, the device flow on any device) or handed over by tokenmaxr's Settings.
// Sign out and Sign in toggle between the public view and the owner's, keeping the key. The Agents page's choices
// are kept encrypted with that key (the origin is shared with every Pages site of the user). There is no Prompts
// archive here: prompt text is never published.
import { createElement, useSyncExternalStore, type ComponentType } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import type { Owner, TokensSite } from '../../src/components/agents/site.ts'
import type { HandoffStatus, OwnerStore } from './owner.ts'

// A visitor (or the owner signed out) sees the public pages, and Sign in in the header.
const SIGNED_OUT: Owner = { status: 'visitor', key: 'locked', controls: true }
const LOADING: Owner = { status: 'checking', key: 'checking', controls: false }
const MOCK: Owner = { status: 'mock', key: 'mock', controls: false }

/** The GitHub user a Pages site belongs to (https://<user>.github.io/…), or null on another host. */
export function githubUser(hostname: string): string | null {
  const host = hostname.toLowerCase()
  if (!host.endsWith('.github.io')) return null
  const user = host.slice(0, -'.github.io'.length)
  return user && !user.includes('.') ? user : null
}

/**
 * Sign out shows the public view and keeps the owner key, so Sign in is one click back (owner direction 2026-10-05:
 * "toggle the GH page from true public/anon version to my personal logged-in version"). Kept per dashboard in this
 * browser; other tabs follow.
 */
export interface ViewToggle {
  signedOut(): boolean
  setSignedOut(signedOut: boolean): void
  subscribe(listener: () => void): () => void
}

export function storedViewToggle(storage: Storage | null, key: string, events: Pick<Window, 'addEventListener' | 'removeEventListener'> | null): ViewToggle {
  const listeners = new Set<() => void>()
  let memory = false
  const read = () => {
    try {
      return storage ? storage.getItem(key) === '1' : memory
    } catch {
      return memory
    }
  }
  let current = read()
  const notify = () => { for (const listener of [...listeners]) listener() }
  const onStorage = (event: StorageEvent) => {
    if (event.key !== key && event.key !== null) return
    const next = read()
    if (next !== current) {
      current = next
      notify()
    }
  }
  return {
    signedOut: () => current,
    setSignedOut(signedOut) {
      memory = signedOut
      try {
        if (signedOut) storage?.setItem(key, '1')
        else storage?.removeItem(key)
      } catch {
        // Storage refused: this tab only.
      }
      if (signedOut !== current) {
        current = signedOut
        notify()
      }
    },
    subscribe(listener) {
      if (listeners.size === 0) events?.addEventListener('storage', onStorage as EventListener)
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
        if (listeners.size === 0) events?.removeEventListener('storage', onStorage as EventListener)
      }
    },
  }
}

/** The header's breadcrumb: "<owner> / <repository>", each linking to it on GitHub, as GitHub writes it. */
export interface HeaderCrumbs {
  /** The repository's owner (their GitHub profile), or null where the repository is not known. */
  owner: { label: string; href: string } | null
  /** The repository, or else the dashboard's title (linking to the dashboard). */
  repository: { label: string; href?: string }
}

/** From the repository ("owner/name", signin.ts pagesRepository or tokenmaxr.json), else the dashboard's title. */
export function headerCrumbs(repository: string | null, title: string): HeaderCrumbs {
  const slash = repository ? repository.indexOf('/') : -1
  if (!repository || slash <= 0 || slash === repository.length - 1) return { owner: null, repository: { label: title } }
  const owner = repository.slice(0, slash)
  const name = repository.slice(slash + 1)
  const profile = `https://github.com/${encodeURIComponent(owner)}`
  return { owner: { label: owner, href: profile }, repository: { label: name, href: `${profile}/${encodeURIComponent(name)}` } }
}

/**
 * The Detail calendar's heat colours: GitHub's contribution-graph greens for the scheme the page follows
 * (github.css defines them, light and dark).
 */
export const PAGES_HEAT_LEVELS = [0, 1, 2, 3, 4].map(level => `var(--pages-heat-${level})`)

/** Until data/index.json names it: the repository (https://<user>.github.io/<repository>/), else "tokens". */
export function defaultTitle(pathname: string): string {
  const repository = pathname.split('/').find(Boolean)
  if (!repository || repository.includes('.')) return 'tokens'
  try {
    return decodeURIComponent(repository)
  } catch {
    return repository
  }
}

/** The Agents page's gate: the GitHub sign-in (SignIn), or tokenmaxr's Settings handing the sign-in over (owner.ts). */
function agentsGate(handoff: HandoffStatus, SignIn?: ComponentType) {
  return {
    title: handoff === 'waiting' ? 'Signing in…' : 'Sign in to see the agents',
    forbiddenTitle: 'This sign-in does not open the agents',
    description: 'Only the owner can read the plan limits: the accounts, organisations and plans behind them are published encrypted.',
    hint: handoff === 'waiting' ? 'Waiting for tokenmaxr’s Settings to hand over the sign-in.'
      : 'Use Sign in in the header, or Open dashboard in tokenmaxr’s Settings.',
    SignIn,
  }
}

const noSubscribe = () => () => {}
const notSignedOut = () => false

export function pagesSite(title: string, store: OwnerStore, handoff: HandoffStatus = 'idle', view: ViewToggle | null = null, user: string | null = null,
  SignIn?: ComponentType, Header?: ComponentType<{ owner: Owner }>): TokensSite {
  // Sign out keeps the key (view.setSignedOut); Sign in brings the owner's view back, or, in a browser that holds
  // no key, opens the Agents page and starts the GitHub sign-in there (SignInPanel).
  const Controls = () => {
    const state = useSyncExternalStore(store.subscribe, store.state)
    const signedOut = useSyncExternalStore(view?.subscribe ?? noSubscribe, view?.signedOut ?? notSignedOut)
    const navigate = useNavigate()
    if (state.status === 'unlocked' && !signedOut) {
      return createElement('button', { type: 'button', onClick: () => view?.setSignedOut(true), title: 'Show the public view (this browser keeps the key: Sign in brings yours back)' }, 'Sign out')
    }
    const signIn = () => {
      if (state.status === 'unlocked') view?.setSignedOut(false)
      else navigate('/agents', { state: { signIn: true } })
    }
    return createElement('button', { type: 'button', onClick: signIn, title: state.status === 'unlocked' ? 'Back to your view' : 'Sign in with GitHub' }, 'Sign in')
  }
  return {
    // Above the dashboard is the user's GitHub profile, as d0m1.com's home is above /tokens. The Header (GithubHeader)
    // draws the breadcrumb; the shell's Esc goes back a page and never leaves the dashboard.
    root: { label: user ?? title, to: '/', href: user ? `https://github.com/${user}` : undefined },
    home: '/',
    ownerPages: [{ label: 'Agents', to: '/agents' }],
    useOwner() {
      const { search } = useLocation()
      const state = useSyncExternalStore(store.subscribe, store.state)
      const signedOut = useSyncExternalStore(view?.subscribe ?? noSubscribe, view?.signedOut ?? notSignedOut)
      if (new URLSearchParams(search).get('mock') === '1') return MOCK
      if (state.status === 'loading') return LOADING
      return state.status === 'unlocked' && !signedOut ? { status: 'owner', key: `unlocked:${state.version}`, controls: true } : SIGNED_OUT
    },
    Controls,
    agentsGate: agentsGate(handoff, SignIn),
    onDenied(status) {
      // Owner files none of which open: the key is not this fleet's (any more). Forget it: the gate offers the sign-in.
      if (status === 403) store.lock()
    },
    prefs: store.prefs,
    Header,
    heatLevels: PAGES_HEAT_LEVELS,
  }
}
