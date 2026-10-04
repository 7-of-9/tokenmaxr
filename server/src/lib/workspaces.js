// Workspace canonicalisation for the prompt archive (SPEC "Workspaces").
// One rule set, mirrored in collector/internal/sources/workspace; both test
// suites run the shared vectors in
// collector/internal/sources/workspace/testdata/vectors.json.
//
//   1. normalise: Windows paths are case-folded with "\" separators; WSL
//      paths (/mnt/<drive>/…, \\wsl$\<distro>\…, vscode-remote://wsl+<distro>/…,
//      \home\… written with backslashes) are mapped to the path the other
//      side sees; POSIX paths keep their case. A tool's project data folder
//      (…/.cursor/projects/<slug>, …/.claude/projects/<slug>) is its slug.
//   2. slugs (Claude project-directory names such as C--Users-me-src-app,
//      WSL ones such as home-me-src-app): matched against the slug form of
//      every known real path and its ancestors; otherwise decoded best effort.
//      A 64-hex Gemini project hash is matched against sha256(real path).
//   3. fold: a workspace moves to the nearest ancestor that is itself a
//      workspace in the set and is not a generic container (a drive or share
//      root, a home or the folder of homes, or a folder named src, repos,
//      tmp, …), repeated until none applies.
import { createHash } from 'node:crypto'

// Folders that hold many unrelated projects: never a fold target.
export const GENERIC_NAMES = new Set([
  'src', 'source', 'sources', 'repos', 'repo', 'code', 'projects', 'dev', 'git', 'github',
  'work', 'workspace', 'workspaces', 'documents', 'desktop', 'downloads', 'tmp', 'temp', 'sandbox',
])

const HEX64 = /^[0-9a-f]{64}$/
const DRIVE = /^[A-Za-z]:(?:[\\/]|$)/
const UNC = /^[\\/]{2}[^\\/]/
const WSL_UNC = /^[\\/]{2}(?:wsl\$|wsl\.localhost)[\\/][^\\/]+(.*)$/i
const MNT = /^\/mnt\/([A-Za-z])(\/.*)?$/
const WSL_REMOTE = /^vscode-remote:\/\/wsl(?:\+|%2B)[^/]+(\/.*)?$/i
const TOOL_PROJECT = /[\\/]\.(?:cursor|claude)[\\/]projects[\\/]([^\\/]+)[\\/]*$/i

export function isWindowsKey(key) {
  return DRIVE.test(key) || UNC.test(key)
}

