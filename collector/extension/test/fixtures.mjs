// Synthetic claude.ai shapes, modelled on redacted live captures (Oct 2026):
// a completion event stream and a conversation tree. No real ids or text.

export const ORG = '11111111-2222-4333-8444-555555555555'
export const CONV = '73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee'
export const ROOT = '00000000-0000-4000-8000-000000000000'
export const id = (n) => 'a0a0aaaa-0000-4000-8000-' + String(n).padStart(12, '0')

const ev = (type, data, pad = 3) => `event: ${type}\ndata: ${JSON.stringify({ type, ...data })}${' '.repeat(pad)}\n\n`

// sse builds a completion stream. Text deltas carry the reply text, which
// the capture must never keep.
export function sse({ assistant, human, model = 'claude-fable-5-1', thinking = true, tool = false, first = false, stop = 'end_turn', cut = false } = {}) {
  let s = ''
  for (let i = 0; i < 9; i++) s += ev('ping', {}, i * 2)
  if (first) s += ev('conversation_ready', { created_at: '2026-10-23T07:39:07Z', updated_at: '2026-10-23T07:39:07Z' })
  s += ev('message_start', { message: { id: 'chatcompl_X', type: 'message', role: 'assistant', model, parent_uuid: human, uuid: assistant, content: [], stop_reason: null, trace_id: 't', request_id: 'req_X' }, discarded_parent_message_uuid: null })
  let i = 0
  if (thinking) {
    s += ev('content_block_start', { index: i, content_block: { type: 'thinking', thinking: '', summaries: [], cut_off: false, truncated: false, start_timestamp: '2026-10-23T07:40:25.5094474Z', stop_timestamp: null } })
    s += ev('content_block_stop', { index: i, stop_timestamp: '2026-10-23T07:40:25.765338399Z', summary_pending: false })
    i++
  }
  if (tool) {
    s += ev('content_block_start', { index: i, content_block: { type: 'tool_use', name: 'web_search', input: {}, start_timestamp: '2026-10-23T07:40:26Z' } })
    s += ev('content_block_delta', { index: i, delta: { type: 'input_json_delta', partial_json: '{"query":"SECRET QUERY"}' } })
    s += ev('content_block_stop', { index: i, stop_timestamp: '2026-10-23T07:40:26.5Z' })
    i++
    s += ev('content_block_start', { index: i, content_block: { type: 'tool_result', name: 'web_search', content: [{ type: 'knowledge', title: 'SECRET RESULT', url: 'https://example.com', text: 'SECRET RESULT TEXT' }] } })
    s += ev('content_block_stop', { index: i })
    i++
  }
  s += ev('content_block_start', { index: i, content_block: { type: 'text', text: '', citations: [], start_timestamp: '2026-10-23T07:40:27Z' } })
  if (cut) return s + ev('content_block_delta', { index: i, delta: { type: 'text_delta', text: 'SECRET REPLY partial' } })
  for (const t of ['SECRET', ' REPLY', ' TEXT']) s += ev('content_block_delta', { index: i, delta: { type: 'text_delta', text: t } })
  s += ev('content_block_stop', { index: i, stop_timestamp: '2026-10-23T07:40:28Z' })
  s += ev('message_delta', { delta: { stop_reason: stop, stop_sequence: null, stop_details: null } })
  s += ev('notice_poll_hint', { check_after_seconds: 300, check_window_seconds: 270 })
  s += ev('message_limit', { message_limit: { type: 'within_limit', windows: { '5h': { utilization: 0.24 } } } })
  s += ev('message_stop', {})
  return s
}

const text = (t) => ({ type: 'text', text: t, citations: [], start_timestamp: 'x', stop_timestamp: 'x' })

// tree: two turns, then a branch (an edited second prompt) whose reply used
// web search once.
export function tree() {
  const m = (n, sender, parent, content, extra = {}) => ({
    uuid: id(n), sender, index: n, parent_message_uuid: parent, text: '', content,
    created_at: `2026-10-01T07:3${n}:00.000000Z`, updated_at: `2026-10-01T07:3${n}:00.000000Z`,
    truncated: false, attachments: [], files: [], sync_sources: [], ...extra,
  })
  return {
    uuid: CONV, name: 'Test', model: 'claude-fable-5-1', platform: 'CLAUDE_AI', current_leaf_message_uuid: id(6),
    chat_messages: [
      m(1, 'human', ROOT, [text('Reply with just: ok')]),
      m(2, 'assistant', id(1), [text('ok')], { stop_reason: 'end_turn' }),
      m(3, 'human', id(2), [text('a'.repeat(297))]),
      m(4, 'assistant', id(3), [text('b'.repeat(594))], { stop_reason: 'end_turn' }),
      m(5, 'human', id(2), [text('edited prompt')], {
        attachments: [{ file_name: 'paste.txt', extracted_content: 'c'.repeat(297) }],
        files: [{ file_kind: 'image', file_uuid: 'f1', preview_asset: { image_width: 1000, image_height: 750 } }],
      }),
      m(6, 'assistant', id(5), [
        { type: 'thinking', thinking: '' },
        { type: 'tool_use', name: 'web_search', input: { query: 'q' } },
        { type: 'tool_result', name: 'web_search', content: [{ type: 'knowledge', title: 'T', url: 'https://e.x', text: 'r'.repeat(297), metadata: { type: 'webpage_metadata', site_domain: 'e.x' } }], display_content: { huge: 'x'.repeat(5000) } },
        text('answer'),
      ], { stop_reason: 'end_turn' }),
    ],
  }
}
