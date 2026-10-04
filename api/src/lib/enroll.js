// POST /api/enroll and POST /api/invite (SPEC "POST /api/enroll", "Bootstrap").
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { machineFromToken, ownerDecision, parsePrincipal } from './auth.js'
import { INVITE_RE, newInvite, newMachineId, newToken, tokenHash } from './ids.js'
import { isStatus } from './tables.js'
import { cleanLabel } from './validate.js'

export const INVITE_TTL_MS = 15 * 60 * 1000
export const JOIN_MISMATCH = 'join code must come from `tokenmaxr invite` on an enrolled machine'
const FP_RE = /^[0-9a-f]{16}$/

// Writes a single-use invite row. Shared by POST /api/invite and the
// bootstrap script.
export async function createInvite(store, now, createdBy) {
  const invite = newInvite()
  const expiresAt = new Date(now.getTime() + INVITE_TTL_MS).toISOString()
  await store.table('invites').create({
    partitionKey: 'i',
    rowKey: invite,
    expiresAt,
    usedAt: '',
    createdBy,
    createdAt: now.toISOString(),
  })
  return { invite, expiresAt }
}

function field(body, name, max) {
  const v = body[name]
  if (v == null) return ''
  if (typeof v !== 'string' || v.length > max) return null
  return v.trim()
}

export async function handleEnroll(req, rawCtx) {
  if (req.method !== 'POST') return error(405, 'method not allowed')
  const { store } = resolveCtx(rawCtx)
  const now = req.now
  const body = req.body
  if (!body || typeof body !== 'object' || Array.isArray(body)) return error(400, 'body must be a JSON object')
  const invite = typeof body.invite === 'string' ? body.invite.trim().toUpperCase() : ''
  const rawLabel = field(body, 'machineLabel', 1000)
  const os = field(body, 'os', 32)
  const arch = field(body, 'arch', 32)
  const version = field(body, 'version', 64)
  const kfp = field(body, 'kFingerprint', 16)
  if ([rawLabel, os, arch, version, kfp].includes(null)) return error(400, 'invalid field')
  // Public since v1.1: cleaned the same way as heartbeat labels.
  const machineLabel = cleanLabel(rawLabel)
  if (kfp && !FP_RE.test(kfp)) return error(400, 'kFingerprint must be 16 lowercase hex')
  if (!INVITE_RE.test(invite)) return error(403, 'invite invalid, used or expired')

  const invites = store.table('invites')
  const accounts = store.table('accounts')
  const inv = await invites.get('i', invite)
  if (!inv || inv.usedAt || !(Date.parse(inv.expiresAt) > now.getTime())) {
    return error(403, 'invite invalid, used or expired')
  }

  // Fleet key pinning: the first enrollment that carries a fingerprint pins it.
  const fleet = await accounts.get('fleet', 'k')
  if (fleet && fleet.fingerprint !== kfp) return error(409, JOIN_MISMATCH)

  // Consume the invite; the ETag makes it single-use under concurrency.
  try {
    await invites.merge({ partitionKey: 'i', rowKey: invite, usedAt: now.toISOString() }, inv.etag)
  } catch (err) {
    if (isStatus(err, 404, 412)) return error(403, 'invite invalid, used or expired')
    throw err
  }

  if (!fleet && kfp) {
    try {
      await accounts.create({ partitionKey: 'fleet', rowKey: 'k', fingerprint: kfp, pinnedAt: now.toISOString() })
    } catch (err) {
      if (!isStatus(err, 409)) throw err
      const again = await accounts.get('fleet', 'k')
      if (again?.fingerprint !== kfp) return error(409, JOIN_MISMATCH)
    }
  }

  const machineId = newMachineId()
  const token = newToken()
  const nowIso = now.toISOString()
  await store.table('machines').create({
    partitionKey: 'm',
    rowKey: machineId,
    tokenHash: tokenHash(token),
    label: machineLabel || machineId,
    os,
    arch,
    version,
    enrolledAt: nowIso,
    lastSeenAt: nowIso,
    heartbeat: '',
  })
  await invites.merge({ partitionKey: 'i', rowKey: invite, usedBy: machineId }).catch(() => {})
  return json(200, { machineId, token })
}

// Callers: an enrolled machine (X-D0M1-Token) or the owner principal.
export async function handleInvite(req, rawCtx) {
  if (req.method !== 'POST') return error(405, 'method not allowed')
  const { store, env } = resolveCtx(rawCtx)
  let createdBy = null
  if (req.headers['x-d0m1-token']) {
    const machine = await machineFromToken(store, req.headers)
    if (!machine) return error(401, 'bad token')
    createdBy = `machine:${machine.machineId}`
  } else {
    const decision = ownerDecision(req.headers, env.AGENTS_OWNER_IDS || '')
    if (decision.status === 401) return error(401, 'login or machine token required')
    if (decision.status === 403) return error(403, 'not the owner')
    // userId is opaque; userDetails can be an email, which stays out of storage.
    const p = parsePrincipal(req.headers)
    createdBy = `owner:${p.identityProvider}:${p.userId}`
  }
  return json(200, await createInvite(store, req.now, createdBy))
}
