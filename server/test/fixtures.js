// Synthetic test data only: no real prompts, accounts or paths.
import { randomBytes } from 'node:crypto'
import { accountHash, eventId, kFingerprint, sessionKey } from '../src/lib/ids.js'

export const K = Buffer.alloc(32, 0x5a)
export const KFP = kFingerprint(K)
export const ACCT = {
  anthropic: accountHash(K, 'anthropic', 'synthetic-claude-account'),
  openai: accountHash(K, 'openai', 'synthetic-openai-account'),
  xai: accountHash(K, 'xai', 'synthetic-xai-account'),
  cursor: accountHash(K, 'cursor', 'synthetic-cursor-account'),
  google: accountHash(K, 'google', 'synthetic-google-account'),
}
// Synthetic scan roots for heartbeats: private, never in a response.
export const HOMES = ['/synthetic/home/user', '\\\\wsl$\\Synthetic-Distro\\home\\user']
export const SESSION = sessionKey('anthropic', 'synthetic-session-1')
export const testKey = () => randomBytes(32).toString('base64')

export function principal(identityProvider, userDetails, roles = ['anonymous', 'authenticated']) {
  const p = { identityProvider, userId: 'uid-' + userDetails, userDetails, userRoles: roles }
  return Buffer.from(JSON.stringify(p)).toString('base64')
}

export function usage(nativeKey, over = {}) {
  return {
    id: eventId('usage', 'anthropic', 'claude-code', nativeKey),
    provider: 'anthropic',
    source: 'claude-code',
    ts: '2026-09-20T10:00:00.000Z',
    tzOffsetMin: 60,
    model: 'claude-test-model',
    acct: ACCT.anthropic,
    acctQ: 'timeline',
    q: 'exact',
    pv: 1,
    session: SESSION,
    in: 100, cacheW: 20, cacheR: 1000, out: 50, reasoning: 0, calls: 1,
    ...over,
  }
}

export function codexUsage(nativeKey, over = {}) {
  return usage(nativeKey, {
    id: eventId('usage', 'openai', 'codex', nativeKey),
    provider: 'openai', source: 'codex', model: 'gpt-test', acct: ACCT.openai, session: '',
    ...over,
  })
}

export function grokUsage(nativeKey, over = {}) {
  return usage(nativeKey, {
    id: eventId('usage', 'xai', 'grok-cli', nativeKey),
    provider: 'xai', source: 'grok-cli', model: 'grok-test', acct: ACCT.xai, q: 'estimated', session: '',
    ...over,
  })
}

// Cursor reports no cache split (SPEC "Cursor"): in/out only, exact.
export function cursorUsage(nativeKey, over = {}) {
  return usage(nativeKey, {
    id: eventId('usage', 'cursor', 'cursor', nativeKey),
    provider: 'cursor', source: 'cursor', model: 'cursor-test-model', acct: ACCT.cursor, session: '',
    cacheW: 0, cacheW1h: 0, cacheR: 0, reasoning: 0,
    ...over,
  })
}

// Gemini CLI (SPEC "Gemini CLI"): cached input split out, no cache writes,
// thoughts inside out and repeated as reasoning, exact.
export function geminiUsage(nativeKey, over = {}) {
  return usage(nativeKey, {
    id: eventId('usage', 'google', 'gemini-cli', nativeKey),
    provider: 'google', source: 'gemini-cli', model: 'gemini-test-model', acct: ACCT.google, session: '',
    in: 120, cacheW: 0, cacheW1h: 0, cacheR: 800, out: 60, reasoning: 25,
    ...over,
  })
}

export function activity(nativeKey, over = {}) {
  return {
    id: eventId('activity', 'anthropic', 'claude-code', nativeKey),
    provider: 'anthropic',
    source: 'claude-code',
    ts: '2026-09-20T09:59:00.000Z',
    tzOffsetMin: 60,
    acct: ACCT.anthropic,
    acctQ: 'timeline',
    session: SESSION,
    hasUsage: true,
    ...over,
  }
}

export function prompt(nativeKey, over = {}) {
  return {
    id: eventId('prompt', 'anthropic', 'claude-code', nativeKey),
    provider: 'anthropic',
    source: 'claude-code',
    ts: '2026-09-20T09:59:00.000Z',
    tzOffsetMin: 60,
    model: 'claude-test-model',
    acct: ACCT.anthropic,
    acctLabel: 'synthetic-label',
    workspace: '/synthetic/workspace',
    machine: 'test-machine',
    session: SESSION,
    text: `synthetic prompt ${nativeKey}`,
    ...over,
  }
}

// A deterministic pseudo-random string that does not compress, so its
// ciphertext really exceeds the inline limit.
export function bigText(bytes) {
  let s = ''
  let x = 12345
  while (s.length < bytes) {
    x = (x * 1103515245 + 12345) & 0x7fffffff
    s += (x % 36).toString(36)
  }
  return s
}

export function batch(parts) {
  return { v: 1, collectorVersion: '0.0.0-test', sentAt: '2026-09-29T10:00:00Z', ...parts }
}