// Step 1 for one string: "" for an empty workspace; slugs, hashes and
// anything unrecognised are returned trimmed (and slugs lowercased) for
// step 2 to resolve.
export function normalize(raw) {
  let s = String(raw ?? '').trim()
  if (!s) return ''
  if (/^file:\/\//i.test(s)) {
    try {
      s = decodeURIComponent(new URL(s).pathname)
      if (/^\/[A-Za-z]:/.test(s)) s = s.slice(1)
    } catch {
      return s
    }
  }
  const remote = s.match(WSL_REMOTE)
  if (remote) {
    try {
      s = decodeURIComponent(remote[1] || '/')
    } catch {
      s = remote[1] || '/'
    }
  }
  // A tool's per-project data folder is named by the project's slug.
  const tool = s.match(TOOL_PROJECT)
  if (tool) return normalize(tool[1])
  const wsl = s.match(WSL_UNC)
  if (wsl) s = (wsl[1] || '/').replace(/\\/g, '/')
  // \home\me\x: a Linux path written with Windows separators.
  if (/^\\(?!\\)/.test(s)) s = s.replace(/\\/g, '/')
  const mnt = s.match(MNT)
  if (mnt) s = `${mnt[1]}:${mnt[2] || '\\'}`
  if (DRIVE.test(s) || UNC.test(s)) {
    const unc = UNC.test(s)
    let p = s.replace(/\//g, '\\').replace(/\\{2,}/g, '\\')
    if (unc) p = '\\' + p
    if (p.length > 3 && p.endsWith('\\')) p = p.replace(/\\+$/, '')
    if (/^[A-Za-z]:$/.test(p)) p += '\\'
    return p.toLowerCase()
  }
  if (s.startsWith('/')) {
    let p = s.replace(/\/{2,}/g, '/')
    if (p.length > 1) p = p.replace(/\/+$/, '')
    return p
  }
  if (isSlug(s)) return s.toLowerCase()
  return s
}

export function isSlug(s) {
  return !HEX64.test(s) && /^[A-Za-z0-9._-]+$/.test(s) && s.includes('-') && !/[\\/]/.test(s)
}

// The comparable form of a path or slug: lowercase, every run of
// non-alphanumerics one "-", no leading or trailing "-", and "mnt-<drive>-"
// read as "<drive>-" (Claude writes /mnt/c/x as -mnt-c-x).
export function slugForm(s) {
  const f = String(s).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return f.replace(/^mnt-([a-z])-/, '$1-')
}

// Best-effort decode of a slug with no known path behind it.
export function decodeSlug(slug) {
  let s = slug.toLowerCase().replace(/^-+/, '').replace(/^mnt-([a-z])-/, '$1-')
  const rest = (tail, sep) => (tail.startsWith('src-') ? `src${sep}${tail.slice(4)}` : tail)
  let m = s.match(/^([a-z])-+users-([^-]+)(?:-(.+))?$/)
  if (m) return `${m[1]}:\\users\\${m[2]}${m[3] ? '\\' + rest(m[3], '\\') : ''}`
  m = s.match(/^home-([^-]+)(?:-(.+))?$/)
  if (m) return `/home/${m[1]}${m[2] ? '/' + rest(m[2], '/') : ''}`
  m = s.match(/^users-([^-]+)(?:-(.+))?$/)
  if (m) return `/Users/${m[1]}${m[2] ? '/' + rest(m[2], '/') : ''}`
  return s
}

export function parentOf(key) {
  if (isWindowsKey(key)) {
    const i = key.lastIndexOf('\\')
    if (/^[a-z]:\\$/.test(key)) return null
    if (i < 0) return null
    const p = key.slice(0, i)
    if (/^[a-z]:$/.test(p)) return p + '\\'
    // \\server\share is the root of a UNC path.
    if (p.startsWith('\\\\') && p.slice(2).split('\\').length < 2) return null
    return p
  }
  if (key.startsWith('/')) {
    if (key === '/') return null
    const i = key.lastIndexOf('/')
    return i === 0 ? '/' : key.slice(0, i)
  }
  return null
}

export function baseName(key) {
  const parts = String(key).split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? key
}

// A folder that holds unrelated projects: a root, a home or the homes
// folder, /root, /mnt/<drive>, or a GENERIC_NAMES basename.
export function isGeneric(key) {
  if (!key) return true
  if (GENERIC_NAMES.has(baseName(key).toLowerCase())) return true
  if (isWindowsKey(key)) {
    if (/^[a-z]:\\$/.test(key)) return true
    if (/^[a-z]:\\users(\\[^\\]+)?$/.test(key)) return true
    if (key.startsWith('\\\\') && key.slice(2).split('\\').length <= 2) return true
    return false
  }
  if (key.startsWith('/')) {
    const k = key.toLowerCase()
    return k === '/' || /^\/(home|users)(\/[^/]+)?$/.test(k) || k === '/root' || /^\/mnt(\/[a-z])?$/.test(k)
  }
  return false
}

const sha256 = (s) => createHash('sha256').update(s, 'utf8').digest('hex')

// counts: Map or object raw -> count. Returns Map raw -> root key (every raw
// in the input, "" for an empty workspace).
export function canonicalize(counts) {
  const entries = counts instanceof Map ? [...counts] : Object.entries(counts)
  const norm = new Map()
  for (const [raw] of entries) norm.set(raw, normalize(raw))
  const weight = new Map()
  for (const [raw, n] of entries) {
    const k = norm.get(raw)
    weight.set(k, (weight.get(k) || 0) + (Number(n) || 0))
  }

  // Known real paths and their ancestors, by slug form and by sha256.
  const bySlug = new Map()
  const byHash = new Map()
  const offer = (map, form, key) => {
    const prev = map.get(form)
    if (!prev || (weight.get(key) || 0) > (weight.get(prev) || 0) || ((weight.get(key) || 0) === (weight.get(prev) || 0) && key < prev)) {
      map.set(form, key)
    }
  }
  for (const [raw] of entries) {
    const k = norm.get(raw)
    if (!isRealPath(k)) continue
    for (let a = k; a; a = parentOf(a)) offer(bySlug, slugForm(a), a)
    // Gemini hashes the project root as the tool saw it.
    offer(byHash, sha256(String(raw).trim()), k)
  }

  const resolve = (k) => {
    if (!k || isRealPath(k)) return k
    if (HEX64.test(k)) return byHash.get(k) || k
    if (isSlug(k)) return bySlug.get(slugForm(k)) || decodeSlug(k)
    return k
  }

  const resolved = new Map()
  for (const [raw] of entries) resolved.set(raw, resolve(norm.get(raw)))
  const set = new Set([...resolved.values()].filter(Boolean))

  const fold = (k) => {
    for (let guard = 0; guard < 64; guard++) {
      let next = null
      for (let a = parentOf(k); a; a = parentOf(a)) {
        if (set.has(a) && !isGeneric(a)) {
          next = a
          break
        }
      }
      if (!next) return k
      k = next
    }
    return k
  }
  const out = new Map()
  for (const [raw, k] of resolved) out.set(raw, k && isRealPath(k) ? fold(k) : k)
  return out
}

export function isRealPath(k) {
  return Boolean(k) && (isWindowsKey(k) || k.startsWith('/'))
}

// Facet groups: [{ key, label, path, paths, count }], largest first. path is
// the root as most often written (original case); paths are the raw
// workspaces folded into it, most frequent first (at most 50).
export function workspaceGroups(counts, rootOf = canonicalize(counts)) {
  const entries = counts instanceof Map ? [...counts] : Object.entries(counts)
  const groups = new Map()
  for (const [raw, n] of entries) {
    const key = rootOf.get(raw) ?? normalize(raw)
    let g = groups.get(key)
    if (!g) groups.set(key, (g = { key, count: 0, raws: [] }))
    g.count += Number(n) || 0
    g.raws.push([raw, Number(n) || 0])
  }
  const out = []
  for (const g of groups.values()) {
    g.raws.sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1))
    const exact = g.raws.find(([raw]) => normalize(raw) === g.key)
    let path = exact ? String(exact[0]).trim() : g.key
    if (!exact && isWindowsKey(g.key)) {
      // Recover the case from the longest raw path under this root.
      const under = g.raws.map(([raw]) => String(raw).trim().replace(/\//g, '\\')).find((r) => r.toLowerCase().startsWith(g.key + '\\'))
      if (under) path = under.slice(0, g.key.length)
    }
    const label = !g.key ? '(no workspace)' : HEX64.test(g.key) ? `gemini ${g.key.slice(0, 8)}` : baseName(path)
    out.push({ key: g.key || 'none', label, path, paths: g.raws.slice(0, 50).map(([raw]) => raw), count: g.count })
  }
  return out.sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
}
