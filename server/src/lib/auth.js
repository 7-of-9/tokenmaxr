// Caller identification (SPEC "Owner auth", "Security").
import { tokenHash } from './ids.js'

// SWA injects x-ms-client-principal (base64 JSON) into managed Functions.
export function parsePrincipal(headers) {
  const raw = headers['x-ms-client-principal']
  if (!raw || typeof raw !== 'string') return null
  try {
    const p = JSON.parse(Buffer.from(raw, 'base64').toString('utf8'))
    if (!p || typeof p !== 'object') return null
    return {
      identityProvider: String(p.identityProvider || ''),
      userId: String(p.userId || ''),
      userDetails: String(p.userDetails || ''),
      userRoles: Array.isArray(p.userRoles) ? p.userRoles.map(String) : [],
    }
  } catch {
    return null
  }
}

// -> { status: 200 | 401 | 403, principal }
export function ownerDecision(headers, ownerIds = process.env.AGENTS_OWNER_IDS || '') {
  const principal = parsePrincipal(headers)
  const loggedIn = principal && principal.userId && principal.identityProvider &&
    !(principal.userRoles.length === 1 && principal.userRoles[0] === 'anonymous')
  if (!loggedIn) return { status: 401, principal: null }
  if (principal.userRoles.includes('owner')) return { status: 200, principal }
  const allowed = ownerIds.split(',').map((s) => s.trim().toLowerCase()).filter(Boolean)
  const who = `${principal.identityProvider}:${principal.userDetails}`.toLowerCase()
  return { status: allowed.includes(who) ? 200 : 403, principal }
}

const TOKEN_CACHE_MS = 60 * 1000

// Resolves X-D0M1-Token to { machineId, label, cc } (or null). Machine rows are
// few, so a partition scan is fine; hits are cached per process for a minute.
export async function machineFromToken(store, headers) {
  const token = headers['x-d0m1-token']
  if (!token || typeof token !== 'string' || token.length > 200) return null
  const hash = tokenHash(token)
  let cache = store.cache.get('tokens')
  if (!cache) store.cache.set('tokens', (cache = new Map()))
  const hit = cache.get(hash)
  if (hit && hit.until > Date.now()) return hit.machine
  const rows = await store.table('machines').list('m', { select: ['tokenHash', 'revokedAt', 'label', 'cc'] })
  const row = rows.find((r) => r.tokenHash === hash && !r.revokedAt)
  if (!row) {
    cache.delete(hash)
    return null
  }
  const machine = { machineId: row.rowKey, label: row.label || '', cc: row.cc || '' }
  cache.set(hash, { machine, until: Date.now() + TOKEN_CACHE_MS })
  return machine
}

// Keeps cached machines in step with a heartbeat this process just stored.
export function patchCachedMachine(store, machineId, patch) {
  const cache = store.cache.get('tokens')
  if (!cache) return
  for (const hit of cache.values()) if (hit.machine.machineId === machineId) Object.assign(hit.machine, patch)
}
