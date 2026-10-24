import LoadingIndicator from '../LoadingIndicator'
import { clearAuth, refreshAuth, useAuth, useAuthLinks } from './auth'
import type { Owner } from './site'
import './AuthControls.css'

/** d0m1.com's header controls (site.ts): GitHub sign-in through Azure Static Web Apps. */
const AuthControls = ({ owner }: { owner: Owner }) => {
  const { signIn, signOut } = useAuthLinks()
  const control = owner.status === 'owner' ? <a href={signOut} onClick={event => {
    // Clearing the shared store rerenders this same anchor as "Sign in".
    // Capture navigation ourselves before its href can change under the click.
    event.preventDefault()
    clearAuth()
    window.location.assign(signOut)
  }}>Sign out</a>
    : owner.status === 'checking' ? <LoadingIndicator label="Checking sign-in…" inline />
    : owner.status === 'unavailable' ? <><span>Couldn’t check sign-in</span><button type="button" onClick={() => void refreshAuth(true)}>Retry</button></>
    : <a href={signIn}>Sign in with GitHub</a>
  return <span className="github-auth-controls">{control}</span>
}

export default AuthControls

/** The same controls on galleries, sharing the Tokens session and return-to URL. */
export function GalleryAuthControls() {
  const auth = useAuth()
  const status = { checking: 'checking', anon: 'visitor', 'no-auth': 'unavailable', mock: 'visitor', 'signed-in': 'owner' } as const
  return <AuthControls owner={{ status: status[auth.status], key: auth.status, controls: true }} />
}
