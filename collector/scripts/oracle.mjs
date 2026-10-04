#!/usr/bin/env node
/**
 * Independent oracle for `tokenmaxr scan --dry-run --json`.
 *
 * A from-scratch Node re-implementation of the counting rules in
 * docs/agents/SPEC.md (plus the parser refinements agreed during the build and
 * recorded in the sources report). It shares no code with the Go parsers: it
 * reads the same local logs, applies the rules, merges events by id the way the
 * server does and prints the same JSON shape, so the two can be diffed per
 * source and month.
 *
 * Usage:
 *   node collector/scripts/oracle.mjs [--home DIR]... [--wsl] [--since D] [--until D] [--checks]
 *   node collector/scripts/oracle.mjs --until 2026-09-29T10:00:00Z --compare go.json
 *
 *   --home DIR      a home to read; repeatable (SPEC "Scan roots": every source and
 *                   month total covers all homes, merged by id). Default: D0M1_USER_HOME,
 *                   else the OS home. The first home is the OS home: CODEX_HOME and
 *                   D0M1_CURSOR_DB apply to it only (needs Node >= 22.13 for node:sqlite)
 *   --wsl           Windows: add the homes of *running* WSL distros, found the way the
 *                   collector finds them (`wsl.exe -l -q` / `-l --running -q`, then
 *                   \\wsl$\<distro>\home\* and \root holding .claude/.codex/.grok/.gemini).
 *                   A distro that is not running is reported on stderr and never touched,
 *                   because opening \\wsl$\<distro> would boot it.
 *   --since/--until YYYY-MM-DD (event's local date) or RFC 3339 (event ts), inclusive
 *   --checks        add a "checks" object: Grok turn_completed vs usage.json session
 *                   totals, and Codex files that mix token_usage_record and token_count
 *   --compare FILE  diff against a saved `scan --dry-run --json` report; prints a
 *                   table and exits 1 on any difference
 *
 * Output never contains prompt text, emails or tokens: only counts and sums.
 */

import { execFileSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const PENDING_MS = 10 * 60 * 1000

// ---------- arguments ----------

function parseArgs(argv) {
  const a = { homes: [], wsl: false, since: null, until: null, checks: false, compare: null }
  for (let i = 0; i < argv.length; i++) {
    const k = argv[i]
    const v = () => {
      if (i + 1 >= argv.length) throw new Error(`${k} needs a value`)
      return argv[++i]
    }
    if (k === '--home') a.homes.push(v())
    else if (k === '--wsl') a.wsl = true
    else if (k === '--since') a.since = v()
    else if (k === '--until') a.until = v()
    else if (k === '--checks') a.checks = true
    else if (k === '--compare') a.compare = v()
    else if (k === '--json') {
      // JSON is the only output format; accepted for symmetry with the collector.
    } else throw new Error(`unknown argument ${k}`)
  }
  if (!a.homes.length) a.homes.push(process.env.D0M1_USER_HOME || os.homedir())
  return a
}

// ---------- homes (SPEC "Scan roots: multiple homes") ----------

const TOOL_DIRS = ['.claude', '.codex', '.grok', '.gemini']

const isDir = (p) => {
  try {
    return fs.statSync(p).isDirectory()
  } catch {
    return false
  }
}

// Decodes `wsl.exe -l -q` output: UTF-16LE (optional BOM, CRLF), one name per
// line; UTF-8 when WSL_UTF8=1 wrote it (no NUL bytes at all). Lines that could
// not be a distro name (messages, path characters) are dropped.
function wslDistros(buf) {
  if (!buf || !buf.length) return []
  const text = buf.includes(0) ? buf.toString('utf16le') : buf.toString('utf8')
  const names = []
  for (const raw of text.split('\n')) {
    const line = raw.replace(/^[\r\t \0\ufeff]+|[\r\t \0\ufeff]+$/g, '')
    if (!line || /[ \t\\/:*?"<>|]/.test(line)) continue
    if (!names.includes(line)) names.push(line)
  }
  return names
}

// Runs the real wsl.exe from System32 (never one on the PATH). A non-zero exit
// means nothing installed or running: an empty list, not an error. null when
// there is no wsl.exe at all.
function wslList(runningOnly) {
  const exe = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'wsl.exe')
  if (!exists(exe)) return null
  try {
    return wslDistros(execFileSync(exe, runningOnly ? ['-l', '--running', '-q'] : ['-l', '-q'], { timeout: 15000, windowsHide: true, stdio: ['ignore', 'pipe', 'ignore'] }))
  } catch {
    return []
  }
}

// The homes inside running distros: \\wsl$\<distro>\home\* and \root that hold
// a tool directory. Listing does not start anything; reading a stopped distro
// would, so those are only reported.
function discoverWsl() {
  if (process.platform !== 'win32') return []
  const all = wslList(false)
  if (all === null) {
    console.error('oracle: wsl.exe not found; --wsl ignored')
    return []
  }
  if (!all.length) return []
  const running = wslList(true)
  for (const d of all) if (!running.includes(d)) console.error(`oracle: wsl: ${d} is not running, skipped (never started)`)
  const homes = []
  for (const d of running) {
    const base = path.join('\\\\wsl$', d)
    const cands = []
    try {
      for (const e of fs.readdirSync(path.join(base, 'home'), { withFileTypes: true })) if (e.isDirectory()) cands.push(path.join(base, 'home', e.name))
    } catch {
      // No /home (a utility distro): only /root is a candidate.
    }
    cands.push(path.join(base, 'root'))
    cands.sort()
    for (const c of cands) if (TOOL_DIRS.some((t) => isDir(path.join(c, t)))) homes.push(c)
  }
  return homes
}

// The OS home first, then the others, cleaned and deduplicated (case-folded
// on Windows, where the file system ignores case), missing ones dropped.
function homesList(args) {
  const out = []
  const seen = new Set()
  const add = (h, must) => {
    const abs = path.resolve(h)
    const key = process.platform === 'win32' ? abs.toLowerCase() : abs
    if (seen.has(key)) return
    if (!isDir(abs)) {
      if (must) throw new Error(`--home ${h}: not a directory`)
      return
    }
    seen.add(key)
    out.push(abs)
  }
  for (const h of args.homes) add(h, true)
  if (args.wsl) for (const h of discoverWsl()) add(h, false)
  return out
}

function parseBound(s) {
  if (!s) return null
  if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return { raw: s, date: s }
  const ms = Date.parse(s)
  if (!/^\d{4}-\d{2}-\d{2}T/.test(s) || Number.isNaN(ms)) throw new Error(`bad bound ${s}`)
  return { raw: s, ms }
}

// ---------- time helpers ----------

// Minutes east of UTC on this machine at the instant ms.
const tzAt = (ms) => -new Date(ms).getTimezoneOffset()
const localDate = (ms, tz) => new Date(ms + tz * 60000).toISOString().slice(0, 10)

function inRange(since, until, ms, tz) {
  if (since) {
    if (since.date ? localDate(ms, tz) < since.date : ms < since.ms) return false
  }
  if (until) {
    if (until.date ? localDate(ms, tz) > until.date : ms > until.ms) return false
  }
  return true
}

// RFC 3339 with a zone, as written by the tools; null when absent or invalid.
function isoMs(s) {
  if (typeof s !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(s)) return null
  const ms = Date.parse(s)
  return Number.isNaN(ms) ? null : ms
}

const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)
const str = (v) => (typeof v === 'string' ? v : '')

// ---------- ids ----------

const eventId = (kind, provider, source, nativeKey) =>
  crypto.createHash('sha256').update(`v1|${kind}|${provider}|${source}|${nativeKey}`).digest('hex').slice(0, 32)

// ---------- line reading ----------

// Yields every complete '\n'-terminated line as a Buffer (CR stripped). A final
// line without '\n' is still being written and is ignored, as the collector does.
async function* lines(file, wanted) {
  let rest = null
  for await (const chunk of fs.createReadStream(file, { highWaterMark: 1 << 22 })) {
    let buf = rest ? Buffer.concat([rest, chunk]) : chunk
    let start = 0
    let nl
    while ((nl = buf.indexOf(10, start)) !== -1) {
      let end = nl
      if (end > start && buf[end - 1] === 13) end--
      const line = buf.subarray(start, end)
      if (!wanted || wanted(line)) yield line
      else yield null
      start = nl + 1
    }
    rest = start < buf.length ? Buffer.from(buf.subarray(start)) : null
    buf = null
  }
}

function parse(buf) {
  if (!buf || buf.length === 0) return null
  try {
    const o = JSON.parse(buf.toString('utf8'))
    return o && typeof o === 'object' && !Array.isArray(o) ? o : null
  } catch {
    return null
  }
}

function* walk(dir, match) {
  let entries
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true })
  } catch {
    return
  }
  for (const e of entries) {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) yield* walk(p, match)
    else if (e.isFile() && match(e.name)) yield p
  }
}

