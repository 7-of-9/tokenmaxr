// After a deploy, a page loaded from the previous build asks for lazy chunks (the Detail view, the Agents page) that
// no longer exist, and the view stays blank. Vite reports that as 'vite:preloadError'; the page then reloads once
// to pick up the new build. A second failure within a minute is left alone (shown as the error it is), so a chunk
// that is really missing never loops.

const KEY = 'stale-chunk-reload-at'
const WINDOW_MS = 60_000

interface StaleChunkWindow {
  addEventListener(type: 'vite:preloadError', listener: (event: Event) => void): void
  sessionStorage?: Pick<Storage, 'getItem' | 'setItem'>
  location: Pick<Location, 'reload'>
}

export function reloadOnStaleChunks(win: StaleChunkWindow = window as unknown as StaleChunkWindow, now: () => number = Date.now): void {
  win.addEventListener('vite:preloadError', (event) => {
    let last = Number.NEGATIVE_INFINITY
    try {
      last = Number(win.sessionStorage?.getItem(KEY)) || Number.NEGATIVE_INFINITY
    } catch {
      // Storage blocked: reload anyway, at most as often as the error comes back.
    }
    if (now() - last < WINDOW_MS) return
    try {
      win.sessionStorage?.setItem(KEY, String(now()))
    } catch {
      // As above.
    }
    event.preventDefault()
    win.location.reload()
  })
}
