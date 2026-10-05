// The GitHub Pages dashboard as a TokensSite (src/components/agents/site.ts): the same shell and pages as
// d0m1.com/tokens, with the dashboard's title where d0m1.com says "d0m1", the pages on the hash router (#/ and
// #/agents for /tokens and /tokens/agents), and the owner let in by the key unlocking left in this browser (owner.ts:
// handed over by tokenmaxr's settings page, or a copied link) rather than by a sign-in. The Agents page's choices are
// kept encrypted with that key (the origin is shared with every Pages site of the user). There is no Prompts archive
// here: prompt text is never published.
import { createElement, useSyncExternalStore } from 'react'
import { useLocation } from 'react-router-dom'
import type { Owner, TokensSite } from '../../src/components/agents/site.ts'
import type { HandoffStatus, OwnerStore } from './owner.ts'

const LOCKED: Owner = { status: 'visitor', key: 'locked', controls: false }
const LOADING: Owner = { status: 'checking', key: 'checking', controls: false }
const MOCK: Owner = { status: 'mock', key: 'mock', controls: false }

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

/** The Agents page's gate: how to unlock, or the handoff from tokenmaxr's settings under way or failed (owner.ts). */
function agentsGate(handoff: HandoffStatus) {
  return {
    title: handoff === 'waiting' ? 'Unlocking…' : 'Unlock to see the agents',
    forbiddenTitle: 'This key does not open the agents',
    description: 'Only the owner can read the plan limits: the accounts, organisations and plans behind them are published encrypted.',
    hint: handoff === 'waiting' ? 'Waiting for tokenmaxr’s settings to hand over the key.'
      : handoff === 'failed' ? 'Open this from tokenmaxr’s settings to unlock.'
        : 'Open this dashboard with “Open my dashboard (unlocked)” on your collector’s Settings page. It unlocks this browser; Lock in the header forgets the key.',
  }
}

export function pagesSite(title: string, store: OwnerStore, handoff: HandoffStatus = 'idle'): TokensSite {
  const Controls = () => createElement('button', { type: 'button', onClick: () => store.lock(), title: 'Forget the owner key in this browser' }, 'Lock')
  return {
    root: { label: title, to: '/' },
    home: '/',
    ownerPages: [{ label: 'Agents', to: '/agents' }],
    useOwner() {
      const { search } = useLocation()
      const state = useSyncExternalStore(store.subscribe, store.state)
      if (new URLSearchParams(search).get('mock') === '1') return MOCK
      if (state.status === 'loading') return LOADING
      return state.status === 'unlocked' ? { status: 'owner', key: `unlocked:${state.version}`, controls: true } : LOCKED
    },
    Controls,
    agentsGate: agentsGate(handoff),
    onDenied(status) {
      // Owner files that none of open: the key is not this fleet's (any more). Back to locked, with the gate's advice.
      if (status === 403) store.lock()
    },
    prefs: store.prefs,
  }
}
