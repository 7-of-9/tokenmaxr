// Ingest validation (SPEC "Wire format"). Each item is validated on its own so
// one malformed event never sinks the rest of the batch.

import { labelled, legacyPromptQ } from './merge.js'

// Cursor is its own provider (SPEC "Providers"): its own subscription and
// model catalogue; the underlying model travels in `model`. Gemini CLI
// (v1.3) reports for provider google.
export const PROVIDERS = ['anthropic', 'openai', 'xai', 'cursor', 'google']

export const SOURCE_PROVIDER = {
  'claude-code': 'anthropic',
  codex: 'openai',
  'grok-cli': 'xai',
  cursor: 'cursor',
  'gemini-cli': 'google',
  'web-claude': 'anthropic',
  'web-chatgpt': 'openai',
  'web-grok': 'xai',
}

// Attribution qualities, best first (ranks in merge.js ACCT_RANK).
export const ACCT_Q = ['recorded', 'session', 'timeline', 'bounded', 'lineage', 'inferred', 'unknown']

export const CAPS = {
  usage: 200,
  activity: 500,
  prompts: 50,
  limits: 50,
  accountUsage: 90,
  promptBytes: 512 * 1024,
}

export const MAX_PROMPT_BYTES = 256 * 1024
export const TRUNCATION_MARKER = '\n[... truncated by d0m1 server]'

const ID_RE = /^[0-9a-f]{32}$/
const ACCT_RE = /^a_[0-9a-f]{16}$/
const SESSION_RE = /^s_[0-9a-f]{16}$/
const WS_RE = /^w_[0-9a-f]{16}$/
const MIN_TS = Date.UTC(2015, 0, 1)
const MAX_FUTURE_MS = 2 * 24 * 3600 * 1000
const MAX_TOKENS = 1e13

export const isId = (v) => typeof v === 'string' && ID_RE.test(v)

// Local calendar date of ts in the machine's zone: (ts + tzOffsetMin).
export function localDate(tsMs, tzOffsetMin) {
  return new Date(tsMs + tzOffsetMin * 60000).toISOString().slice(0, 10)
}

export function utcMonth(tsMs) {
  return new Date(tsMs).toISOString().slice(0, 7)
}

class Invalid extends Error {}

const fail = (msg) => {
  throw new Invalid(msg)
}

function str(x, field, maxLen, { required = false } = {}) {
  const v = x[field]
  if (v == null || v === '') {
    if (required) fail(`${field} required`)
    return ''
  }
  if (typeof v !== 'string') fail(`${field} must be a string`)
  if (v.length > maxLen) fail(`${field} too long`)
  return v
}

function count(x, field, dflt = 0) {
  const v = x[field]
  if (v == null) return dflt
  if (!Number.isSafeInteger(v) || v < 0 || v > MAX_TOKENS) fail(`${field} must be a non-negative integer`)
  return v
}

// Fields shared by usage, activity and prompt records.
function common(x, now) {
  if (!x || typeof x !== 'object' || Array.isArray(x)) fail('not an object')
  if (!isId(x.id)) fail('id must be 32 lowercase hex')
  const source = str(x, 'source', 32, { required: true })
  const provider = str(x, 'provider', 32, { required: true })
  if (SOURCE_PROVIDER[source] === undefined) fail('unknown source')
  if (SOURCE_PROVIDER[source] !== provider) fail('provider does not match source')
  if (typeof x.ts !== 'string') fail('ts must be an RFC 3339 string')
  const tsMs = Date.parse(x.ts)
  if (!Number.isFinite(tsMs)) fail('ts unparseable')
  if (tsMs < MIN_TS || tsMs > now.getTime() + MAX_FUTURE_MS) fail('ts out of range')
  const tz = x.tzOffsetMin ?? 0
  if (!Number.isInteger(tz) || tz < -840 || tz > 840) fail('tzOffsetMin out of range')
  let acct = str(x, 'acct', 32)
  if (acct && !ACCT_RE.test(acct)) fail('acct malformed')
  let acctQ = str(x, 'acctQ', 16) || 'unknown'
  if (!ACCT_Q.includes(acctQ)) fail('acctQ unknown')
  if (!acct) acctQ = 'unknown'
  const session = str(x, 'session', 32)
  if (session && !SESSION_RE.test(session)) fail('session malformed')
  // The workspace, hashed with the fleet key on the collector (never the path).
  const ws = str(x, 'ws', 32)
  if (ws && !WS_RE.test(ws)) fail('ws malformed')
  return {
    id: x.id,
    provider,
    source,
    tsMs,
    ts: new Date(tsMs).toISOString(),
    tzOffsetMin: tz,
    acct,
    acctQ,
    session,
    ws,
  }
}

