/*
 * tokenmaxr Claude Web capture: the page side (content script, world MAIN,
 * document_start, https://claude.ai/*). docs/agents/SPEC.md "Claude chat".
 *
 * It wraps window.fetch without changing any request or response. For a
 * chat completion it reads a clone of the event stream as it arrives and
 * keeps only ids, the model, block types and timings: never the text. A
 * few seconds after the stream ends it reads the conversation the way the
 * app itself does (same origin, the page's own session), estimates the
 * turn's tokens (estimate.js) and hands a record of numbers and ids to the
 * extension (relay.js). It also measures the request's tool definitions
 * (a token count, nothing else). The prompt text is added only while the
 * collector's prompt capture and its chat prompt setting allow it: for live
 * turns (`prompts`) and, separately, for swept history (`sweepPrompts`).
 * The same reader also serves the opt-in history sweep the extension asks
 * for, in personal organisations only.
 *
 * The moment a stream ends it also sends a claim (ids and the client
 * platform, no numbers, no text), which the service worker keeps until the
 * host has it: if the tab closes before the conversation is read back, the
 * turn is still known to come from this browser (SPEC "Claude chat").
 *
 * Nothing is left on the page: no global (a page script could otherwise
 * detect the capture or switch it off) and no test hooks in the shipped
 * file (the unit tests add them, see test/main-world.test.mjs). Page scripts
 * can still forge records through the relay's events; records are estimates
 * and the relay and host bound them.
 *
 * Everything is wrapped in try/catch: a failure here must never break
 * claude.ai.
 */
