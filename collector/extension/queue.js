/*
 * Pure helpers of the service worker (sw.js), kept apart for the unit tests.
 */

export const MAX_QUEUE = 5000
// SWEEP_ROOM: swept records are queued only while the queue is shorter than
// this, so live records always have room and nothing queued is ever dropped.
export const SWEEP_ROOM = 2000
export const MAX_SENT = 20000
export const SWEEP_EVERY = 30 * 60 * 1000
export const SWEEP_AGAIN = 60 * 1000
export const SWEEP_LEASE = 15 * 60 * 1000
export const SWEEP_BUDGET = 300
// Prompt text waits in the queue only briefly: TEXT_TTL after it was queued
// (the host was unreachable meanwhile) the record loses its text and keeps
// its numbers, and the queue holds at most MAX_QUEUE_TEXT bytes of text
// (oldest text goes first). One prompt is cut to MAX_TEXT_BYTES, the
// collector's own prompt limit.
export const TEXT_TTL = 15 * 60 * 1000
export const MAX_QUEUE_TEXT = 2 << 20
export const MAX_TEXT_BYTES = 256 * 1024
// MAX_BATCH_BYTES caps one message to the host (which reads up to 8 MB;
// Chrome allows 64 MB). With text capped and the queue's text bounded, the
// queue (MAX_QUEUE records of under 1 KB without text, plus MAX_QUEUE_TEXT)
// and the sent list (MAX_SENT ids) stay well inside chrome.storage.local's
// 10 MB, so the extension needs no unlimitedStorage permission.
export const MAX_BATCH_BYTES = 4 << 20

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const int = (v) => (Number.isSafeInteger(v) && v >= 0 ? v : null)
const str = (v, re, max = 80) => (typeof v === 'string' && v.length <= max && (!re || re.test(v)) ? v : null)

// recordKey identifies a queued record: its kind and turn.
export const recordKey = (r) => r.kind + ':' + r.assistant

const enc = new TextEncoder()
export const byteLength = (s) => enc.encode(s).length

// cutText shortens text to at most max UTF-8 bytes.
export function cutText(text, max = MAX_TEXT_BYTES) {
  if (text.length * 3 <= max) return text
  const b = enc.encode(text)
  if (b.length <= max) return text
  // Drop a partial character at the end (no replacement character).
  return new TextDecoder().decode(b.subarray(0, max)).replace(/\uFFFD$/, '')
}

// sanitize keeps only the record schema's fields (the host validates them
// again, strictly), drops the prompt text unless the collector allows it for
// the record's kind (prompts: live turns; sweepPrompts: swept history), cuts
// it to MAX_TEXT_BYTES and stamps the extension version. Malformed records
// are dropped. A claim (the turn's stream just ended) keeps ids, the
// platform and a time only.
export function sanitize(records, { prompts, sweepPrompts, ext }) {
  const out = []
  for (const r of Array.isArray(records) ? records : []) {
    if (!r || typeof r !== 'object') continue
    if (r.kind !== 'live' && r.kind !== 'sweep' && r.kind !== 'claim') continue
    const ids = ['org', 'conv', 'human', 'assistant'].map((k) => str(r[k], UUID, 36))
    if (r.kind === 'claim' && ids[2] === null && r.human === '') ids[2] = ''
    if (ids.some((v) => v === null)) continue
    if (r.kind === 'claim') {
      const ts = str(r.ts, null, 40)
      if (!ts) continue
      out.push({
        v: 1, kind: 'claim', platform: str(r.platform, /^(web_claude_ai|desktop_app)?$/, 16) ?? '',
        org: ids[0], conv: ids[1], human: ids[2], assistant: ids[3], ts, calls: 1,
        est: { in: 0, out: 0, algo: 'chars-v1' }, ext: str(ext, /^[0-9.]*$/, 16) ?? '',
      })
      continue
    }
    const est = r.est && typeof r.est === 'object' ? r.est : {}
    const rec = {
      v: 1, kind: r.kind, platform: str(r.platform, /^(web_claude_ai|desktop_app)?$/, 16) ?? '',
      org: ids[0], conv: ids[1], human: ids[2], assistant: ids[3],
      ts: str(r.ts, null, 40), promptTs: str(r.promptTs, null, 40) ?? '',
      model: str(r.model, /^[A-Za-z0-9._:\-[\]]*$/) ?? '',
      chatPlatform: str(r.chatPlatform, /^[A-Z_]*$/, 32) ?? '',
      stop: str(r.stop, /^[a-z_]*$/, 32) ?? '',
      calls: int(r.calls) || 1,
      est: { in: int(est.in) ?? 0, out: int(est.out) ?? 0, algo: str(est.algo, /^[a-z0-9.-]{1,16}$/, 16) ?? 'chars-v1' },
      ext: str(ext, /^[0-9.]*$/, 16) ?? '',
    }
    if (!rec.ts) continue
    const ctx = int(est.ctx)
    if (ctx) rec.est.ctx = ctx
    const thinking = int(r.thinkingMs)
    if (thinking) rec.thinkingMs = thinking
    const textOK = r.kind === 'live' ? !!prompts : !!sweepPrompts
    if (textOK && typeof r.text === 'string' && r.text) rec.text = cutText(r.text)
    out.push(rec)
  }
  return out
}

