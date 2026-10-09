// Path-keyed records for unlisted routes. A record is stored under a hash of its route
// path and XOR-scrambled with a keystream seeded from that same path, so the client
// bundle holds neither the path nor the record as text: the route itself is the key.
// Obfuscation, not encryption: anyone who guesses a path can read its record.
// Plain JS so the build (scripts/generate-cv-routes.js) and the app share one copy.

const fnv1a = (text, seed) => {
  let hash = seed >>> 0
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash
}

const hex = (value) => value.toString(16).padStart(8, '0')

/** Lowercase, no trailing slash: how the router already matched these routes. */
export function normalizeRoutePath(pathname) {
  const path = String(pathname).split(/[?#]/)[0].toLowerCase().replace(/\/+$/, '')
  return path || '/'
}

export function routeRecordId(pathname) {
  const path = normalizeRoutePath(pathname)
  return hex(fnv1a(`id|${path}`, 0x811c9dc5)) + hex(fnv1a(`id|${path}`, 0x2f6b9a37))
}

// sfc32, seeded from the path.
function keystream(path, length) {
  let a = fnv1a(`k0|${path}`, 0x811c9dc5)
  let b = fnv1a(`k1|${path}`, 0x9e3779b9)
  let c = fnv1a(`k2|${path}`, 0x85ebca6b)
  let d = fnv1a(`k3|${path}`, 0xc2b2ae35)
  const bytes = new Uint8Array(length)
  for (let i = 0; i < length; i++) {
    const t = (((a + b) >>> 0) + d) >>> 0
    d = (d + 1) >>> 0
    a = b ^ (b >>> 9)
    b = (c + (c << 3)) >>> 0
    c = ((c << 21) | (c >>> 11)) >>> 0
    c = (c + t) >>> 0
    bytes[i] = t & 0xff
  }
  return bytes
}

function scramble(path, bytes) {
  const key = keystream(path, bytes.length + 16).subarray(16)
  return bytes.map((byte, i) => byte ^ key[i])
}

export function sealRouteRecord(pathname, value) {
  const path = normalizeRoutePath(pathname)
  const bytes = scramble(path, new TextEncoder().encode(JSON.stringify(value)))
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  // base64url: no '/' in the sealed text to read as a path.
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

/** The record, or null when the path is not the one it was sealed with. */
export function openRouteRecord(pathname, sealed) {
  const path = normalizeRoutePath(pathname)
  try {
    const binary = atob(sealed.replaceAll('-', '+').replaceAll('_', '/'))
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    const value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(scramble(path, bytes)))
    return value && value.routePath === path ? value : null
  } catch {
    return null
  }
}