const exists = (p) => {
  try {
    return fs.statSync(p).isFile()
  } catch {
    return false
  }
}

// ---------- the merged store for one source ----------

// Token buckets per usage event. cacheW1h is the 1-hour-TTL part of cacheW:
// merged and summed like the others, but never added into any total.
const TOKEN_FIELDS = ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'calls']

class Store {
  constructor(provider, source) {
    this.provider = provider
    this.source = source
    this.files = 0
    this.usage = new Map()
    this.activity = new Map()
    this.prompts = new Map()
  }

  // Server merge rule for one pv: fieldwise max, ts = min.
  addUsage(nativeKey, ms, t) {
    const id = eventId('usage', this.provider, this.source, nativeKey)
    const have = this.usage.get(id)
    if (!have) {
      this.usage.set(id, { ms, ...t })
      return
    }
    for (const f of TOKEN_FIELDS) have[f] = Math.max(have[f], t[f])
    if (ms < have.ms) have.ms = ms
  }

  addActivity(nativeKey, ms, hasUsage) {
    const id = eventId('activity', this.provider, this.source, nativeKey)
    const have = this.activity.get(id)
    if (!have) this.activity.set(id, { ms, hasUsage })
    else {
      have.ms = Math.min(have.ms, ms)
      have.hasUsage = have.hasUsage || hasUsage
    }
  }

  addPrompt(nativeKey, ms) {
    const id = eventId('prompt', this.provider, this.source, nativeKey)
    const have = this.prompts.get(id)
    if (!have || ms < have.ms) this.prompts.set(id, { ms })
  }

