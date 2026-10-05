// reloadOnStaleChunks: one reload per minute when a lazy chunk of an older build is gone.
// Run: node --experimental-strip-types --no-warnings src/utils/staleChunks.test.ts
import assert from 'node:assert/strict'
import test from 'node:test'
import { reloadOnStaleChunks } from './staleChunks.ts'

function fakeWindow(storage: Map<string, string> | null) {
  let listener: ((event: Event) => void) | null = null
  const win = {
    reloads: 0,
    addEventListener: (_: 'vite:preloadError', l: (event: Event) => void) => { listener = l },
    sessionStorage: storage
      ? { getItem: (k: string) => storage.get(k) ?? null, setItem: (k: string, v: string) => { storage.set(k, v) } }
      : { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } },
    location: { reload: () => { win.reloads++ } },
    fire() {
      let prevented = false
      listener!({ preventDefault: () => { prevented = true } } as Event)
      return prevented
    },
  }
  return win
}

test('reloads once, then lets a repeat within a minute through', () => {
  let t = 1_000_000
  const win = fakeWindow(new Map())
  reloadOnStaleChunks(win, () => t)
  assert.equal(win.fire(), true)
  assert.equal(win.reloads, 1)
  t += 30_000
  assert.equal(win.fire(), false)
  assert.equal(win.reloads, 1)
  t += 31_000
  assert.equal(win.fire(), true)
  assert.equal(win.reloads, 2)
})

test('blocked storage still reloads', () => {
  const win = fakeWindow(null)
  reloadOnStaleChunks(win, () => 5)
  assert.equal(win.fire(), true)
  assert.equal(win.reloads, 1)
})
