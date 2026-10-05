// The Agents page's sign-in on the Pages dashboard (signin.ts): a GitHub device code, shown big, with Copy and Open
// GitHub (which copies it too), then the wait for GitHub, then the owner's view. Shown in the gate's panel when
// this browser holds no key. tokenmaxr's Settings ("Open dashboard") still hands the key over as a second way in.
import { useEffect, useState, useSyncExternalStore } from 'react'
import { useLocation } from 'react-router-dom'
import LoadingIndicator from '../../src/components/LoadingIndicator'
import { copyText } from '../../src/components/agents/clipboard'
import type { HandoffStatus, UnlockHandoff } from './owner'
import { APP_INSTALL_URL, signInMessage, type DeviceSignIn } from './signin'
import './signin.css'

/** Set by the header's Sign in: the panel asks GitHub for a code at once. */
export interface SignInNavigation {
  signIn?: boolean
}

const noHandoff = () => () => {}
const idleHandoff = (): HandoffStatus => 'idle'

function useClock(on: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!on) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [on])
  return now
}

const mmss = (ms: number) => {
  const s = Math.max(0, Math.ceil(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

export default function SignInPanel({ signIn, repository: findRepository, handoff }: {
  signIn: DeviceSignIn
  /** The dashboard's repository ("owner/name"), or null on a custom domain that names none. */
  repository: () => Promise<string | null>
  handoff?: UnlockHandoff
}) {
  const [repository, setRepository] = useState<string | null>(null)
  useEffect(() => {
    let live = true
    void findRepository().then(r => { if (live) setRepository(r) })
    return () => { live = false }
  }, [findRepository])
  const state = useSyncExternalStore(signIn.subscribe, signIn.state)
  const handoffStatus = useSyncExternalStore(handoff?.subscribe ?? noHandoff, handoff?.status ?? idleHandoff)
  const location = useLocation()
  const asked = (location.state as SignInNavigation | null)?.signIn === true
  const [copied, setCopied] = useState(false)
  const now = useClock(state.step === 'code')

  // The header's Sign in (each click a new navigation) starts one, unless one is under way.
  useEffect(() => {
    if (!asked || handoffStatus === 'waiting') return
    const step = signIn.state().step
    if (step === 'idle' || step === 'error') signIn.start()
  }, [asked, location.key, signIn, handoffStatus])
  // Leaving the page stops the polling.
  useEffect(() => () => signIn.cancel(), [signIn])
  useEffect(() => setCopied(false), [state])

  const copy = (code: string) => {
    copyText(code).then(() => setCopied(true), () => { /* the code is on screen */ })
  }
  const who = repository ? <>the GitHub account that owns <strong>{repository}</strong></> : 'the GitHub account that owns this dashboard'

  if (handoffStatus === 'waiting') {
    return <div className="gh-signin"><LoadingIndicator label="Waiting for tokenmaxr to hand over the sign-in…" inline /></div>
  }

  let body
  switch (state.step) {
    case 'idle':
      body = (
        <>
          <p className="agents-muted">Sign in with {who}. GitHub shows a code to confirm, on this device or any other.</p>
          <div className="gh-signin__actions">
            <button type="button" className="agents-btn agents-btn--primary" onClick={() => signIn.start()}>Sign in with GitHub</button>
          </div>
        </>
      )
      break
    case 'requesting':
      body = <LoadingIndicator label="Asking GitHub for a code…" inline />
      break
    case 'code': {
      const { code, opened } = state
      body = (
        <>
          <p className="gh-signin__step">Enter this code on GitHub, signed in as {who}:</p>
          <div className="gh-signin__code" aria-label={`Code ${code.userCode.split('').join(' ')}`}>{code.userCode}</div>
          <div className="gh-signin__actions">
            <a className="agents-btn agents-btn--primary" href={code.verificationUri} target="_blank" rel="noopener noreferrer"
              onClick={() => { copy(code.userCode); signIn.opened() }}>
              Open GitHub ↗
            </a>
            <button type="button" className="agents-btn" onClick={() => copy(code.userCode)}>{copied ? 'Copied' : 'Copy code'}</button>
          </div>
          <div className="gh-signin__status" aria-live="polite">
            {opened
              ? <LoadingIndicator label="Waiting for GitHub…" detail="Approve tokenmaxor there; this page carries on by itself." inline />
              : <p className="agents-muted">Open GitHub copies the code too: paste it there, then approve tokenmaxor.</p>}
          </div>
          <p className="agents-muted gh-signin__meta">
            Code expires in {mmss(code.expiresAt - now)} ·{' '}
            <button type="button" className="gh-signin__link" onClick={() => signIn.cancel()}>Cancel</button>
          </p>
        </>
      )
      break
    }
    case 'checking':
      body = <LoadingIndicator label="Signed in on GitHub" detail={`Reading the key from ${repository ?? 'your repository'}…`} inline />
      break
    case 'done':
      body = <p className="gh-signin__done" role="status">Signed in.</p>
      break
    case 'error': {
      const { error } = state
      body = (
        <>
          <p className="gh-signin__error" role="alert">{signInMessage(error)}</p>
          {error.kind === 'not-installed' && (
            <p className="agents-muted">Add the repository to tokenmaxor on GitHub (<a href={APP_INSTALL_URL} target="_blank" rel="noopener noreferrer">install or configure tokenmaxor ↗</a>), then sign in again.</p>
          )}
          {error.kind === 'cant-read' && <p className="agents-muted">Sign in with the account that owns it, or a collaborator.</p>}
          <div className="gh-signin__actions">
            <button type="button" className="agents-btn agents-btn--primary" onClick={() => signIn.start()}>
              {error.kind === 'expired' ? 'Get a new code' : 'Try again'}
            </button>
          </div>
        </>
      )
      break
    }
  }

  return (
    <div className="gh-signin">
      {body}
      {state.step !== 'done' && (
        <p className="agents-muted gh-signin__alt">
          On a computer running tokenmaxr, <em>Open dashboard</em> in its Settings signs this browser in too.
        </p>
      )}
    </div>
  )
}