  totals(since, until) {
    const zeroUsage = () => ({ events: 0, in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, effective: 0 })
    const st = {
      provider: this.provider,
      files: this.files,
      usage: zeroUsage(),
      activity: { withUsage: 0, noUsage: 0 },
      prompts: 0,
      firstTs: null,
      lastTs: null,
      byMonth: {},
    }
    let first = Infinity
    let last = -Infinity
    const month = (ms, tz) => {
      const m = localDate(ms, tz).slice(0, 7)
      return (st.byMonth[m] ??= { usage: zeroUsage(), activity: { withUsage: 0, noUsage: 0 }, prompts: 0 })
    }
    const span = (ms) => {
      first = Math.min(first, ms)
      last = Math.max(last, ms)
    }
    const addU = (u, e) => {
      u.events++
      for (const f of TOKEN_FIELDS) u[f] += e[f]
    }
    for (const e of this.usage.values()) {
      const tz = tzAt(e.ms)
      if (!inRange(since, until, e.ms, tz)) continue
      addU(st.usage, e)
      addU(month(e.ms, tz).usage, e)
      span(e.ms)
    }
    for (const e of this.activity.values()) {
      const tz = tzAt(e.ms)
      if (!inRange(since, until, e.ms, tz)) continue
      const k = e.hasUsage ? 'withUsage' : 'noUsage'
      st.activity[k]++
      month(e.ms, tz).activity[k]++
      span(e.ms)
    }
    for (const e of this.prompts.values()) {
      const tz = tzAt(e.ms)
      if (!inRange(since, until, e.ms, tz)) continue
      st.prompts++
      month(e.ms, tz).prompts++
      span(e.ms)
    }
    // Effective from the integer totals: in + cacheW + out + 0.1 x cacheR.
    const eff = (u) => (u.effective = u.in + u.cacheW + u.out + u.cacheR / 10)
    eff(st.usage)
    for (const m of Object.values(st.byMonth)) eff(m.usage)
    if (first !== Infinity) {
      st.firstTs = new Date(first).toISOString()
      st.lastTs = new Date(last).toISOString()
    }
    return st
  }
}

// ---------- Claude Code ----------