// mergeQueue appends records to the queue and never drops a queued one. A
// swept turn already delivered (sent) is skipped, and so is a record of the
// same kind and turn already queued. Swept records are refused while the
// queue has less than its sweep room left, live ones when it is full: the
// sweep then stops and resumes from its cursor (see sw.js), and the page has
// no other copy of a refused live record, which is the only loss, and only
// after MAX_QUEUE records could not be delivered.
export function mergeQueue(queue, records, sent, now = Date.now()) {
  const q = Array.isArray(queue) ? queue.slice() : []
  const done = new Set(Array.isArray(sent) ? sent : [])
  const queued = new Set(q.map(recordKey))
  let added = 0
  let refused = 0
  for (const r of records) {
    const key = recordKey(r)
    if (queued.has(key) || (r.kind === 'sweep' && done.has(r.assistant))) continue
    if (q.length >= (r.kind === 'sweep' ? SWEEP_ROOM : MAX_QUEUE)) {
      refused++
      continue
    }
    queued.add(key)
    q.push(r.text ? { ...r, queuedAt: now } : r)
    added++
  }
  return { queue: q, added, refused }
}

// settleQueue removes the records the host answered for (by key, wherever
// they now are in the queue) and adds the swept ones to the sent list.
export function settleQueue(queue, batch, sent) {
  const answered = new Set(batch.map(recordKey))
  const rest = (Array.isArray(queue) ? queue : []).filter((r) => !answered.has(recordKey(r)))
  const done = Array.isArray(sent) ? sent.slice() : []
  const have = new Set(done)
  for (const r of batch) {
    if (r.kind === 'sweep' && !have.has(r.assistant)) {
      have.add(r.assistant)
      done.push(r.assistant)
    }
  }
  return { queue: rest, sent: done.slice(-MAX_SENT) }
}

// expireText removes prompt text queued longer than TEXT_TTL (or with no
// queue time), and the oldest text past MAX_QUEUE_TEXT bytes. The records
// stay. changed is false when nothing was removed.
export function expireText(queue, now) {
  const q = Array.isArray(queue) ? queue.slice() : []
  let changed = false
  let bytes = 0
  for (let i = q.length - 1; i >= 0; i--) {
    const r = q[i]
    if (!r || typeof r.text !== 'string') continue
    bytes += byteLength(r.text)
    if (!(now - r.queuedAt < TEXT_TTL) || bytes > MAX_QUEUE_TEXT) {
      q[i] = withoutText([r])[0]
      changed = true
    }
  }
  return { queue: q, changed }
}

// withoutText is the queue with no prompt text at all (storage is full).
export const withoutText = (queue) => queue.map(({ text, queuedAt, ...r }) => r)

// pickBatch is the next message's records: at most n, at most maxBytes of
// JSON (always one), without the queue's own fields.
export function pickBatch(queue, n, maxBytes = MAX_BATCH_BYTES) {
  const out = []
  let bytes = 0
  for (const r of Array.isArray(queue) ? queue : []) {
    if (out.length >= n) break
    const { queuedAt, ...rec } = r
    const size = byteLength(JSON.stringify(rec)) + 1
    if (out.length && bytes + size > maxBytes) break
    bytes += size
    out.push(rec)
  }
  return out
}

// shrinkRecord handles a single record the host found too large: its text
// goes, or, without text, the record itself.
export function shrinkRecord(queue, rec) {
  const key = recordKey(rec)
  const out = []
  for (const r of Array.isArray(queue) ? queue : []) {
    if (recordKey(r) !== key) out.push(r)
    else if (typeof r.text === 'string') out.push(withoutText([r])[0])
  }
  return out
}

// decideSweep: a sweep is due when the collector enabled this machine's
// sweeper, no other tab holds the lease, the last run is old enough (a run
// that stopped at its budget asks to continue a minute later) and the queue
// has room for what it would send.
export function decideSweep(state, cfg, now, only, queued = 0) {
  const s = state && typeof state === 'object' ? state : {}
  const no = { due: false, state: s, reply: { due: false } }
  if (!cfg || !cfg.sweeper) return no
  if (queued >= SWEEP_ROOM / 2) return no
  if (s.lease && now < s.lease) return no
  const every = s.more ? SWEEP_AGAIN : SWEEP_EVERY
  if (s.lastAt && now - s.lastAt < every) return no
  return {
    due: true,
    state: { ...s, lease: now + SWEEP_LEASE, refused: false },
    reply: {
      due: true, cursor: typeof s.cursor === 'string' ? s.cursor : '', settleMinutes: Number(cfg.settleMinutes) || 0,
      budget: SWEEP_BUDGET, only: typeof only === 'string' ? only : '',
    },
  }
}

// validCursor: a sweep cursor is a conversation's updated_at, an ISO time
// no later than an hour from now. A forged "9999..." would stop the sweep.
export function validCursor(c, now) {
  if (typeof c !== 'string' || c.length > 40 || !/^\d{4}-\d\d-\d\dT/.test(c)) return false
  const t = Date.parse(c)
  return Number.isFinite(t) && t <= now + 60 * 60 * 1000
}

// finishSweep records a run's result. The cursor only moves forward, and not
// at all when records of the run were refused (the queue was full): the
// next run reads those conversations again, and the sent list skips what
// was already delivered.
export function finishSweep(state, result, now) {
  const s = state && typeof state === 'object' ? state : {}
  const r = result && typeof result === 'object' ? result : {}
  const ok = r.ok === true
  let cursor = s.cursor || ''
  if (ok && !s.refused && validCursor(r.cursor, now) && r.cursor > cursor) cursor = r.cursor
  return {
    ...s, cursor, lease: 0, lastAt: now, refused: false,
    more: ok && (r.more === true || s.refused === true),
    lastResult: { ok, conversations: Number(r.conversations) || 0, records: Number(r.records) || 0, refused: !!s.refused },
  }
}