(function () {
  'use strict'
  var W = window
  var est = W.__tokenmaxrEstimate
  try { delete W.__tokenmaxrEstimate } catch (e) { W.__tokenmaxrEstimate = undefined }
  if (!est || typeof W.fetch !== 'function') return

  var EV_RECORDS = '__tokenmaxr:records'
  var EV_CONFIG = '__tokenmaxr:config'
  var EV_SWEEP = '__tokenmaxr:sweep'
  var EV_SWEEP_DONE = '__tokenmaxr:sweep-done'
  var UUID = '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
  var COMPLETION = new RegExp('/api/organizations/(' + UUID + ')/chat_conversations/(' + UUID + ')/(?:completion|retry_completion)(?:[?#]|$)')
  var ORG = new RegExp('/api/organizations/(' + UUID + ')(?:[/?#]|$)')
  // After the stream ends, the conversation is read after FINISH_DELAYS[i]
  // until the reply appears in it.
  var FINISH_DELAYS = [3000, 5000, 12000]
  var SWEEP_GAP = 1100
  var MAX_PENDING = 200
  // One event to the relay carries at most MAX_EVENT characters of JSON
  // (the relay takes up to 8 MB), and a prompt at most MAX_TEXT characters
  // (the service worker cuts it to 256 KB of UTF-8).
  var MAX_EVENT = 4 << 20
  var MAX_TEXT = 256 * 1024

  var origFetch = W.fetch
  var config = null // {enabled, prompts, sweepPrompts} from the extension
  var pending = [] // records waiting for the first config
  var lastOrg = null

  var state = { captured: 0, emitted: 0, errors: 0 }

  function lower(s) { return typeof s === 'string' ? s.toLowerCase() : s }

  function urlOf(input) {
    if (typeof input === 'string') return input
    if (input && typeof input.url === 'string') return input.url
    try { return String(input) } catch (e) { return '' }
  }

  function headerOf(input, init, name) {
    var sources = [init && init.headers, input && typeof input === 'object' ? input.headers : null]
    for (var i = 0; i < sources.length; i++) {
      var h = sources[i]
      if (!h) continue
      try {
        if (typeof h.get === 'function') { var v = h.get(name); if (v) return v; continue }
        if (Array.isArray(h)) {
          for (var j = 0; j < h.length; j++) if (h[j] && String(h[j][0]).toLowerCase() === name) return String(h[j][1])
          continue
        }
        for (var k in h) if (Object.prototype.hasOwnProperty.call(h, k) && k.toLowerCase() === name) return String(h[k])
      } catch (e) { /* ignore */ }
    }
    return ''
  }

  function platformOf(v) {
    v = String(v || '').toLowerCase()
    return v === 'web_claude_ai' || v === 'desktop_app' ? v : ''
  }

  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms) }) }

  function emit(records) {
    if (!records.length) return
    if (!config) {
      pending = pending.concat(records).slice(-MAX_PENDING)
      return
    }
    if (!config.enabled) return
    records = records.map(function (r) {
      if (!('text' in r) || textAllowed(r.kind)) return r
      var c = Object.assign({}, r); delete c.text; return c
    })
    state.emitted += records.length
    // Split by size, so no event is over the relay's limit.
    var chunk = [], size = 0
    for (var i = 0; i < records.length; i++) {
      var n = JSON.stringify(records[i]).length + 1
      if (chunk.length && size + n > MAX_EVENT) { dispatch(chunk); chunk = []; size = 0 }
      chunk.push(records[i]); size += n
    }
    dispatch(chunk)
  }

  function dispatch(records) {
    try { W.dispatchEvent(new CustomEvent(EV_RECORDS, { detail: JSON.stringify(records) })) } catch (e) { state.errors++ }
  }

  // textAllowed: may a record of this kind carry its prompt text?
  function textAllowed(kind) {
    return !!config && (kind === 'live' ? config.prompts === true : kind === 'sweep' && config.sweepPrompts === true)
  }

  W.addEventListener(EV_CONFIG, function (e) {
    try {
      var c = JSON.parse(e.detail)
      config = { enabled: c.enabled === true, prompts: c.prompts === true, sweepPrompts: c.sweepPrompts === true }
      var p = pending
      pending = []
      emit(p)
    } catch (err) { /* ignore */ }
  })

  // --- SSE: metadata only ---

  // newStreamState holds what a completion stream tells us besides text.
  function newStreamState() {
    return { assistant: '', human: '', model: '', stop: '', blocks: [], thinkingMs: 0, open: {}, done: false }
  }

  // onEvent updates st from one parsed SSE data object. Deltas (the text)
  // are ignored.
  function onEvent(st, d) {
    if (!d || typeof d !== 'object') return
    switch (d.type) {
      case 'message_start':
        var m = d.message || {}
        st.assistant = typeof m.uuid === 'string' ? m.uuid : ''
        st.human = typeof m.parent_uuid === 'string' ? m.parent_uuid : ''
        st.model = typeof m.model === 'string' ? m.model : ''
        break
      case 'content_block_start':
        var b = d.content_block || {}
        st.blocks.push(String(b.type || ''))
        st.open[d.index] = { type: b.type, start: Date.parse(b.start_timestamp || '') }
        break
      case 'content_block_stop':
        var o = st.open[d.index]
        if (o && o.type === 'thinking') {
          var stop = Date.parse(d.stop_timestamp || '')
          if (isFinite(o.start) && isFinite(stop) && stop >= o.start) st.thinkingMs += stop - o.start
        }
        delete st.open[d.index]
        break
      case 'message_delta':
        if (d.delta && typeof d.delta.stop_reason === 'string') st.stop = d.delta.stop_reason
        break
      case 'message_stop':
        st.done = true
        break
    }
  }

  // sseParser feeds text chunks and calls onData for every data payload.
  function sseParser(onData) {
    var buf = ''
    function block(text) {
      var data = []
      var lines = text.split(/\r?\n/)
      for (var i = 0; i < lines.length; i++) {
        var l = lines[i]
        if (l.slice(0, 5) === 'data:') data.push(l.slice(5).replace(/^ /, ''))
      }
      if (!data.length) return
      var d
      try { d = JSON.parse(data.join('\n')) } catch (e) { return }
      onData(d)
    }
    return {
      push: function (chunk) {
        buf += chunk
        for (;;) {
          var m = /\r?\n\r?\n/.exec(buf)
          if (!m) break
          block(buf.slice(0, m.index))
          buf = buf.slice(m.index + m[0].length)
        }
        if (buf.length > 4 << 20) buf = '' // a block this large is a text delta: drop it
      },
      end: function () { if (buf.trim()) block(buf); buf = '' }
    }
  }

  function watch(res, ctx) {
    var st = newStreamState()
    var parser = sseParser(function (d) { onEvent(st, d) })
    var reader = res.body.getReader()
    var dec = new TextDecoder()
    function finish() {
      try { parser.end() } catch (e) { /* ignore */ }
      if (st.assistant) finishTurn(ctx, st)
    }
    function pump() {
      return reader.read().then(function (r) {
        if (r.done) { finish(); return }
        try { parser.push(dec.decode(r.value, { stream: true })) } catch (e) { /* ignore */ }
        return pump()
      }, function () { finish() }) // aborted mid-stream: the reply may still be saved
    }
    pump()
  }

  // --- conversation reads ---

  function getJSON(path) {
    return origFetch.call(W, path, { method: 'GET', credentials: 'same-origin', headers: { accept: 'application/json' } })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json() })
  }

  function treePath(org, conv) {
    return '/api/organizations/' + org + '/chat_conversations/' + conv +
      '?tree=True&rendering_mode=messages&render_all_tools=true&consistency=eventual'
  }

  function recordFor(kind, ctx, tree, turn, extra) {
    var a = turn.assistant, h = turn.human
    var r = {
      v: 1, kind: kind, platform: ctx.platform || '',
      org: lower(ctx.org), conv: lower(ctx.conv), human: lower(h.uuid), assistant: lower(a.uuid),
      ts: a.created_at, promptTs: h.created_at,
      model: (extra && extra.model) || tree.model || '',
      chatPlatform: typeof tree.platform === 'string' ? tree.platform : '',
      stop: (extra && extra.stop) || (typeof a.stop_reason === 'string' ? a.stop_reason : ''),
      calls: turn.calls,
      est: { in: turn.in, out: turn.out, ctx: est.contextTokens(extra && extra.tools), algo: turn.algo }
    }
    if (extra && extra.thinkingMs) r.thinkingMs = Math.round(extra.thinkingMs)
    if (textAllowed(kind)) {
      var t = est.humanText(h)
      r.text = typeof t === 'string' && t.length > MAX_TEXT ? t.slice(0, MAX_TEXT) : t
    }
    if (!/^[a-z_]{0,32}$/.test(r.stop)) r.stop = ''
    if (!/^[A-Z_]{0,32}$/.test(r.chatPlatform)) r.chatPlatform = ''
    return r
  }

  // claimFor is the turn's claim, sent as soon as its stream ends: the live
  // record needs the conversation read back, which a closing tab can cut.
  function claimFor(ctx, st) {
    return {
      v: 1, kind: 'claim', platform: ctx.platform || '', org: lower(ctx.org), conv: lower(ctx.conv),
      human: lower(st.human || ''), assistant: lower(st.assistant), ts: new Date().toISOString()
    }
  }

  function finishTurn(ctx, st) {
    state.captured++
    emit([claimFor(ctx, st)])
    var i = 0
    function attempt() {
      return Promise.all([getJSON(treePath(ctx.org, ctx.conv)), ctx.tools]).then(function (got) {
        var tree = got[0]
        var turn = est.estimateTurn(tree, st.assistant)
        if (!turn) throw new Error('reply not in the conversation yet')
        emit([recordFor('live', ctx, tree, turn, { model: st.model, stop: st.stop, thinkingMs: st.thinkingMs, tools: got[1] })])
      }).catch(function () {
        i++
        if (i < FINISH_DELAYS.length) return sleep(FINISH_DELAYS[i]).then(attempt)
        state.errors++
      })
    }
    sleep(FINISH_DELAYS[0]).then(attempt)
  }

  // --- the request's tool definitions (a token count only) ---

  // bodyBytes copies a request body the page passed, before fetch reads it:
  // a string, Blob, ArrayBuffer, typed array, or a Request's own body. A
  // stream or form data cannot be read without consuming it: null.
  function bodyBytes(input, init) {
    var b = init && 'body' in init ? init.body : undefined
    if (b === undefined && input && typeof input === 'object' && typeof input.clone === 'function') {
      return input.clone().arrayBuffer()
    }
    if (typeof b === 'string') return Promise.resolve(new TextEncoder().encode(b).buffer)
    if (typeof Blob === 'function' && b instanceof Blob) return b.arrayBuffer()
    if (b instanceof ArrayBuffer) return Promise.resolve(b.slice(0))
    if (b && ArrayBuffer.isView(b)) return Promise.resolve(b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength))
    return null
  }

  // requestTools resolves to the token estimate of the request's tool
  // definitions, or 0 when the body cannot be read. A gzip body (the app
  // compresses it) is inflated in the page.
  function requestTools(input, init) {
    var p = null
    try { p = bodyBytes(input, init) } catch (e) { p = null }
    if (!p) return Promise.resolve(0)
    return p.then(function (buf) {
      var u8 = new Uint8Array(buf)
      if (u8.length > 64 << 20) return null
      if (u8.length > 1 && u8[0] === 0x1f && u8[1] === 0x8b) {
        if (typeof DecompressionStream !== 'function') return null
        return new Response(new Blob([u8]).stream().pipeThrough(new DecompressionStream('gzip'))).text()
      }
      return new TextDecoder().decode(u8)
    }).then(function (text) {
      if (typeof text !== 'string') return 0
      var j = JSON.parse(text)
      return j && Array.isArray(j.tools) ? est.toolTokens(j.tools) : 0
    }).catch(function () { return 0 })
  }

  // --- the fetch wrapper ---

  W.fetch = function (input, init) {
    var tools = null
    try {
      // Before fetch takes the body: a Request's body can be read once.
      if (COMPLETION.test(urlOf(input))) tools = requestTools(input, init)
    } catch (e) { tools = null }
    var p = origFetch.apply(this, arguments)
    try {
      var url = urlOf(input)
      var om = ORG.exec(url)
      if (om) lastOrg = om[1].toLowerCase()
      var m = COMPLETION.exec(url)
      if (m) {
        var ctx = { org: m[1], conv: m[2], platform: platformOf(headerOf(input, init, 'anthropic-client-platform')), tools: tools || Promise.resolve(0) }
        p.then(function (res) {
          try { if (res && res.ok && res.body) watch(res.clone(), ctx) } catch (e) { state.errors++ }
        }, function () { /* the app handles its own errors */ })
      }
    } catch (e) { state.errors++ }
    return p
  }

  // --- history sweep (opt-in; the extension decides when) ---

  function listPage(org, offset, archived) {
    return getJSON('/api/organizations/' + org + '/chat_conversations_v2?limit=50&offset=' + offset +
      '&archived=' + archived + '&consistency=eventual')
  }

  // personalOrg resolves true when the organisation is a personal one (no
  // Team or Enterprise plan): the history sweep reads nothing else. Any
  // doubt (an error, an unexpected shape) is false.
  function personalOrg(org) {
    return getJSON('/api/organizations/' + org).then(function (o) {
      if (!o || typeof o !== 'object' || !('raven_type' in o) || o.raven_type != null) return false
      var caps = Array.isArray(o.capabilities) ? o.capabilities : []
      for (var i = 0; i < caps.length; i++) {
        if (/raven|enterprise|team/i.test(String(caps[i]))) return false
      }
      return true
    }, function () { return false })
  }

  function sweep(opts) {
    var org = lastOrg
    var cursor = typeof opts.cursor === 'string' ? opts.cursor : ''
    var settleMs = Math.max(0, Number(opts.settleMinutes || 0)) * 60000
    var budget = Math.max(1, Math.min(Number(opts.budget || 300), 2000))
    var only = typeof opts.only === 'string' ? opts.only.toLowerCase() : ''
    var done = function (out) {
      try { W.dispatchEvent(new CustomEvent(EV_SWEEP_DONE, { detail: JSON.stringify(out) })) } catch (e) { /* ignore */ }
    }
    if (!org) return Promise.resolve(done({ ok: false, error: 'no organization seen yet', cursor: cursor }))
    return personalOrg(org).then(function (ok) {
      if (!ok) return done({ ok: false, error: 'not a personal organization', cursor: cursor })
      return sweepOrg(org, cursor, settleMs, budget, only, done)
    })
  }

  function sweepOrg(org, cursor, settleMs, budget, only, done) {
    var cutoff = new Date(Date.now() - settleMs).toISOString()
    var items = []
    var pages = 0
    function pass(archived, offset) {
      if (pages++ > 400) return Promise.resolve()
      return listPage(org, offset, archived).then(function (r) {
        var data = (r && Array.isArray(r.data)) ? r.data : Array.isArray(r) ? r : []
        var newer = 0
        for (var i = 0; i < data.length; i++) {
          var c = data[i]
          if (!c || typeof c.uuid !== 'string' || typeof c.updated_at !== 'string') continue
          if (!cursor || c.updated_at > cursor) { items.push(c); newer++ }
        }
        // Newest first: a page with nothing newer than the cursor ends the pass.
        if (!data.length || newer === 0 || !(r && r.has_more)) return
        return sleep(SWEEP_GAP).then(function () { return pass(archived, offset + data.length) })
      })
    }
    return pass(false, 0).then(function () { return pass(true, 0) }).then(function () {
      if (only) items = items.filter(function (c) { return c.uuid.toLowerCase() === only })
      items.sort(function (a, b) { return a.updated_at < b.updated_at ? -1 : a.updated_at > b.updated_at ? 1 : 0 })
      var seenConv = {}
      items = items.filter(function (c) { if (seenConv[c.uuid]) return false; seenConv[c.uuid] = 1; return true })
      var next = cursor, n = 0, sent = 0
      function one(k) {
        if (k >= items.length || n >= budget) return Promise.resolve(k < items.length)
        var c = items[k]
        n++
        return sleep(SWEEP_GAP).then(function () { return getJSON(treePath(org, c.uuid)) }).then(function (tree) {
          var by = est.index(tree)
          var batch = []
          var msgs = Array.isArray(tree.chat_messages) ? tree.chat_messages : []
          for (var i = 0; i < msgs.length; i++) {
            var a = msgs[i]
            if (!a || a.sender !== 'assistant' || typeof a.created_at !== 'string' || a.created_at >= cutoff) continue
            var turn = est.estimateTurn(tree, a.uuid, by)
            if (!turn) continue
            batch.push(recordFor('sweep', { org: org, conv: c.uuid, platform: '' }, tree, turn, { model: tree.model || c.model }))
            if (batch.length >= 100) { emit(batch); sent += batch.length; batch = [] }
          }
          emit(batch)
          sent += batch.length
          var mark = c.updated_at < cutoff ? c.updated_at : cutoff
          if (mark > next) next = mark
        }, function () { /* skip this conversation; it is retried when it changes */ })
          .then(function () { return one(k + 1) })
      }
      return one(0).then(function (more) { done({ ok: true, cursor: next, more: more, conversations: n, records: sent }) })
    }).catch(function (e) { done({ ok: false, error: String(e && e.message || e).slice(0, 80), cursor: cursor }) })
  }

  W.addEventListener(EV_SWEEP, function (e) {
    try { sweep(JSON.parse(e.detail) || {}) } catch (err) { /* ignore */ }
  })

  /* test-hooks */
})()