function wrap(fn) {
  return (x, now) => {
    try {
      return { ok: true, value: fn(x, now) }
    } catch (err) {
      if (err instanceof Invalid) return { ok: false, id: isId(x?.id) ? x.id : null, error: err.message }
      throw err
    }
  }
}

// A usage event whose cacheW1h exceeds cacheW is accepted with cacheW1h
// clamped to cacheW (it is a subset); `clamped` lets ingest report it.
export const validateUsage = wrap((x, now) => {
  const c = common(x, now)
  const q = str(x, 'q', 16) || 'exact'
  if (q !== 'exact' && q !== 'estimated') fail('q must be exact or estimated')
  const pv = x.pv ?? 1
  if (!Number.isSafeInteger(pv) || pv < 0 || pv > 1e6) fail('pv must be a non-negative integer')
  const cacheW = count(x, 'cacheW')
  const cacheW1h = count(x, 'cacheW1h')
  const ev = {
    kind: 'usage',
    ...c,
    model: str(x, 'model', 200),
    q,
    pv,
    in: count(x, 'in'),
    cacheW,
    cacheW1h: Math.min(cacheW1h, cacheW),
    cacheR: count(x, 'cacheR'),
    out: count(x, 'out'),
    reasoning: count(x, 'reasoning'),
    calls: count(x, 'calls', 1),
  }
  if (cacheW1h > cacheW) ev.clamped = 'cacheW1h clamped to cacheW'
  return ev
})

export const validateActivity = wrap((x, now) => {
  const c = common(x, now)
  if (x.hasUsage != null && typeof x.hasUsage !== 'boolean') fail('hasUsage must be boolean')
  return { kind: 'activity', ...c, hasUsage: x.hasUsage === true }
})

// Truncates on a UTF-8 character boundary, never splitting a code point.
export function truncateUtf8(text, maxBytes) {
  const buf = Buffer.from(text, 'utf8')
  if (buf.length <= maxBytes) return { text, truncated: false }
  let end = maxBytes
  while (end > 0 && (buf[end] & 0xc0) === 0x80) end--
  return { text: buf.subarray(0, end).toString('utf8'), truncated: true }
}

export const validatePrompt = wrap((x, now) => {
  // A prompt without acctQ comes from a collector older than v1.4: derive it
  // the way that collector decided (a label only for timeline/session).
  const legacy = x && typeof x === 'object' && (x.acctQ == null || x.acctQ === '')
  const c = common(x, now)
  let acctLabel = str(x, 'acctLabel', 512)
  if (legacy && c.acct) c.acctQ = legacyPromptQ(c.acct, acctLabel)
  // Labels travel only with an attribution good enough to show (rank >= 3).
  if (!labelled(c.acctQ)) acctLabel = ''
  if (typeof x.text !== 'string') fail('text must be a string')
  let text = x.text
  // The collector already caps at 256 KB plus its own marker; allow that slack.
  if (Buffer.byteLength(text, 'utf8') > MAX_PROMPT_BYTES + 1024) {
    text = truncateUtf8(text, MAX_PROMPT_BYTES).text + TRUNCATION_MARKER
  }
  return {
    ...c,
    model: str(x, 'model', 200),
    acctLabel,
    workspace: str(x, 'workspace', 4096),
    machine: str(x, 'machine', 200),
    text,
  }
})

export const MAX_LABEL_RUNES = 48
const CC_RE = /^[A-Z]{2}$/
const IANA_RE = /^[A-Za-z0-9_+\-/]{1,64}$/
// Bidi embedding/override/isolate controls (format characters that can make
// a public label render as something else).
const BIDI_RE = /[\u202A-\u202E\u2066-\u2069]/u

// Public machine label (SPEC "machines"): control and bidi characters
// dropped, whitespace runs collapsed to one space, at most 48 characters.
// Mirrors the collector's CleanLabel.
export function cleanLabel(s) {
  if (typeof s !== 'string') return ''
  const out = []
  let space = false
  for (const ch of s.trim()) {
    if (/\s/u.test(ch)) {
      space = true
      continue
    }
    if (/\p{Cc}/u.test(ch) || BIDI_RE.test(ch) || ch === '\uFFFD') continue
    if (out.length >= MAX_LABEL_RUNES) break
    if (space && out.length > 0) {
      if (out.length + 1 >= MAX_LABEL_RUNES) break
      out.push(' ')
    }
    space = false
    out.push(ch)
  }
  return out.join('')
}

