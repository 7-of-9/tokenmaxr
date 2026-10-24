import { useEffect, useSyncExternalStore } from 'react'
import { useLocation } from 'react-router-dom'
import { AUTH_CHECKING, AUTH_MOCK, authUrls, createAuthStore } from './auth-store'

const store = createAuthStore(() => fetch(import.meta.env.DEV ? '/.auth/me' : '/api/session', {
  method: import.meta.env.DEV ? 'GET' : 'POST',
  credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' }, signal: AbortSignal.timeout(25_000),
}))
let consumers = 0
let cleanup: (() => void) | undefined
const noSubscription = () => () => {}

const SIGN_OUT_EVENT = 'd0m1-sign-out'
export const clearAuth = () => {
  store.clear()
  // No credentials or private data enter storage; this only invalidates other tabs.
  try { window.localStorage.setItem(SIGN_OUT_EVENT, `${Date.now()}:${Math.random()}`) } catch { /* Storage may be disabled. */ }
}
export const refreshAuth = store.refresh

export function useAuth({ enabled = true } = {}) {
  const auth = useSyncExternalStore(enabled ? store.subscribe : noSubscription,
    enabled ? store.getSnapshot : () => AUTH_MOCK, () => enabled ? AUTH_CHECKING : AUTH_MOCK)
  useEffect(() => {
    if (!enabled) return
    if (++consumers === 1) {
      const refresh = () => { if (document.visibilityState !== 'hidden') void store.refresh() }
      const signedOutElsewhere = (event: StorageEvent) => { if (event.key === SIGN_OUT_EVENT) store.clear() }
      const restored = (event: PageTransitionEvent) => { if (event.persisted) void store.refresh(true) }
      const timer = window.setInterval(refresh, 60_000)
      window.addEventListener('focus', refresh)
      document.addEventListener('visibilitychange', refresh)
      window.addEventListener('storage', signedOutElsewhere)
      window.addEventListener('pageshow', restored)
      cleanup = () => {
        window.clearInterval(timer)
        window.removeEventListener('focus', refresh)
        document.removeEventListener('visibilitychange', refresh)
        window.removeEventListener('storage', signedOutElsewhere)
        window.removeEventListener('pageshow', restored)
      }
    }
    void store.refresh()
    return () => { if (--consumers === 0) { cleanup?.(); cleanup = undefined } }
  }, [enabled])
  return auth
}

export function useAuthLinks() { return authUrls(useLocation(), !import.meta.env.DEV) }
