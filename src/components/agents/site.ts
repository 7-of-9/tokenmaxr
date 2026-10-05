// Which site the token pages are on: what the shell's breadcrumb starts with, where the pages live, and how the
// owner gets in. d0m1.com (the default: nothing provides one) signs its owner in with GitHub and offers Prompts
// and Agents; the tokenmaxr GitHub Pages dashboard (pages/dashboard/site.ts) unlocks its owner pages with a key
// kept in the browser and offers Agents. TokensShell, AgentsPage and LimitsPage read it, so both sites render the
// same pages from the same code.
import { createContext, useContext, type ComponentType } from 'react'
import { useLocation } from 'react-router-dom'
import AuthControls from './AuthControls'
import { clearAuth, refreshAuth, useAuth } from './auth'

/**
 * checking: not known yet; visitor: no owner pages (signed out, or locked); owner: the owner pages are open;
 * unavailable: sign-in could not be checked; mock: test data (?mock=1), no sign-in at all.
 */
export type OwnerStatus = 'checking' | 'visitor' | 'owner' | 'unavailable' | 'mock'

export interface Owner {
  status: OwnerStatus
  /** Changes whenever the owner's identity does, so owner data is read again. */
  key: string
  /** The header shows the site's controls (sign-in, lock), on a row of their own on a phone. */
  controls: boolean
}

/** What the Agents page says to someone who cannot open it. */
export interface OwnerGate {
  title: string
  /** Signed in (or unlocked), but not as the owner. */
  forbiddenTitle: string
  description: string
  /** How to get in (default: AuthGate's sign-in advice). */
  hint?: string
}

/**
 * Where the Agents page keeps this browser's choices (which accounts are tracked, plan labels). They name the
 * owner's accounts by email, so a site whose origin is shared (GitHub Pages) keeps them encrypted instead.
 */
export interface SitePrefs {
  /** The saved value, or null; may throw when the browser refuses storage. */
  get(name: string): string | null
  /** May throw when it cannot be saved. */
  set(name: string, value: string): void
  /** Changes made elsewhere (another tab, or the site's own lock), never this page's own set. */
  subscribe(name: string, listener: (value: string | null) => void): () => void
}

/** d0m1.com: plain local storage, with other tabs' changes. */
export const LOCAL_PREFS: SitePrefs = {
  get: name => window.localStorage.getItem(name),
  set: (name, value) => window.localStorage.setItem(name, value),
  subscribe(name, listener) {
    const sync = (event: StorageEvent) => {
      if (event.storageArea === window.localStorage && (event.key === name || event.key === null)) listener(event.newValue)
    }
    window.addEventListener('storage', sync)
    return () => window.removeEventListener('storage', sync)
  },
}

export interface TokensSite {
  /** The breadcrumb's first link: "d0m1" to the home page on d0m1.com. */
  root: { label: string; to: string }
  /** The /tokens page's route: the token pages are this route and the routes below it. */
  home: string
  /** The owner's pages, offered in the header once the owner is in. */
  ownerPages: Array<{ label: string; to: string }>
  /** The viewer's access to the owner's pages (a hook: call it on every render). */
  useOwner(): Owner
  /** The header's controls, shown when the owner state asks for them. */
  Controls: ComponentType<{ owner: Owner }>
  agentsGate: OwnerGate
  /** The owner's data was refused: 401 signed out (locked), 403 not the owner (the wrong key). */
  onDenied?(status: 401 | 403): void
  /** Where the Agents page keeps its choices (default: LOCAL_PREFS). */
  prefs?: SitePrefs
}

const OWNER_STATUS = { checking: 'checking', anon: 'visitor', 'no-auth': 'unavailable', mock: 'mock', 'signed-in': 'owner' } as const

/** d0m1.com: Azure Static Web Apps' GitHub sign-in, off for ?mock=1. */
function useD0m1Owner(): Owner {
  const { search } = useLocation()
  const auth = useAuth({ enabled: new URLSearchParams(search).get('mock') !== '1' })
  return {
    status: OWNER_STATUS[auth.status],
    key: auth.status === 'signed-in' ? `${auth.principal.identityProvider}:${auth.principal.userId}` : auth.status,
    controls: auth.status !== 'mock',
  }
}

export const D0M1_SITE: TokensSite = {
  root: { label: 'd0m1', to: '/' },
  home: '/tokens',
  ownerPages: [{ label: 'Prompts', to: '/tokens/prompts' }, { label: 'Agents', to: '/tokens/agents' }],
  useOwner: useD0m1Owner,
  Controls: AuthControls,
  agentsGate: {
    title: 'Sign in to see the agents',
    forbiddenTitle: 'This account cannot see the agents',
    description: 'Only the owner account can read the plan limits.',
  },
  onDenied(status) {
    if (status === 401) clearAuth()
    else void refreshAuth(true)
  },
}

/** Null means d0m1.com: it never provides one, so its pages behave exactly as before the seam. */
export const TokensSiteContext = createContext<TokensSite | null>(null)

export function useTokensSite(): TokensSite {
  return useContext(TokensSiteContext) ?? D0M1_SITE
}
