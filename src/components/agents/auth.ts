import { useEffect, useSyncExternalStore } from 'react'
import { useLocation } from 'react-router-dom'
import { AUTH_CHECKING, AUTH_MOCK, authUrls, createAuthStore } from './auth-store'

const store = createAuthStore(() => fetch('/.auth/me', {
  credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' }, signal: AbortSignal.timeout(25_000),
}))
let consumers = 0
let cleanup: (() => void) | undefined
const noSubscription = () => () => {}

export const clearAuth = store.clear
export const refreshAuth = store.refresh

export function useAuth({ enabled = true } = {}) {
  const auth = useSyncExternalStore(enabled ? store.subscribe : noSubscription,
    enabled ? store.getSnapshot : () => AUTH_MOCK, () => enabled ? AUTH_CHECKING : AUTH_MOCK)
  useEffect(() => {
    if (!enabled) return
    if (++consumers === 1) {
      const refresh = () => { if (document.visibilityState !== 'hidden') void store.refresh() }
      const timer = window.setInterval(refresh, 60_000)
      window.addEventListener('focus', refresh)
      document.addEventListener('visibilitychange', refresh)
      cleanup = () => {
        window.clearInterval(timer)
        window.removeEventListener('focus', refresh)
        document.removeEventListener('visibilitychange', refresh)
      }
    }
    void store.refresh()
    return () => { if (--consumers === 0) { cleanup?.(); cleanup = undefined } }
  }, [enabled])
  return auth
}

export function useAuthLinks() { return authUrls(useLocation()) }
