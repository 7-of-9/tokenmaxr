import type { NavigationType } from 'react-router-dom'

export type EnterDir = 'forward' | 'back' | 'none' | 'zoom-in' | 'zoom-out'

export type CardOrigin = {
  x: number
  y: number
  width: number
  height: number
  borderRadius: string
}

const MENU_PATHS = new Set([
  '/',
  '/projects',
  '/cv',
  '/cvfull',
  '/cvsg',
  '/cvuk',
  '/cto',
  '/full',
  '/sg',
  '/uk',
  '/btc',
  '/tokens',
  '/agents',
  '/tokens/agents',
  '/tokens/prompts',
  '/collector',
  '/collector/link',
  '/work',
])

// Gallery collection routes slide like menu pages. Matched by FNV-1a hash so the
// unlisted routes never appear as text in the client bundle.
const COLLECTION_PATH_HASHES = new Set([14843383, 1508697868, 335610735])

const pathHash = (path: string) => {
  let hash = 0x811c9dc5
  for (let i = 0; i < path.length; i++) {
    hash ^= path.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash
}

const isMenuPath = (path: string) => MENU_PATHS.has(path) || COLLECTION_PATH_HASHES.has(pathHash(path.toLowerCase()))

export const ZOOM_ORIGIN_KEY = 'd0m1-zoom-origin'

export const prefersReducedMotion = () => {
  if (typeof window === 'undefined') return false
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

const PHONE_SHORT_SIDE_MAX = 520
const TABLET_SHORT_SIDE_MAX = 1024
const TABLET_LONG_SIDE_MAX = 1366

export type CardOrientation = 'portrait' | 'landscape'
export type CardRotateAction = 'ignore' | 'stay' | 'close'

export const readCardOrientation = (width: number, height: number): CardOrientation =>
  height >= width ? 'portrait' : 'landscape'

/**
 * Phone or tablet card viewport from CSS pixels only (no DOM).
 * Chrome device-emulator portrait sizes count; wide desktop does not.
 */
export const isPhoneCardViewport = (width?: number, height?: number): boolean => {
  if (width == null || height == null) {
    if (typeof window === 'undefined') return false
    width = window.innerWidth
    height = window.innerHeight
  }
  const shortSide = Math.min(width, height)
  const longSide = Math.max(width, height)
  if (shortSide <= PHONE_SHORT_SIDE_MAX) return true
  if (shortSide <= TABLET_SHORT_SIDE_MAX && longSide <= TABLET_LONG_SIDE_MAX) {
    const landscape = width > height
    if (landscape && width >= 1200 && height <= 900) return false
    return true
  }
  return false
}

/**
 * Portrait card open: first swap to landscape stays fullscreen;
 * swap back to portrait closes. Driven by width/height, not orientationchange.
 */
export const resolveCardRotateAction = (
  prev: CardOrientation | null,
  next: CardOrientation,
  seenOpposite: boolean,
  isCardViewport: boolean,
): CardRotateAction => {
  if (!isCardViewport) return 'ignore'
  if (prev == null || prev === next) return 'ignore'
  if (prev === 'portrait' && next === 'landscape') return 'stay'
  if (prev === 'landscape' && next === 'portrait' && seenOpposite) return 'close'
  return 'ignore'
}

export const isCardPath = (pathname: string) => pathname.split('?')[0] === '/card'

export const routeDepth = (pathname: string) => {
  const path = pathname.split('?')[0]
  if (path === '/') return 0
  return path.split('/').filter(Boolean).length
}

export const sceneKey = (pathname: string, search = '') => {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  const folder = params.get('path')
  return folder ? `${pathname}?path=${folder}` : pathname
}

export const isDrillScene = (scene: string) => {
  if (scene.includes('?path=')) return true
  const path = scene.split('?')[0]
  if (isMenuPath(path) || isCardPath(path)) return false
  return path.split('/').filter(Boolean).length >= 1
}

export const resolveEnterDir = (
  from: string,
  to: string,
  navType: NavigationType,
  reducedMotion = false,
): EnterDir => {
  if (reducedMotion) return 'none'
  const fromPath = from.split('?')[0]
  const toPath = to.split('?')[0]
  if (isCardPath(fromPath) || isCardPath(toPath)) return 'none'
  if (from === to) return 'none'

  const fromDrill = isDrillScene(from)
  const toDrill = isDrillScene(to)
  if (!fromDrill && toDrill) return 'zoom-in'
  if (fromDrill && !toDrill) return 'zoom-out'
  if (fromDrill && toDrill) {
    const fromFolder = from.includes('?path=') ? from.length : 0
    const toFolder = to.includes('?path=') ? to.length : 0
    if (toFolder > fromFolder) return 'zoom-in'
    if (toFolder < fromFolder) return 'zoom-out'
    return 'zoom-in'
  }

  if (navType === 'POP') return 'back'
  if (routeDepth(toPath) < routeDepth(fromPath)) return 'back'
  return 'forward'
}

export const rememberZoomOrigin = (el: Element) => {
  if (typeof window === 'undefined') return
  const rect = el.getBoundingClientRect()
  const ox = ((rect.left + rect.width / 2) / window.innerWidth) * 100
  const oy = ((rect.top + rect.height / 2) / window.innerHeight) * 100
  sessionStorage.setItem(ZOOM_ORIGIN_KEY, `${ox}% ${oy}%`)
}

export const readZoomOrigin = () => {
  if (typeof window === 'undefined') return '50% 50%'
  return sessionStorage.getItem(ZOOM_ORIGIN_KEY) || '50% 50%'
}

export const readCardOrigin = (el: Element): CardOrigin => {
  const rect = el.getBoundingClientRect()
  return {
    x: rect.left,
    y: rect.top,
    width: rect.width,
    height: rect.height,
    borderRadius: getComputedStyle(el).borderRadius || '14px',
  }
}
