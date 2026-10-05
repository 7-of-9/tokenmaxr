import LoadingIndicator from '../LoadingIndicator'
import { clearAuth, refreshAuth, useAuthLinks } from './auth'
import type { Owner } from './site'

/** d0m1.com's header controls (site.ts): GitHub sign-in through Azure Static Web Apps. */
const AuthControls = ({ owner }: { owner: Owner }) => {
  const { signIn, signOut } = useAuthLinks()
  if (owner.status === 'owner') return <a href={signOut} onClick={clearAuth}>Sign out</a>
  if (owner.status === 'checking') return <LoadingIndicator label="Checking sign-in…" inline />
  if (owner.status === 'unavailable') return <><span>Couldn’t check sign-in</span><button type="button" onClick={() => void refreshAuth(true)}>Retry</button></>
  return <a href={signIn}>Sign in with GitHub</a>
}

export default AuthControls