const CLAUDE_SLASH = /^\/[A-Za-z0-9:_-]+(\s|$)/
const PASTE = /\[Pasted text #(\d+)(?: \+\d+ lines)?\]/g

// The text a person typed on a transcript user line, or null when the line is
// not a prompt (tool results, injected wrappers, interruptions, notifications).
function claudePromptText(o) {
  const c = o.message?.content
  let text
  let image = false
  if (typeof c === 'string') text = c
  else if (Array.isArray(c)) {
    const parts = []
    for (const b of c) {
      const type = b?.type
      if (type === 'tool_result') return null
      if (type === 'text') parts.push(str(b.text))
      else if (type === 'image') image = true
    }
    text = parts.join('\n')
  } else return null
  const t = text.trim()
  if (!t) return image ? '[image]' : null
  if (t.startsWith('[Request interrupted by user')) return null
  const kind = o.origin && typeof o.origin === 'object' ? str(o.origin.kind) : ''
  if (kind && kind !== 'human') return null
  if (t.startsWith('<') && o.promptSource !== 'typed') return null
  return text
}

async function claude(home, now, s = new Store('anthropic', 'claude-code')) {
  const dir = path.join(home, '.claude')
  const transcripts = [...walk(path.join(dir, 'projects'), (n) => n.endsWith('.jsonl'))]
  const sessionsWithTranscript = new Set(transcripts.map((f) => path.basename(f, '.jsonl')))
  const hist = path.join(dir, 'history.jsonl')
  s.files += transcripts.length + (exists(hist) ? 1 : 0)

  const wanted = (b) => b.includes('"assistant"') || b.includes('"user"') || b.includes('"attachment"')
  for (const f of transcripts) {
    for await (const b of lines(f, wanted)) {
      const o = parse(b)
      if (!o) continue
      const ms = isoMs(o.timestamp)
      if (o.type === 'assistant') {
        const m = o.message
        const u = m?.usage
        if (!u || typeof u !== 'object' || ms === null || m.model === '<synthetic>') continue
        const key = str(m.id) || str(o.requestId) || `${str(o.sessionId)}:${str(o.uuid)}`
        s.addUsage(key, ms, {
          in: num(u.input_tokens),
          cacheW: num(u.cache_creation_input_tokens),
          cacheW1h: num(u.cache_creation?.ephemeral_1h_input_tokens),
          cacheR: num(u.cache_read_input_tokens),
          out: num(u.output_tokens),
          reasoning: num(u.output_tokens_details?.thinking_tokens),
          calls: 1,
        })
      } else if (o.type === 'user') {
        if (ms === null || o.isMeta === true || o.isSidechain === true || o.isCompactSummary === true) continue
        if (claudePromptText(o) === null) continue
        s.addActivity(str(o.uuid), ms, true)
        s.addPrompt(str(o.uuid), ms)
      } else if (o.type === 'attachment') {
        // A prompt typed while a turn was running is delivered inside that
        // turn as a queued_command attachment, with no user line of its own.
        const a = o.attachment
        if (ms === null || o.isSidechain === true || !str(o.uuid) || !a || a.type !== 'queued_command') continue
        if (a.isMeta === true || a.commandMode !== 'prompt' || a.origin?.kind !== 'human') continue
        const p = a.prompt
        let text = ''
        let image = false
        if (typeof p === 'string') text = p
        else if (Array.isArray(p)) {
          if (p.some((b) => b?.type === 'tool_result')) continue
          text = p.filter((b) => b?.type === 'text').map((b) => str(b.text)).join('\n')
          image = p.some((b) => b?.type === 'image')
        } else continue
        if (!text.trim() && !image) continue
        s.addActivity(o.uuid, ms, true)
        s.addPrompt(o.uuid, ms)
      }
    }
  }

  // history.jsonl: only sessions with no transcript anywhere under projects/.
  if (exists(hist)) {
    for await (const b of lines(hist)) {
      const h = parse(b)
      if (!h || !num(h.timestamp)) continue
      const sid = str(h.sessionId)
      if (sid && sessionsWithTranscript.has(sid)) continue
      const ms = h.timestamp
      if (now - ms < PENDING_MS) break
      const text = claudeHistoryText(dir, h)
      const t = text.trim()
      if (!t || t.startsWith('<') || CLAUDE_SLASH.test(t)) continue
      const key = `hist:${sid}:${ms}`
      s.addActivity(key, ms, false)
      s.addPrompt(key, ms)
    }
  }
  return s
}

// Fills "[Pasted text #N]" placeholders from pastedContents or the paste cache,
// because a filled paste can turn an entry into one the filters drop.
function claudeHistoryText(dir, h) {
  const display = str(h.display)
  const pasted = h.pastedContents
  if (!pasted || typeof pasted !== 'object' || Object.keys(pasted).length === 0) return display
  return display.replace(PASTE, (m, n) => {
    const p = pasted[n]
    if (!p || p.type !== 'text') return m
    if (str(p.content)) return p.content
    const hash = str(p.contentHash)
    if (hash && !/[/\\.]/.test(hash)) {
      try {
        return fs.readFileSync(path.join(dir, 'paste-cache', `${hash}.txt`), 'utf8')
      } catch {
        // Fall through: the placeholder stays.
      }
    }
    return m
  })
}

// ---------- Codex ----------

const ROLLOUT = /^rollout-.*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$/

function codexTokens(u) {
  const input = num(u.input_tokens)
  const cached = num(u.cached_input_tokens)
  const write = num(u.cache_write_input_tokens)
  return {
    in: Math.max(0, input - cached - write),
    cacheW: write,
    cacheW1h: 0,
    cacheR: cached,
    out: num(u.output_tokens),
    reasoning: num(u.reasoning_output_tokens),
    calls: 1,
  }
}

// Codex frames an image a person attaches with `<image name=[Image #1]>` and
// `</image>` text blocks. Those frames are not text; a bare input_image with no
// text block at all is a tool result (view_image), not a prompt.
const IMAGE_FRAME = /^<\/?image(\s[^>]*)?>$/

function codexPromptText(p) {
  const parts = []
  let image = false
  let anyText = false
  for (const b of Array.isArray(p.content) ? p.content : []) {
    if (b?.type === 'input_text' || b?.type === 'text') {
      anyText = true
      if (!IMAGE_FRAME.test(str(b.text).trim())) parts.push(str(b.text))
    } else if (b?.type === 'input_image') image = true
  }
  const t = parts.join('\n').trim()
  if (!t) return image && anyText ? '[image]' : null
  if (t.startsWith('<') || t.startsWith('# AGENTS')) return null
  return t
}

// isOS: $CODEX_HOME applies to the OS home only; any other home has its own .codex.
async function codex(home, now, checks, s = new Store('openai', 'codex'), isOS = true) {
  const dir = (isOS && process.env.CODEX_HOME) || path.join(home, '.codex')
  const files = [
    ...walk(path.join(dir, 'sessions'), (n) => ROLLOUT.test(n)),
    ...walk(path.join(dir, 'archived_sessions'), (n) => ROLLOUT.test(n)),
  ]
  const hist = path.join(dir, 'history.jsonl')
  s.files += files.length + (exists(hist) ? 1 : 0)
  const threadsWithRollout = new Set(files.map((f) => ROLLOUT.exec(path.basename(f))[1]))
  const mix = (checks && checks.codexModeRule) || { filesWithRecords: 0, filesWithTokenCount: 0, filesMixed: 0, countedTokenCountAfterRecord: 0, mixedFiles: [] }

  for (const f of files) {
    let rollout = ROLLOUT.exec(path.basename(f))[1]
    let metaSeen = false
    let subagent = false
    let sawRecord = false
    let lastTotal = null
    let epoch = 0
    let lineIndex = -1
    const fm = { records: 0, countedBefore: 0, ignoredAfter: 0, firstRecordMs: null, lastCountedMs: null }
    for await (const b of lines(f)) {
      lineIndex++
      const o = parse(b)
      if (!o) continue
      const p = o.payload && typeof o.payload === 'object' ? o.payload : {}
      const ms = isoMs(o.timestamp)
      if (o.type === 'session_meta') {
        if (metaSeen) continue
        metaSeen = true
        rollout = str(p.id)
        subagent = p.thread_source === 'subagent' || (p.source !== null && typeof p.source === 'object' && 'subagent' in p.source)
      } else if (o.type === 'token_usage_record') {
        sawRecord = true
        fm.records++
        if (fm.firstRecordMs === null) fm.firstRecordMs = ms
        if (ms === null || !p.usage || typeof p.usage !== 'object' || !str(p.response_id)) continue
        s.addUsage(p.response_id, ms, codexTokens(p.usage))
      } else if (o.type === 'event_msg' && p.type === 'token_count') {
        const info = p.info
        if (!info || typeof info !== 'object' || !info.total_token_usage || ms === null) continue
        if (sawRecord) {
          fm.ignoredAfter++
          continue
        }
        const total = num(info.total_token_usage.total_tokens)
        if (lastTotal !== null && total === lastTotal) continue
        if (lastTotal !== null && total < lastTotal) epoch++
        lastTotal = total
        if (!info.last_token_usage || typeof info.last_token_usage !== 'object') continue
        fm.countedBefore++
        fm.lastCountedMs = ms
        s.addUsage(`${rollout}:${epoch}:${total}`, ms, codexTokens(info.last_token_usage))
      } else if (o.type === 'response_item' && p.type === 'message' && p.role === 'user') {
        if (ms === null || subagent || o.metadata?.inherited_user_message === true) continue
        if (codexPromptText(p) === null) continue
        const ord = typeof o.ordinal === 'number' ? o.ordinal : lineIndex
        const key = `${rollout}:${ord}`
        s.addActivity(key, ms, true)
        s.addPrompt(key, ms)
      }
    }
    if (fm.records) mix.filesWithRecords++
    if (fm.countedBefore || fm.ignoredAfter) mix.filesWithTokenCount++
    if (fm.records && fm.countedBefore) {
      mix.filesMixed++
      mix.mixedFiles.push({
        file: path.basename(f).slice(0, 36),
        countedTokenCountBeforeFirstRecord: fm.countedBefore,
        records: fm.records,
        ignoredTokenCountAfterRecord: fm.ignoredAfter,
        lastCountedBeforeFirstRecord: fm.lastCountedMs === null ? null : new Date(fm.lastCountedMs).toISOString(),
        firstRecord: fm.firstRecordMs === null ? null : new Date(fm.firstRecordMs).toISOString(),
      })
    }
  }

  if (exists(hist)) {
    for await (const b of lines(hist)) {
      const h = parse(b)
      if (!h || !num(h.ts)) continue
      const sid = str(h.session_id)
      if (sid && threadsWithRollout.has(sid)) continue
      const ms = h.ts * 1000
      if (now - ms < PENDING_MS) break
      const t = str(h.text).trim()
      if (!t || t.startsWith('<') || t.startsWith('# AGENTS')) continue
      const key = `hist:${sid}:${h.ts}`
      s.addActivity(key, ms, false)
      s.addPrompt(key, ms)
    }
  }
  if (checks) checks.codexModeRule = mix
  return s
}

// ---------- Grok CLI ----------

function grokTokens(u) {
  const input = num(u.inputTokens)
  const cached = num(u.cachedReadTokens)
  const write = num(u.cacheCreationTokens)
  return {
    in: Math.max(0, input - cached - write),
    cacheW: write,
    cacheW1h: 0,
    cacheR: cached,
    out: num(u.outputTokens),
    reasoning: num(u.reasoningTokens),
    calls: num(u.modelCalls),
  }
}

function readJSON(p) {
  try {
    const o = JSON.parse(fs.readFileSync(p, 'utf8'))
    return o && typeof o === 'object' ? o : null
  } catch {
    return null
  }
}

async function grok(home, now, checks, s = new Store('xai', 'grok-cli')) {
  const root = path.join(home, '.grok', 'sessions')
  const sessions = []
  let cwds = []
  try {
    cwds = fs.readdirSync(root, { withFileTypes: true }).filter((d) => d.isDirectory())
  } catch {
    // No Grok sessions on this machine.
  }
  for (const c of cwds) {
    for (const d of fs.readdirSync(path.join(root, c.name), { withFileTypes: true })) {
      if (d.isDirectory()) sessions.push(path.join(root, c.name, d.name))
    }
  }
  const perSession = []

  for (const dir of sessions) {
    const updates = path.join(dir, 'updates.jsonl')
    const usageFile = path.join(dir, 'usage.json')
    const hasUpdates = exists(updates)
    const hasUsage = exists(usageFile)
    s.files += (hasUpdates ? 1 : 0) + (hasUsage ? 1 : 0)
    const summary = readJSON(path.join(dir, 'summary.json')) || {}
    const sid = str(summary.info?.id) || path.basename(dir)
    const delegated = !!(str(summary.session_kind) || str(summary.parent_session_id))
    const fallbackModel = str(summary.current_model_id)

    let turnCompletedWithUsage = 0
    // Raw turn_completed sums for this session, deduplicated by key.
    const seen = new Set()
    const raw = { inputTokens: 0, outputTokens: 0, cachedReadTokens: 0, cacheCreationTokens: 0, reasoningTokens: 0, modelCalls: 0 }
    const turns = []
    if (hasUpdates) {
      let model = ''
      let pending = null
      const flush = () => {
        const p = pending
        pending = null
        if (!p || p.hidden || delegated) return
        const t = p.parts.join('\n').trim()
        if (!t && !p.image) return
        if (t.startsWith('<system-reminder>')) return
        s.addActivity(p.key, p.ms, true)
        s.addPrompt(p.key, p.ms)
      }
      for await (const b of lines(updates)) {
        const o = parse(b)
        const u = o?.params?.update
        const kind = u && typeof u === 'object' ? u.sessionUpdate : undefined
        if (kind !== 'user_message_chunk') flush()
        if (kind === 'user_message_chunk') {
          const eid = str(o.params?._meta?.eventId)
          const lineMs = num(o.timestamp) * 1000
          // One message is one text block and the images after it, so a
          // second text block in a run is the next (mid-turn) message.
          if (pending && pending.parts.length && u.content?.type === 'text' && eid) flush()
          if (!pending) {
            if (!eid) continue
            const agentMs = num(o.params?._meta?.agentTimestampMs)
            pending = { key: eid, ms: agentMs > 0 ? agentMs : lineMs, parts: [], image: false, hidden: false }
          }
          const mid = str(u._meta?.modelId)
          if (mid) model = mid
          if (u._meta?.hideFromScrollback === true) pending.hidden = true
          const c = u.content
          if (c && typeof c === 'object') {
            if (c.type === 'text') pending.parts.push(typeof c._meta?.displayText === 'string' ? c._meta.displayText : str(c.text))
            else if (c.type === 'image') pending.image = true
          }
        } else if (kind === 'turn_completed') {
          const us = u.usage
          const eid = str(o.params?._meta?.eventId)
          if (!us || typeof us !== 'object' || !eid) continue
          turnCompletedWithUsage++
          const ms = num(o.timestamp) * 1000
          const mu = us.modelUsage && typeof us.modelUsage === 'object' ? us.modelUsage : null
          const entries = mu && Object.keys(mu).length ? Object.entries(mu) : [[model || fallbackModel, us]]
          for (const [m, c] of entries) {
            const key = `${eid}:${m}`
            s.addUsage(key, ms, grokTokens(c))
            if (!seen.has(key)) {
              seen.add(key)
              for (const f of Object.keys(raw)) raw[f] += num(c[f])
              turns.push({ ms, c })
            }
          }
        }
      }
      if (pending && now - pending.ms >= PENDING_MS) flush()
    }

    // usage.json only when updates.jsonl has no turn_completed usage, and only
    // once the session has been quiet for ten minutes.
    const uj = hasUsage ? readJSON(usageFile) : null
    if (uj && turnCompletedWithUsage === 0) {
      const quiet = [updates, usageFile].every((p) => {
        try {
          return now - fs.statSync(p).mtimeMs >= PENDING_MS
        } catch {
          return true
        }
      })
      if (quiet) {
        const usid = str(uj.sessionId) || sid
        for (const t of Array.isArray(uj.turns) ? uj.turns : []) {
          const ms = isoMs(t?.endedAt)
          if (ms === null) continue
          s.addUsage(`${usid}:${num(t.turnNumber)}:${t.endedAt}`, ms, grokTokens(t))
        }
      }
    }
    perSession.push({
      id: sid,
      parent: str(summary.parent_session_id),
      kind: str(summary.session_kind),
      turnCompleted: turnCompletedWithUsage,
      raw,
      turns,
      usageJson: uj?.session && typeof uj.session === 'object' ? uj.session : null,
      usageJsonAt: isoMs(uj?.updatedAt),
      lastMs: Math.max(...[updates, usageFile].map((p) => (exists(p) ? fs.statSync(p).mtimeMs : 0))),
    })
  }

  if (checks) checks.grokUsageJson = mergeGrokChecks(checks.grokUsageJson, grokCheck(perSession, now))
  return s
}

// Sums the per-home Grok check counters (one --checks object covers every home).
function mergeGrokChecks(a, b) {
  if (!a) return b
  const out = { ...a }
  for (const k of Object.keys(b)) out[k] = Array.isArray(b[k]) ? [...a[k], ...b[k]] : a[k] + b[k]
  return out
}

// Compares each session's deduplicated turn_completed sums with its usage.json
// session totals. Two known cases are explained: a parent's usage.json can
// include its subagents' usage, and usage.json can stop before the session's
// last turns (a cancelled final turn is often never written to it).
function grokCheck(perSession, now) {
  const F = ['inputTokens', 'outputTokens', 'cachedReadTokens', 'cacheCreationTokens', 'reasoningTokens', 'modelCalls']
  const children = new Map()
  for (const x of perSession) if (x.parent) children.set(x.parent, [...(children.get(x.parent) || []), x])
  const out = { sessions: 0, equal: 0, parentIncludesChild: 0, usageJsonBehind: 0, live: 0, noUsageJson: 0, usageJsonOnly: 0, unexplained: [] }
  for (const x of perSession) {
    out.sessions++
    if (!x.usageJson) {
      out.noUsageJson++
      continue
    }
    if (x.turnCompleted === 0) {
      out.usageJsonOnly++
      continue
    }
    const diff = F.filter((f) => num(x.usageJson[f]) !== x.raw[f])
    if (!diff.length) {
      out.equal++
      continue
    }
    const kids = children.get(x.id) || []
    const withKids = F.every((f) => num(x.usageJson[f]) === x.raw[f] + kids.reduce((a, k) => a + k.raw[f], 0))
    if (kids.length && withKids) {
      out.parentIncludesChild++
      continue
    }
    // Turns that completed after usage.json was last written.
    const later = x.usageJsonAt === null ? [] : x.turns.filter((t) => t.ms > x.usageJsonAt)
    if (later.length && F.every((f) => num(x.usageJson[f]) + later.reduce((a, t) => a + num(t.c[f]), 0) === x.raw[f])) {
      out.usageJsonBehind++
      continue
    }
    if (now - x.lastMs < PENDING_MS) {
      out.live++
      continue
    }
    out.unexplained.push({
      session: x.id.slice(0, 12),
      kind: x.kind || 'main',
      children: kids.length,
      turns: x.turnCompleted,
      delta: Object.fromEntries(diff.map((f) => [f, num(x.usageJson[f]) - x.raw[f]])),
    })
  }
  return out
}

// ---------- Cursor ----------

// Cursor's per-user data directory (globalStorage/state.vscdb lives under it).
function cursorUserDir(home) {
  if (process.platform === 'win32') return path.join(home, 'AppData', 'Roaming', 'Cursor', 'User')
  if (process.platform === 'darwin') return path.join(home, 'Library', 'Application Support', 'Cursor', 'User')
  return path.join(home, '.config', 'Cursor', 'User')
}

// createdAt is epoch ms on composers and an ISO string on bubbles; null if unusable.
function cursorMs(v) {
  if (typeof v === 'number' && Number.isFinite(v) && v > 0) return v < 1e11 ? v * 1000 : v
  if (typeof v === 'string' && v) {
    const ms = Date.parse(v)
    return Number.isFinite(ms) ? ms : null
  }
  return null
}

const cursorJSON = (v) => {
  if (v === null || v === undefined) return null
  try {
    const o = JSON.parse(typeof v === 'string' ? v : Buffer.from(v).toString('utf8'))
    return o && typeof o === 'object' && !Array.isArray(o) ? o : null
  } catch {
    return null
  }
}

// One usage event per assistant bubble whose tokenCount carries tokens
// (nativeKey usageUuid, else composerId:bubbleId; ts the bubble's createdAt,
// else the composer's); one activity + prompt per user bubble with text or an
// image (nativeKey composerId:bubbleId; hasUsage when the composer has any
// token-bearing bubble). Rows are streamed straight from SQLite, read-only.
async function cursor(home, now, s = new Store('cursor', 'cursor'), isOS = true) {
  const file = (isOS && process.env.D0M1_CURSOR_DB) || path.join(cursorUserDir(home), 'globalStorage', 'state.vscdb')
  if (!exists(file)) return s
  let DatabaseSync
  try {
    ;({ DatabaseSync } = await import('node:sqlite'))
  } catch {
    console.error('oracle: node:sqlite unavailable (Node >= 22.13 needed); cursor skipped')
    return s
  }
  s.files += 1
  const db = new DatabaseSync(file, { readOnly: true })
  try {
    db.exec('PRAGMA busy_timeout = 5000')
    const composers = new Map()
    for (const r of db.prepare("SELECT key, value FROM cursorDiskKV WHERE key >= 'composerData:' AND key < 'composerData;'").iterate()) {
      const o = cursorJSON(r.value)
      if (!o) continue
      composers.set(r.key.slice('composerData:'.length), { ms: cursorMs(o.createdAt) })
    }
    // Bubbles grouped by composer (key order), so hasUsage is known per session.
    let cur = null
    let users = []
    let anyTokens = false
    const flush = () => {
      for (const u of users) {
        s.addActivity(u.key, u.ms, anyTokens)
        s.addPrompt(u.key, u.ms)
      }
      users = []
      anyTokens = false
    }
    for (const r of db.prepare("SELECT key, value FROM cursorDiskKV WHERE key >= 'bubbleId:' AND key < 'bubbleId;' ORDER BY key").iterate()) {
      const [, composerId, bubbleId] = r.key.split(':')
      if (!composerId || !bubbleId) continue
      if (composerId !== cur) {
        flush()
        cur = composerId
      }
      const o = cursorJSON(r.value)
      if (!o) continue
      const ms = cursorMs(o.createdAt) ?? composers.get(composerId)?.ms ?? null
      if (o.type === 2) {
        const tc = o.tokenCount
        const inTok = num(tc?.inputTokens)
        const outTok = num(tc?.outputTokens)
        if (!tc || (inTok <= 0 && outTok <= 0)) continue
        anyTokens = true
        if (ms === null) continue
        const key = str(o.usageUuid) || `${composerId}:${bubbleId}`
        s.addUsage(key, ms, { in: inTok, cacheW: 0, cacheW1h: 0, cacheR: 0, out: outTok, reasoning: 0, calls: 1 })
      } else if (o.type === 1) {
        const hasText = str(o.text).trim() !== '' || (Array.isArray(o.images) && o.images.length > 0)
        if (!hasText || ms === null) continue
        users.push({ key: `${composerId}:${bubbleId}`, ms })
      }
    }
    flush()
  } finally {
    db.close()
  }
  return s
}

// ---------- Gemini CLI ----------

// Gemini's input includes the cached part and output excludes thoughts, so
// in = max(0, input - cached) + tool, cacheR = cached, out = output + thoughts,
// reasoning = thoughts, calls = 1 (verified on real sessions: total == input +
// output + thoughts + tool). A tokens object with every field zero is no usage.
function geminiTokens(t) {
  const input = num(t.input)
  const cached = num(t.cached)
  const thoughts = num(t.thoughts)
  return {
    in: Math.max(0, input - cached) + num(t.tool),
    cacheW: 0,
    cacheW1h: 0,
    cacheR: cached,
    out: num(t.output) + thoughts,
    reasoning: thoughts,
    calls: 1,
  }
}

const geminiHasTokens = (t) => !!t && typeof t === 'object' && ['input', 'output', 'cached', 'thoughts', 'tool'].some((k) => num(t[k]) > 0)

// A message's text: a string, or the text parts of a list of parts.
function geminiText(c) {
  if (typeof c === 'string') return c
  if (!Array.isArray(c) || !c.every((p) => p && typeof p === 'object' && !Array.isArray(p))) return ''
  return c.map((p) => str(p.text)).filter(Boolean).join('\n')
}

// One whole-JSON session per ~/.gemini/tmp/<projectHash>/chats/session-*.json:
// a usage event per `gemini` message with tokens (nativeKey sessionId:messageId),
// an activity + prompt per non-blank `user` message (hasUsage when any gemini
// message of the session carries tokens). A document that does not parse (being
// rewritten) counts as a file and yields nothing. The session id falls back to
// the id part of the file name (session-<date>-<id>.json).
async function gemini(home, now, s = new Store('google', 'gemini-cli')) {
  const tmp = path.join(home, '.gemini', 'tmp')
  let projects = []
  try {
    projects = fs.readdirSync(tmp, { withFileTypes: true }).filter((d) => d.isDirectory())
  } catch {
    // No Gemini CLI in this home.
  }
  const files = []
  for (const p of projects) {
    const chats = path.join(tmp, p.name, 'chats')
    let ents
    try {
      ents = fs.readdirSync(chats, { withFileTypes: true })
    } catch {
      continue
    }
    for (const e of ents) if (e.isFile() && /^session-.*\.json$/.test(e.name)) files.push(path.join(chats, e.name))
  }
  s.files += files.length
  for (const f of files) {
    const doc = readJSON(f)
    if (!doc || Array.isArray(doc)) continue
    const msgs = Array.isArray(doc.messages) ? doc.messages : []
    const base = path.basename(f, '.json')
    const sid = str(doc.sessionId) || base.slice(base.lastIndexOf('-') + 1)
    const hasUsage = msgs.some((m) => m && m.type === 'gemini' && geminiHasTokens(m.tokens))
    for (const m of msgs) {
      if (!m || typeof m !== 'object') continue
      const ms = isoMs(m.timestamp)
      const id = str(m.id)
      if (ms === null || !id) continue
      const key = `${sid}:${id}`
      if (m.type === 'gemini') {
        if (geminiHasTokens(m.tokens)) s.addUsage(key, ms, geminiTokens(m.tokens))
      } else if (m.type === 'user') {
        if (!geminiText(m.content).trim()) continue
        s.addActivity(key, ms, hasUsage)
        s.addPrompt(key, ms)
      }
    }
  }
  return s
}

// ---------- compare ----------

function compare(mine, theirs) {
  const fields = ['events', ...TOKEN_FIELDS]
  const rows = []
  let diffs = 0
  const row = (src, month, a, b) => {
    const cells = []
    const pairs = [
      ...fields.map((f) => [f, a?.usage?.[f] ?? 0, b?.usage?.[f] ?? 0]),
      ['actWith', a?.activity?.withUsage ?? 0, b?.activity?.withUsage ?? 0],
      ['actNo', a?.activity?.noUsage ?? 0, b?.activity?.noUsage ?? 0],
      ['prompts', a?.prompts ?? 0, b?.prompts ?? 0],
    ]
    for (const [f, x, y] of pairs) {
      if (x !== y) {
        diffs++
        cells.push(`${f}: oracle ${x} go ${y} (${y - x > 0 ? '+' : ''}${y - x})`)
      }
    }
    rows.push({ src, month, a, cells })
  }
  const names = [...new Set([...Object.keys(mine.sources), ...Object.keys(theirs.sources)])].sort()
  for (const n of names) {
    const a = mine.sources[n]
    const b = theirs.sources[n]
    const months = [...new Set([...Object.keys(a?.byMonth || {}), ...Object.keys(b?.byMonth || {})])].sort()
    for (const m of months) row(n, m, a?.byMonth?.[m], b?.byMonth?.[m])
    row(n, 'total', a, b)
    if ((a?.files ?? 0) !== (b?.files ?? 0)) {
      diffs++
      rows.push({ src: n, month: 'files', a: null, cells: [`files: oracle ${a?.files} go ${b?.files}`] })
    }
  }
  const pad = (s, n) => String(s).padStart(n)
  console.log(`${'source'.padEnd(12)} ${'month'.padEnd(7)} ${pad('events', 8)} ${pad('in', 13)} ${pad('cacheW', 13)} ${pad('cacheW1h', 13)} ${pad('cacheR', 15)} ${pad('out', 12)} ${pad('reasoning', 11)} ${pad('calls', 8)} ${pad('act+', 6)} ${pad('act-', 6)} ${pad('prompts', 7)}  match`)
  for (const r of rows) {
    const u = r.a?.usage
    const line = r.a
      ? `${r.src.padEnd(12)} ${r.month.padEnd(7)} ${pad(u.events, 8)} ${pad(u.in, 13)} ${pad(u.cacheW, 13)} ${pad(u.cacheW1h, 13)} ${pad(u.cacheR, 15)} ${pad(u.out, 12)} ${pad(u.reasoning, 11)} ${pad(u.calls, 8)} ${pad(r.a.activity.withUsage, 6)} ${pad(r.a.activity.noUsage, 6)} ${pad(r.a.prompts, 7)}`
      : `${r.src.padEnd(12)} ${r.month.padEnd(7)} ${'(only in go)'.padStart(8)}`
    console.log(`${line}  ${r.cells.length ? 'DIFF ' + r.cells.join('; ') : 'ok'}`)
  }
  console.log(diffs ? `\n${diffs} differing values` : '\nidentical: every source, month and field matches')
  return diffs
}

// ---------- main ----------

async function main() {
  const args = parseArgs(process.argv.slice(2))
  const since = parseBound(args.since)
  const until = parseBound(args.until)
  const now = Date.now()
  const checks = args.checks ? {} : null
  const homes = homesList(args)
  // One store per source across every home, merged by id the way the server
  // (and the collector's DryRunHomes) do: a file reachable twice counts once.
  const stores = [new Store('anthropic', 'claude-code'), new Store('openai', 'codex'), new Store('xai', 'grok-cli'), new Store('cursor', 'cursor'), new Store('google', 'gemini-cli')]
  const [sClaude, sCodex, sGrok, sCursor, sGemini] = stores
  for (const [i, home] of homes.entries()) {
    const isOS = i === 0
    await claude(home, now, sClaude)
    await codex(home, now, checks, sCodex, isOS)
    await grok(home, now, checks, sGrok)
    await cursor(home, now, sCursor, isOS)
    await gemini(home, now, sGemini)
  }
  const report = {
    generatedAt: new Date(now).toISOString(),
    since: since?.raw ?? null,
    until: until?.raw ?? null,
    sources: Object.fromEntries(stores.map((s) => [s.source, s.totals(since, until)])),
    homes,
  }
  if (checks) report.checks = checks
  if (args.compare) {
    const theirs = JSON.parse(fs.readFileSync(args.compare, 'utf8'))
    process.exitCode = compare(report, theirs) ? 1 : 0
    if (checks) console.log(JSON.stringify({ checks }, null, 2))
    return
  }
  console.log(JSON.stringify(report, null, 2))
}

main().catch((err) => {
  console.error(`oracle: ${err.message}`)
  process.exit(2)
})
