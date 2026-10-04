// After `vite build --config vite.pages.config.ts` (npm run build:pages): stamps the built GitHub Pages
// dashboard (pages/site) with site/version.json and mirrors it into collector/internal/ghpub/site, which the
// collector embeds (go:embed cannot reach outside its module) and commits to each user's usage repository
// when its copy is newer than the repository's.
//
// version.json is {"builtAt", "hash"}: hash covers every other file (sha256 of "<sha256 of file>  <path>\n"
// lines in path order; collector/internal/ghpub/site.go computes the same), and builtAt changes only when
// the hash does, so rebuilding unchanged sources changes nothing. Collectors compare builtAt: a machine
// never replaces a newer dashboard with its older one.
//
// Usage: node scripts/sync-pages-site.mjs          stamp and mirror
//        node scripts/sync-pages-site.mjs --check  fail unless both copies are stamped and identical
import { createHash } from 'node:crypto'
import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const repo = join(dirname(fileURLToPath(import.meta.url)), '..')
export const PAGES_SITE = join(repo, 'pages', 'site')
export const EMBED_SITE = join(repo, 'collector', 'internal', 'ghpub', 'site')
export const VERSION_FILE = 'version.json'

const sha256 = (data) => createHash('sha256').update(data).digest('hex')

/** Every file below dir as a sorted list of '/'-separated relative paths. */
export function listFiles(dir) {
  if (!existsSync(dir)) return []
  const out = []
  const walk = (d) => {
    for (const name of readdirSync(d)) {
      const p = join(d, name)
      if (statSync(p).isDirectory()) walk(p)
      else out.push(relative(dir, p).split(sep).join('/'))
    }
  }
  walk(dir)
  // Byte order, as Go sorts strings (paths are ASCII).
  return out.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
}

/** The content hash of a site folder: every file but version.json. */
export function siteHash(dir) {
  const lines = listFiles(dir)
    .filter((p) => p !== VERSION_FILE)
    .map((p) => `${sha256(readFileSync(join(dir, p)))}  ${p}\n`)
  return sha256(lines.join(''))
}

function readVersion(dir) {
  try {
    const v = JSON.parse(readFileSync(join(dir, VERSION_FILE), 'utf8'))
    return typeof v.builtAt === 'string' && typeof v.hash === 'string' ? v : null
  } catch {
    return null
  }
}

const iso = (ms) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')

/**
 * Writes `from`/version.json (builtAt kept from `to`'s copy when the hash is unchanged, otherwise now and
 * always after the previous builtAt) and makes `to` an exact copy of `from`. Returns the version.
 */
export function stampAndMirror(from, to, now = Date.now()) {
  if (!existsSync(join(from, 'index.html'))) throw new Error(`${from} has no index.html: run the vite build first`)
  const hash = siteHash(from)
  const prev = readVersion(to)
  let builtAt = iso(now)
  if (prev && prev.hash === hash) builtAt = prev.builtAt
  else if (prev && Date.parse(prev.builtAt) >= Date.parse(builtAt)) builtAt = iso(Date.parse(prev.builtAt) + 1000)
  const version = { builtAt, hash }
  writeFileSync(join(from, VERSION_FILE), JSON.stringify(version, null, 1) + '\n')

  const want = new Set(listFiles(from))
  for (const p of listFiles(to)) if (!want.has(p)) rmSync(join(to, p))
  // Folders emptied by the removals above.
  const prune = (d) => {
    for (const name of readdirSync(d)) {
      const p = join(d, name)
      if (statSync(p).isDirectory()) {
        prune(p)
        if (!readdirSync(p).length) rmSync(p, { recursive: true })
      }
    }
  }
  if (existsSync(to)) prune(to)
  for (const p of want) {
    mkdirSync(dirname(join(to, p)), { recursive: true })
    copyFileSync(join(from, p), join(to, p))
  }
  return version
}

/** Problems that make the two copies unusable for the collector (empty: fine). */
export function check(from, to) {
  const problems = []
  for (const dir of [from, to]) {
    const v = readVersion(dir)
    if (!v) problems.push(`${dir}: no valid ${VERSION_FILE}`)
    else if (v.hash !== siteHash(dir)) problems.push(`${dir}: ${VERSION_FILE} does not match the files (edited by hand?)`)
  }
  const a = listFiles(from)
  const b = listFiles(to)
  if (a.join('\n') !== b.join('\n')) problems.push(`${from} and ${to} hold different files`)
  else for (const p of a) if (!readFileSync(join(from, p)).equals(readFileSync(join(to, p)))) problems.push(`${p} differs`)
  return problems
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  if (process.argv.includes('--check')) {
    const problems = check(PAGES_SITE, EMBED_SITE)
    if (problems.length) {
      console.error(problems.join('\n') + '\nRun npm run build:pages.')
      process.exit(1)
    }
    console.log('pages site: stamped, and identical to the collector copy')
  } else {
    const v = stampAndMirror(PAGES_SITE, EMBED_SITE)
    console.log(`pages site ${v.builtAt} (${v.hash.slice(0, 12)}) -> collector/internal/ghpub/site`)
  }
}