// ISO 3166-1 alpha-2 in upper case, or '' when absent or malformed.
export function cleanCountry(v) {
  if (typeof v !== 'string') return ''
  const cc = v.trim().toUpperCase()
  return CC_RE.test(cc) ? cc : ''
}

// Scan roots (SPEC "Scan roots: multiple homes"): at most 16 paths of at
// most 260 characters (MAX_PATH). They are private, so they live only on the
// machines row and never in a response.
export const MAX_HOMES = 16
export const MAX_HOME_CHARS = 260

// hb.tz is null when the heartbeat carries no tz (an older collector), so the
// machine row keeps what it had; otherwise { iana, cc } with '' for unknown.
// hb.homes likewise: null when absent, else the validated list.
export const validateHeartbeat = wrap((x) => {
  if (!x || typeof x !== 'object' || Array.isArray(x)) fail('heartbeat must be an object')
  const hb = {
    machineLabel: cleanLabel(str(x, 'machineLabel', 1000)),
    os: str(x, 'os', 32),
    arch: str(x, 'arch', 32),
    version: str(x, 'version', 64),
    tz: null,
    homes: null,
  }
  if (x.tz != null) {
    if (typeof x.tz !== 'object' || Array.isArray(x.tz)) fail('tz must be an object')
    const iana = typeof x.tz.iana === 'string' && IANA_RE.test(x.tz.iana) ? x.tz.iana : ''
    // model.TZInfo sends `country`; accept `cc` as an alias.
    hb.tz = { iana, cc: cleanCountry(x.tz.country ?? x.tz.cc) }
  }
  if (x.homes != null) {
    if (!Array.isArray(x.homes)) fail('homes must be an array')
    if (x.homes.length > MAX_HOMES) fail(`homes exceeds ${MAX_HOMES}`)
    for (const h of x.homes) {
      if (typeof h !== 'string') fail('homes must be strings')
      if (h.length > MAX_HOME_CHARS) fail('home path too long')
    }
    hb.homes = [...x.homes]
  }
  const raw = JSON.stringify(x)
  // Table string properties hold 32K UTF-16 chars; keep well under.
  hb.json = raw.length <= 30000 ? raw : JSON.stringify({ machineLabel: hb.machineLabel, os: hb.os, arch: hb.arch, version: hb.version, scanAt: x.scanAt, oversized: true })
  return hb
})

const WINDOW_RE = /^[a-z0-9_-]{1,32}$/
const LABEL_RE = /^[^@\r\n]{1,80}$/

// A plan-window snapshot. Newest observedAt wins on the server; percent is
// not a max. name and detail must not be an email.
export const validateLimit = wrap((x, now) => {
  if (!x || typeof x !== 'object' || Array.isArray(x)) fail('not an object')
  if (!isId(x.id)) fail('id must be 32 lowercase hex')
  const source = str(x, 'source', 32, { required: true })
  const provider = str(x, 'provider', 32, { required: true })
  if (SOURCE_PROVIDER[source] === undefined) fail('unknown source')
  if (SOURCE_PROVIDER[source] !== provider) fail('provider does not match source')
  const window = str(x, 'window', 32, { required: true })
  if (!WINDOW_RE.test(window)) fail('window malformed')
  let acct = str(x, 'acct', 32)
  if (acct && !ACCT_RE.test(acct)) fail('acct malformed')
  let acctQ = str(x, 'acctQ', 16) || 'unknown'
  if (!ACCT_Q.includes(acctQ)) fail('acctQ unknown')
  if (!acct) acctQ = 'unknown'
  const observedMs = Date.parse(x.observedAt)
  if (!Number.isFinite(observedMs)) fail('observedAt unparseable')
  if (observedMs < MIN_TS || observedMs > now.getTime() + MAX_FUTURE_MS) fail('observedAt out of range')
  let usedPercent = null
  if (x.usedPercent != null) {
    if (typeof x.usedPercent !== 'number' || !Number.isFinite(x.usedPercent) || x.usedPercent < 0 || x.usedPercent > 100) {
      fail('usedPercent must be 0..100')
    }
    usedPercent = x.usedPercent
  }
  let resetsAt = null
  if (x.resetsAt != null && x.resetsAt !== '') {
    const ms = Date.parse(x.resetsAt)
    if (!Number.isFinite(ms)) fail('resetsAt unparseable')
    if (ms < MIN_TS || ms > now.getTime() + 400 * 24 * 3600 * 1000) fail('resetsAt out of range')
    resetsAt = new Date(ms).toISOString()
  }
  const label = (field) => {
    const v = str(x, field, 80)
    if (v && !LABEL_RE.test(v)) fail(`${field} malformed`)
    return v
  }
  // The account email. Owner-only on GET /api/limits. name stays email-free.
  const accountEmail = str(x, 'label', 80)
  if (accountEmail && !/^[^\s@]{1,64}@[^\s@]{1,80}$/.test(accountEmail)) fail('label must be an email')
  const status = str(x, 'status', 32)
  if (status && !WINDOW_RE.test(status)) fail('status malformed')
  const out = {
    id: x.id,
    provider,
    source,
    acct,
    acctQ,
    plan: label('plan'),
    window,
    scope: label('scope'),
    name: label('name'),
    label: accountEmail,
    detail: label('detail'),
    resetsAt,
    observedAt: new Date(observedMs).toISOString(),
    status,
  }
  if (usedPercent != null) out.usedPercent = usedPercent
  if (!out.label) delete out.label
  return out
})

