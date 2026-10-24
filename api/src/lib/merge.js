// Event merge rules (SPEC "Merge rule"). Pure functions over plain objects so
// they are unit-testable; the ingest handler applies them in an ETag-checked
// read-modify-write against the events table.
//
// - usage, same pv: fieldwise max of the token fields, ts = min
// - usage, higher pv replaces; lower pv is ignored
// - acct/acctQ: keep the higher quality (SPEC "Accounts": recorded > session >
//   timeline > bounded > lineage > inferred > unknown), so a re-upload with
//   better evidence upgrades a row in place
// - model and session: keep non-empty
// - machine/cc: the first reporter's, set on insert and never changed. A row
//   written before v1.1 has no machine; its first later sighting fills both.
//   Exception: a record whose source is an upgrade (sourceUpgrade) brings its
//   channel and its reporter.
// Every tie-break is deterministic so arrival order never changes the result.

import { num } from './tables.js'

export const TOKEN_FIELDS = ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'calls']

// Attribution quality rank. Labels (prompt records) are shown only from
// LABEL_MIN_RANK up: recorded, session, timeline and bounded.
export const ACCT_RANK = { recorded: 6, session: 5, timeline: 4, bounded: 3, lineage: 2, inferred: 1, unknown: 0 }
export const LABEL_MIN_RANK = 3

export const acctRank = (q) => ACCT_RANK[q] ?? 0
export const labelled = (q) => acctRank(q) >= LABEL_MIN_RANK

// acctQ of a prompt record from a collector older than v1.4, which sent no
// acctQ on prompts and a label only for timeline or session attributions.
export function legacyPromptQ(acct, acctLabel) {
  if (acctLabel) return 'timeline'
  return acct ? 'inferred' : 'unknown'
}

function pickAcct(a, b) {
  const ra = a.acct ? acctRank(a.acctQ) : -1
  const rb = b.acct ? acctRank(b.acctQ) : -1
  if (ra !== rb) return ra > rb ? a : b
  // Equal quality: the lexicographically smaller hash wins, in any order.
  return (a.acct || '') <= (b.acct || '') ? a : b
}

function pickNonEmpty(a, b) {
  if (!a) return b || ''
  if (!b) return a
  return a <= b ? a : b
}

const minTs = (a, b) => (Date.parse(a) <= Date.parse(b) ? a : b)

// Returns the merged event. `existing` may be null. Both sides use the
// normalised event shape (see validate.js) minus tsMs.
export function mergeEvent(existing, incoming) {
  if (!existing) return { ...incoming }
  if (existing.kind === 'activity' || incoming.kind === 'activity') return mergeActivity(existing, incoming)
  const epv = existing.pv ?? 0
  const ipv = incoming.pv ?? 0
  if (ipv < epv) return mergeMeta({ ...existing }, existing, incoming)
  let out
  if (ipv > epv) {
    out = { ...incoming }
    // A Claude chat turn recorded live (pv 2) replaces a swept inference
    // (pv 1) of the same turn, reporter included (SPEC "Claude chat").
    if (chatRank(incoming.source) && chatRank(existing.source)) return mergeMeta(out, incoming, existing)
  } else {
    out = { ...existing }
    for (const f of TOKEN_FIELDS) out[f] = Math.max(existing[f] ?? 0, incoming[f] ?? 0)
    out.ts = minTs(existing.ts, incoming.ts)
    if (existing.q === 'exact' || incoming.q === 'exact') out.q = 'exact'
    // Same pv, better evidence of the client: the usage moves channel and
    // reporter with its activity and prompt (mergeActivity), in any order.
    if (sourceUpgrade(existing.source, incoming.source)) {
      out.source = incoming.source
      return mergeMeta(out, incoming, existing)
    }
  }
  return mergeMeta(out, existing, incoming)
}

// Account, model and session follow their own rules regardless of pv.
function mergeMeta(out, a, b) {
  const acct = pickAcct(a, b)
  out.acct = acct.acct || ''
  out.acctQ = acct.acct ? acct.acctQ : 'unknown'
  out.session = pickNonEmpty(a.session, b.session)
  out.ws = pickNonEmpty(a.ws, b.ws)
  if ('model' in a || 'model' in b) out.model = pickNonEmpty(a.model, b.model)
  return firstReporter(out, a, b)
}

// `a` is the existing row: its reporter stands. Machine and country travel
// together so a row never mixes one machine with another's country.
function firstReporter(out, a, b) {
  const src = a.machine ? a : b
  out.machine = src.machine || ''
  out.cc = src.machine ? src.cc || '' : ''
  return out
}

// Claude chat channels share event ids (SPEC "Claude chat"). A prompt's
// channel follows the best evidence of its client: seen live in a browser,
// then an app, then not recorded. Swept records arrive hours after live ones,
// so this only matters when that order is broken.
const CHAT_RANK = { 'chat-claude': 1, 'native-claude': 2, 'web-claude': 3 }
const chatRank = (source) => CHAT_RANK[source] ?? 0

// sourceUpgrade reports that a record of source `to` replaces the channel of
// a stored record of source `from` with the same id: a Claude chat turn by
// the evidence of its client, and a ~/.claude line the desktop app wrote,
// first uploaded as claude-code by collectors that did not read its
// entrypoint (SPEC "Claude Native"). Never the other way round.
export function sourceUpgrade(from, to) {
  if (chatRank(from) && chatRank(to) > chatRank(from)) return true
  return from === 'claude-code' && to === 'native-claude'
}

function mergeActivity(a, b) {
  if (sourceUpgrade(a.source, b.source)) [a, b] = [b, a]
  const out = { ...a, kind: 'activity' }
  out.ts = minTs(a.ts, b.ts)
  out.hasUsage = Boolean(a.hasUsage || b.hasUsage)
  const acct = pickAcct(a, b)
  out.acct = acct.acct || ''
  out.acctQ = acct.acct ? acct.acctQ : 'unknown'
  out.session = pickNonEmpty(a.session, b.session)
  out.ws = pickNonEmpty(a.ws, b.ws)
  return firstReporter(out, a, b)
}

// Fields persisted in the events table (besides partition/row keys).
const STORED = {
  usage: ['kind', 'provider', 'source', 'ts', 'tzOffsetMin', 'model', 'acct', 'acctQ', 'q', 'pv', 'session', ...TOKEN_FIELDS, 'machine', 'cc', 'ws'],
  activity: ['kind', 'provider', 'source', 'ts', 'tzOffsetMin', 'acct', 'acctQ', 'session', 'hasUsage', 'machine', 'cc', 'ws'],
}

const NUMERIC = new Set(['tzOffsetMin', 'pv', ...TOKEN_FIELDS])

export function storedFields(ev) {
  const out = {}
  for (const f of STORED[ev.kind]) {
    if (f === 'hasUsage') out[f] = ev[f] === true
    else if (NUMERIC.has(f)) out[f] = num(ev[f])
    else out[f] = ev[f] == null ? '' : String(ev[f])
  }
  return out
}

// Normalises an events-table row back into the event shape.
export function rowToEvent(row) {
  const kind = row.kind === 'activity' ? 'activity' : 'usage'
  return { id: row.rowKey, ...storedFields({ ...row, kind }) }
}

export function sameEvent(a, b) {
  if (a.kind !== b.kind) return false
  const sa = storedFields(a)
  const sb = storedFields(b)
  return STORED[b.kind].every((f) => sa[f] === sb[f])
}
