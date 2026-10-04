// Id helpers mirrored from collector/internal/model (docs/agents/SPEC.md "IDs").
// The server never derives event ids itself; these exist so tests and tools
// can build ids exactly the way the collector does.
import { createHash, createHmac, randomBytes } from 'node:crypto'

const sha256Hex = (s) => createHash('sha256').update(s, 'utf8').digest('hex')

export function eventId(kind, provider, source, nativeKey) {
  return sha256Hex(`v1|${kind}|${provider}|${source}|${nativeKey}`).slice(0, 32)
}

export function sessionKey(provider, sessionId) {
  if (!sessionId) return ''
  return 's_' + sha256Hex(`${provider}|${sessionId}`).slice(0, 16)
}

export function accountHash(k, provider, nativeAccountId) {
  return 'a_' + createHmac('sha256', k).update(`${provider}|${nativeAccountId}`, 'utf8').digest('hex').slice(0, 16)
}

export function kFingerprint(k) {
  return createHash('sha256').update(k).digest('hex').slice(0, 16)
}

export function tokenHash(token) {
  return sha256Hex(token)
}

// Machine tokens: 32 random bytes as unpadded base64url (43 chars).
export function newToken() {
  return randomBytes(32).toString('base64url')
}

export function newMachineId() {
  return 'm_' + randomBytes(8).toString('hex')
}

const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'

// Invites are 24 RFC 4648 base32 chars (15 random bytes): no '-' so the join
// code D0M1-<invite>-<base32(K)> splits cleanly.
export function newInvite() {
  const bytes = randomBytes(15)
  let bits = 0
  let value = 0
  let out = ''
  for (const b of bytes) {
    value = (value << 8) | b
    bits += 8
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31]
      bits -= 5
    }
  }
  return out
}

export const INVITE_RE = /^[A-Z2-7]{24}$/