// Account-wide daily token snapshots. Only sources with a verified, matching
// total-token definition may use this channel; further providers can be added
// once their account API semantics have been checked.
export const validateAccountUsage = wrap((x, now) => {
  if (!x || typeof x !== 'object' || Array.isArray(x)) fail('not an object')
  if (!isId(x.id)) fail('id must be 32 lowercase hex')
  if (x.source !== 'codex' || x.provider !== 'openai') fail('unsupported account usage source')
  if (!ACCT_RE.test(x.acct || '')) fail('acct required and must be a hash')
  if (x.acctQ !== 'recorded') fail('account usage requires recorded account attribution')
  if (x.timezone !== 'UTC') fail('account usage timezone must be UTC')
  if (typeof x.date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(x.date)) fail('date malformed')
  const dateMs = Date.parse(`${x.date}T00:00:00Z`)
  if (!Number.isFinite(dateMs) || new Date(dateMs).toISOString().slice(0, 10) !== x.date) fail('date invalid')
  if (dateMs < MIN_TS || dateMs > now.getTime() + MAX_FUTURE_MS) fail('date out of range')
  const observedMs = Date.parse(x.observedAt)
  if (!Number.isFinite(observedMs) || observedMs < dateMs || observedMs > now.getTime() + MAX_FUTURE_MS) fail('observedAt out of range')
  if (x.totalTokens == null) fail('totalTokens required')
  return { id: x.id, provider: x.provider, source: x.source, acct: x.acct, acctQ: 'recorded',
    date: x.date, timezone: 'UTC', totalTokens: count(x, 'totalTokens'), observedAt: new Date(observedMs).toISOString() }
})

// Request-level caps. Exceeding them is a 413 so the client halves its batch.
export function checkCaps(body) {
  const usage = body.usage ?? []
  const activity = body.activity ?? []
  const prompts = body.prompts ?? []
  const limits = body.limits ?? []
  const accountUsage = body.accountUsage ?? []
  if (!Array.isArray(usage) || !Array.isArray(activity) || !Array.isArray(prompts) || !Array.isArray(limits) || !Array.isArray(accountUsage)) {
    return { status: 400, error: 'usage, activity, prompts, limits and accountUsage must be arrays' }
  }
  if (usage.length > CAPS.usage) return { status: 413, error: `usage exceeds ${CAPS.usage}` }
  if (activity.length > CAPS.activity) return { status: 413, error: `activity exceeds ${CAPS.activity}` }
  if (prompts.length > CAPS.prompts) return { status: 413, error: `prompts exceeds ${CAPS.prompts}` }
  if (limits.length > CAPS.limits) return { status: 413, error: `limits exceeds ${CAPS.limits}` }
  if (accountUsage.length > CAPS.accountUsage) return { status: 413, error: `accountUsage exceeds ${CAPS.accountUsage}` }
  let bytes = 0
  for (const p of prompts) if (typeof p?.text === 'string') bytes += Buffer.byteLength(p.text, 'utf8')
  if (bytes > CAPS.promptBytes) return { status: 413, error: 'prompt text exceeds 512 KB' }
  return null
}
