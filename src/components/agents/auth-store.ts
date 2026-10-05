import type { ClientPrincipal } from './types'

export type AuthState =
  | { status: 'checking' | 'anon' | 'no-auth' | 'mock' }
  | { status: 'signed-in'; principal: ClientPrincipal }

export const AUTH_CHECKING: AuthState = { status: 'checking' }
export const AUTH_MOCK: AuthState = { status: 'mock' }
const REFRESH_MS = 60_000

/** One principal cache for all token pages. A protected API's 401 outranks an older auth request. */
export function createAuthStore(read: () => Promise<Response>, now = Date.now) {
  let state = AUTH_CHECKING
  let checkedAt = -Infinity
  let revision = 0
  let pending: Promise<void> | null = null
  const listeners = new Set<() => void>()
  const publish = (next: AuthState) => { state = next; listeners.forEach(listener => listener()) }
  return {
    getSnapshot: () => state,
    subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } },
    clear: () => { revision++; checkedAt = now(); publish({ status: 'anon' }) },
    refresh: (force = false): Promise<void> => {
      if (pending) return pending
      if (!force && now() - checkedAt < REFRESH_MS) return Promise.resolve()
      const started = revision
      pending = (async () => {
        try {
          const response = await Promise.resolve().then(read)
          if (started !== revision) return
          if (response.status === 401) { publish({ status: 'anon' }); return }
          if (!response.ok) throw new Error('Authentication unavailable')
          const body = await response.json() as { clientPrincipal?: ClientPrincipal | null }
          if (started !== revision) return
          const p = body?.clientPrincipal
          if (p && typeof p.userId === 'string' && p.userId && typeof p.identityProvider === 'string' &&
            Array.isArray(p.userRoles) && p.userRoles.includes('authenticated')) {
            publish({ status: 'signed-in', principal: {
              userId: p.userId, identityProvider: p.identityProvider,
              userDetails: typeof p.userDetails === 'string' ? p.userDetails : '',
              userRoles: p.userRoles.filter(role => typeof role === 'string'),
            } })
          } else publish({ status: 'anon' })
        } catch {
          if (started === revision && state.status !== 'signed-in' && state.status !== 'anon') publish({ status: 'no-auth' })
        } finally { checkedAt = now(); pending = null }
      })()
      return pending
    },
  }
}

/** Both endpoints stay on the current origin, including when Vite bridges production authentication. */
export function authUrls(location: { pathname: string; search: string; hash: string }) {
  const current = location.pathname + location.search + location.hash
  const returnTo = current.startsWith('/') && !current.startsWith('//') && !current.includes('\\') ? current : '/tokens'
  return {
    signIn: `/.auth/login/github?post_login_redirect_uri=${encodeURIComponent(returnTo)}`,
    signOut: `/.auth/logout?post_logout_redirect_uri=${encodeURIComponent(returnTo)}`,
  }
}
