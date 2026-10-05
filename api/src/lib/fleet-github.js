// GET / PUT / DELETE /api/fleet/github (SPEC "Fleet GitHub sign-in"): one
// enrolled machine shares its GitHub sign-in with the rest of the fleet, so
// the other machines publish to the owner's repository without signing in.
//
// The blob is opaque here. The collectors encrypt it end to end with the
// fleet key K (AES-256-GCM, "tmx1" prefix); the server only checks its shape,
// never tries to decrypt it, and never logs it. Any enrolled machine may read,
// replace or delete the one share; the collector decides whose it is ("by").
// PUT reports whether K is escrowed here (a fleet linked through /api/link):
// then whoever holds PROMPT_ENC_KEY and the storage could open the share, and
// the collector says so instead of "your server cannot read it".
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { machineFromToken } from './auth.js'

export const FLEET_SECRETS = 'fleetsecrets'
export const MAX_BLOB_BYTES = 4096
const PK = 'fleet'
const RK = 'github'
const PREFIX = 'tmx1'
const PRIVATE = { 'Cache-Control': 'private, no-store' }
// Standard base64 with padding, as Go's base64.StdEncoding writes it.
const B64_RE = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/
const MAX_BLOB_CHARS = Math.ceil(MAX_BLOB_BYTES / 3) * 4

// Every answer, errors included, stays out of shared caches.
function priv(res) {
  return { ...res, headers: { ...res.headers, ...PRIVATE } }
}

// -> true when the fleet key is escrowed (link.js fleetKey).
async function escrowed(store) {
  return !!(await store.table('accounts').get('fleet', 'kenc'))?.enc
}

// -> null when the blob is a well-formed share, else the reason.
export function blobProblem(blob) {
  if (typeof blob !== 'string' || blob === '') return 'blob must be a base64 string'
  if (blob.length > MAX_BLOB_CHARS) return `blob is over ${MAX_BLOB_BYTES} bytes`
  if (!B64_RE.test(blob)) return 'blob must be a base64 string'
  const raw = Buffer.from(blob, 'base64')
  if (raw.length > MAX_BLOB_BYTES) return `blob is over ${MAX_BLOB_BYTES} bytes`
  if (raw.subarray(0, PREFIX.length).toString('latin1') !== PREFIX) return `blob must start with "${PREFIX}"`
  return null
}

export async function handleFleetGitHub(req, rawCtx) {
  if (!['GET', 'PUT', 'DELETE'].includes(req.method)) return priv(error(405, 'method not allowed'))
  const { store } = resolveCtx(rawCtx)
  const machine = await machineFromToken(store, req.headers)
  if (!machine) return priv(error(401, 'bad token'))
  const t = store.table(FLEET_SECRETS)

  if (req.method === 'GET') {
    const row = await t.get(PK, RK)
    if (!row?.blob) return priv(error(404, 'no shared sign-in'))
    return json(200, { blob: row.blob, updatedAt: row.updatedAt, by: row.by }, PRIVATE)
  }

  if (req.method === 'DELETE') {
    await t.delete(PK, RK)
    return { status: 204, headers: { ...PRIVATE } }
  }

  const body = req.body
  if (!body || typeof body !== 'object' || Array.isArray(body)) return priv(error(400, 'body must be a JSON object'))
  const problem = blobProblem(body.blob)
  if (problem) return priv(error(400, problem))
  const updatedAt = req.now.toISOString()
  await t.upsertMerge({ partitionKey: PK, rowKey: RK, blob: body.blob, updatedAt, by: machine.machineId })
  return json(200, { updatedAt, escrowed: await escrowed(store) }, PRIVATE)
}
// runHandler's own answers (bad JSON, too large, storage down) carry it too.
handleFleetGitHub.responseHeaders = PRIVATE
