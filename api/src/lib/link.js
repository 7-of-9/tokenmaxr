// POST /api/link (SPEC "Linking a machine"): the owner's browser asks for a
// ready-made join code, so a collector run on a new machine enrolls without
// anyone typing one. The page hands the code to the collector's loopback
// listener; the code is still a single-use invite plus the fleet key K.
//
// K is escrowed here, encrypted with PROMPT_ENC_KEY (AAD "fleet-k"): the same
// trust the prompt archive already places in the server. The first link on
// an empty fleet generates K; enroll pins its fingerprint as before. A fleet
// whose K was made by a machine (pinned, nothing escrowed) still links
// through `tokenmaxr invite` on that machine.
import { randomBytes } from 'node:crypto'
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { ownerDecision, parsePrincipal } from './auth.js'
import { createInvite } from './enroll.js'
import { decryptJson, encryptJson, parseKey } from './crypto.js'
import { isStatus } from './tables.js'

const AAD = 'fleet-k'
const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
export const NOT_ESCROWED = 'this fleet key was made by a machine: run `tokenmaxr invite` on an enrolled machine'

// RFC 4648 base32 without padding, as the collector's joincode package reads it.
export function base32(buf) {
  let bits = 0
  let value = 0
  let out = ''
  for (const byte of buf) {
    value = (value << 8) | byte
    bits += 8
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31]
      bits -= 5
    }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31]
  return out
}

// The escrowed fleet key, created on the first link of an empty fleet.
// -> Buffer, or null when a machine-made K is already pinned.
export async function fleetKey(store, key, now) {
  const accounts = store.table('accounts')
  const escrow = await accounts.get('fleet', 'kenc')
  if (escrow?.enc) return Buffer.from(decryptJson(key, AAD, escrow.enc), 'base64')
  if (await accounts.get('fleet', 'k')) return null
  const k = randomBytes(32)
  try {
    await accounts.create({ partitionKey: 'fleet', rowKey: 'kenc', enc: encryptJson(key, AAD, k.toString('base64')), createdAt: now.toISOString() })
    return k
  } catch (err) {
    if (!isStatus(err, 409)) throw err
    // A concurrent first link won: use its key.
    const again = await accounts.get('fleet', 'kenc')
    return Buffer.from(decryptJson(key, AAD, again.enc), 'base64')
  }
}

export async function handleLink(req, rawCtx) {
  if (req.method !== 'POST') return error(405, 'method not allowed')
  const { store, env } = resolveCtx(rawCtx)
  const decision = ownerDecision(req.headers, env.AGENTS_OWNER_IDS || '')
  if (decision.status === 401) return error(401, 'login required')
  if (decision.status === 403) return error(403, 'not the owner')
  let key
  try {
    key = parseKey(env.PROMPT_ENC_KEY)
  } catch {
    return error(503, 'PROMPT_ENC_KEY is not set')
  }
  const k = await fleetKey(store, key, req.now)
  if (!k) return error(409, NOT_ESCROWED)
  const p = parsePrincipal(req.headers)
  const { invite, expiresAt } = await createInvite(store, req.now, `link:${p.identityProvider}:${p.userId}`)
  return json(200, { join: `D0M1-${invite}-${base32(k)}`, expiresAt }, { 'Cache-Control': 'private, no-store' })
}
