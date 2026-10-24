/*
 * tokenmaxr Claude chat token estimator, algorithm "chars-v1"
 * (docs/agents/SPEC.md "Claude chat estimates").
 *
 * claude.ai reports no token counts for chat, so a turn's tokens are
 * estimated from the conversation tree: a LOWER BOUND. It cannot see the
 * server's system prompt, memory, project knowledge or hidden thinking.
 *
 * The conversation (`in`, `out`) is kept apart from the fixed context every
 * model call re-reads (`ctx`, per call): the tool definitions the page sends
 * with each completion request (measured from the request body, live turns
 * only) plus SYSTEM_FLOOR for the server's system prompt, which the page
 * never sees. The collector reports ctx x calls as estimated cache reads.
 *
 * Pure functions, no I/O. Loaded as a classic script in the page (it hands
 * itself to main-world.js through a property that script deletes) and with
 * require() in the unit tests.
 */
(function (root) {
  'use strict'

  var ALGO = 'chars-v1'
  // ASCII characters per output token, fitted by
  // `go run ./cmd/claude-probe --calibrate` over local Claude Code
  // transcripts (text-only replies, exact output_tokens). See SPEC.
  var RHO = 2.97
  // CJK characters cost about a token each, other non-ASCII about half.
  var CJK_TOKENS = 1
  var OTHER_TOKENS = 0.5
  // Role and turn framing per message.
  var MESSAGE_OVERHEAD = 4
  // An image whose size is unknown (Claude scales images to about 1.15 MP).
  var IMAGE_TOKENS = 1600
  // A deliberately low floor for the server's system prompt, which the page
  // never sees (Anthropic's published claude.ai system prompts are several
  // thousand tokens before any tool instructions).
  var SYSTEM_FLOOR = 2000
  var ROOT_UUID = '00000000-0000-4000-8000-000000000000'

  function isCJK(cp) {
    return (cp >= 0x1100 && cp <= 0x11ff) || (cp >= 0x2e80 && cp <= 0x2fdf) ||
      (cp >= 0x3040 && cp <= 0x30ff) || (cp >= 0x3130 && cp <= 0x318f) ||
      (cp >= 0x3400 && cp <= 0x4dbf) || (cp >= 0x4e00 && cp <= 0x9fff) ||
      (cp >= 0xac00 && cp <= 0xd7af) || (cp >= 0xf900 && cp <= 0xfaff) ||
      (cp >= 0x20000 && cp <= 0x2fa1f)
  }

  // tokens estimates the tokens of a string (a float; callers round sums).
  function tokens(s) {
    if (typeof s !== 'string' || s === '') return 0
    var ascii = 0, cjk = 0, other = 0
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i)
      if (c < 128) { ascii++; continue }
      if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
        c = ((c - 0xd800) << 10) + (s.charCodeAt(i + 1) - 0xdc00) + 0x10000
        i++
      }
      if (isCJK(c)) cjk++
      else other++
    }
    return ascii / RHO + cjk * CJK_TOKENS + other * OTHER_TOKENS
  }

  // Keys that are rendering or bookkeeping, not text the model reads.
  var SKIP = {
    type: 1, uuid: 1, id: 1, tool_use_id: 1, start_timestamp: 1, stop_timestamp: 1,
    created_at: 1, updated_at: 1, citations: 1, flags: 1, signature: 1, cut_off: 1,
    truncated: 1, summaries: 1, is_error: 1, display_content: 1, icon_url: 1,
    integration_icon_url: 1, integration_name: 1, approval_key: 1, approval_options: 1,
    file_uuid: 1, preview_url: 1, thumbnail_url: 1, thumbnail_asset: 1, preview_asset: 1,
    favicon_url: 1, context: 1
  }

  // stringTokens sums the tokens of every string value in v, skipping
  // bookkeeping keys (tool results arrive as nested objects).
  function stringTokens(v, depth) {
    depth = depth || 0
    if (depth > 12 || v == null) return 0
    if (typeof v === 'string') return tokens(v)
    if (typeof v === 'number' || typeof v === 'boolean') return tokens(String(v))
    var n = 0
    if (Array.isArray(v)) {
      for (var i = 0; i < v.length; i++) n += stringTokens(v[i], depth + 1)
      return n
    }
    if (typeof v === 'object') {
      for (var k in v) {
        if (Object.prototype.hasOwnProperty.call(v, k) && !SKIP[k]) n += stringTokens(v[k], depth + 1)
      }
    }
    return n
  }

  function isToolResult(b) { return !!b && (b.type === 'tool_result' || b.type === 'web_search_tool_result') }
  function isOutput(b) { return !!b && (b.type === 'text' || b.type === 'tool_use' || b.type === 'server_tool_use') }

  function blockTokens(b) {
    if (!b || typeof b !== 'object') return 0
    switch (b.type) {
      case 'text': return tokens(b.text)
      case 'thinking': case 'redacted_thinking': return 0 // hidden: never sent to the client
      case 'tool_use': case 'server_tool_use':
        return tokens(b.name) + tokens(JSON.stringify(b.input == null ? {} : b.input))
      case 'tool_result': case 'web_search_tool_result':
        return stringTokens(b.content)
      default: return stringTokens(b)
    }
  }

  function imageTokens(f) {
    var a = (f && (f.preview_asset || f.thumbnail_asset)) || {}
    var w = Number((f && (f.image_width || f.width)) || a.image_width || 0)
    var h = Number((f && (f.image_height || f.height)) || a.image_height || 0)
    if (w > 0 && h > 0) return Math.min(w * h / 750, IMAGE_TOKENS)
    return IMAGE_TOKENS
  }

  // attachmentTokens: pasted or uploaded documents (their extracted text)
  // and images. An upload without extracted text in the tree counts 0.
  function attachmentTokens(m) {
    var n = 0, i
    var att = Array.isArray(m.attachments) ? m.attachments : []
    for (i = 0; i < att.length; i++) n += tokens(att[i] && att[i].extracted_content)
    var files = [].concat(Array.isArray(m.files) ? m.files : [], Array.isArray(m.files_v2) ? m.files_v2 : [])
    var seen = {}
    for (i = 0; i < files.length; i++) {
      var f = files[i]
      if (!f) continue
      var key = f.file_uuid || f.uuid || ('#' + i)
      if (seen[key]) continue
      seen[key] = 1
      if (f.file_kind === 'image' || /^image\//.test(f.file_type || f.mime_type || '')) n += imageTokens(f)
    }
    return n
  }

  function contentOf(m) {
    if (Array.isArray(m.content) && m.content.length) return m.content
    if (typeof m.text === 'string' && m.text) return [{ type: 'text', text: m.text }]
    return []
  }

  // messageTokens is one message as the model reads it.
  function messageTokens(m) {
    var n = MESSAGE_OVERHEAD
    var c = contentOf(m)
    for (var i = 0; i < c.length; i++) n += blockTokens(c[i])
    return n + attachmentTokens(m)
  }

  // humanText is the prompt text of a human message (its text blocks).
  function humanText(m) {
    var c = contentOf(m), out = []
    for (var i = 0; i < c.length; i++) {
      if (c[i] && c[i].type === 'text' && typeof c[i].text === 'string') out.push(c[i].text)
    }
    return out.join('\n')
  }

  // index maps message uuids (lower-cased) to messages.
  function index(tree) {
    var by = {}
    var msgs = (tree && Array.isArray(tree.chat_messages)) ? tree.chat_messages : []
    for (var i = 0; i < msgs.length; i++) {
      if (msgs[i] && typeof msgs[i].uuid === 'string') by[msgs[i].uuid.toLowerCase()] = msgs[i]
    }
    return by
  }

  function get(by, uuid) { return typeof uuid === 'string' ? by[uuid.toLowerCase()] : undefined }

  // estimateTurn estimates the assistant message `assistant` of a
  // conversation tree (GET .../chat_conversations/<id>?tree=True):
  //   context = the branch above its prompt H, fresh = H,
  //   calls = 1 + the tool results inside the reply (each starts another
  //     model call over everything so far),
  //   in = the sum over calls of (context + fresh + the reply so far),
  //   out = the reply's text and tool-use inputs.
  // It returns null when the message or its prompt is missing.
  function estimateTurn(tree, assistant, by) {
    by = by || index(tree)
    var a = get(by, assistant)
    if (!a || a.sender !== 'assistant') return null
    var h = get(by, a.parent_message_uuid)
    if (!h || h.sender !== 'human') return null
    var context = 0
    var seen = {}
    var m = get(by, h.parent_message_uuid)
    while (m && m.uuid !== ROOT_UUID && !seen[m.uuid]) {
      seen[m.uuid] = 1
      context += messageTokens(m)
      m = get(by, m.parent_message_uuid)
    }
    var fresh = messageTokens(h)
    var base = context + fresh + MESSAGE_OVERHEAD
    var input = base
    var out = 0, sofar = 0, calls = 1
    var blocks = contentOf(a)
    for (var i = 0; i < blocks.length; i++) {
      var b = blocks[i]
      var t = blockTokens(b)
      sofar += t
      if (isOutput(b)) out += t
      if (isToolResult(b) && i < blocks.length - 1) {
        calls++
        input += base + sofar
      }
    }
    return { in: Math.round(input), out: Math.round(out), calls: calls, algo: ALGO, human: h, assistant: a }
  }

  // toolTokens estimates the tool definitions of a completion request body
  // (its `tools` array): each tool's name, description and input schema, as
  // the model reads them. Only this number leaves the page.
  function toolTokens(tools) {
    if (!Array.isArray(tools)) return 0
    var n = 0
    for (var i = 0; i < tools.length && i < 2000; i++) {
      var t = tools[i]
      if (!t || typeof t !== 'object') continue
      n += MESSAGE_OVERHEAD + tokens(String(t.name || '')) + tokens(String(t.description || ''))
      try { n += tokens(JSON.stringify(t.input_schema == null ? {} : t.input_schema)) } catch (e) { /* ignore */ }
    }
    return Math.round(n)
  }

  // contextTokens is the fixed context of one model call: the system prompt
  // floor plus the request's tool definitions (0 when not measured).
  function contextTokens(tools) {
    var t = Number(tools)
    return SYSTEM_FLOOR + (isFinite(t) && t > 0 ? Math.round(t) : 0)
  }

  var api = {
    ALGO: ALGO, RHO: RHO, IMAGE_TOKENS: IMAGE_TOKENS, MESSAGE_OVERHEAD: MESSAGE_OVERHEAD, ROOT_UUID: ROOT_UUID,
    SYSTEM_FLOOR: SYSTEM_FLOOR,
    tokens: tokens, messageTokens: messageTokens, estimateTurn: estimateTurn, humanText: humanText, index: index,
    toolTokens: toolTokens, contextTokens: contextTokens
  }
  if (typeof module === 'object' && module && module.exports) module.exports = api
  else root.__tokenmaxrEstimate = api
})(typeof globalThis !== 'undefined' ? globalThis : this)
