import test from 'node:test'
import assert from 'node:assert/strict'
import { channelDays, channelOptionLabel, claudeChannelCounts } from './channels.ts'
import { normaliseDays, buildCells, DEFAULT_VIEW, summarise } from './usage.ts'
import { buildUsage, type Published } from '../../../pages/dashboard/githubSource.ts'
import { machineDays } from './machines.ts'

const published: Published = { machines: ['m_a', 'm_b'].map((id, i) => ({
  id, meta: { label: id }, usage: [{ schema: 2,
    cols: ['date', 'provider', 'source', 'model', 'in', 'cacheW', 'cacheR', 'out', 'events', 'prompts', 'promptsNoUsage', 'modelPrompts'],
    rows: [
      ['2026-10-22', 'anthropic', 'claude-code', 'code-model', 100, 0, 0, 20, 1, 1, 0, 1],
      ['2026-10-22', 'anthropic', 'native-claude', 'native-model', 10 + i, 20, 30, 40, 1, 2, 1, 2],
    ],
  }],
})) }

test('both reports filter recorded channels without filling gaps or borrowing prompt counts', () => {
  const days = normaliseDays(buildUsage(published, new Date('2026-10-22T12:00:00Z')))
  const counts = claudeChannelCounts(days, '2026-10-22', '2026-10-22')
  assert.equal(counts.find(r => r.source === 'native-claude')!.out, 80)
  assert.equal(counts.find(r => r.source === 'web-claude')!.observed, false)
  const machineCounts = claudeChannelCounts(days, '2026-10-22', '2026-10-22', 'm_b')
  assert.equal(machineCounts.find(r => r.source === 'native-claude')!.in, 11)
  assert.equal(claudeChannelCounts(days, '2026-10-23', '2026-10-24')[0].observed, false)
  const selected = channelDays(days, 'native-claude')
  assert.deepEqual(selected[0].providers.anthropic!.promptsByModel, { 'native-model': 4 })
  const view = summarise(buildCells(selected, DEFAULT_VIEW), '2026-10-22', '2026-10-22')
  assert.equal(view.total, 201)
  assert.equal(view.prompts, 4)
  const one = machineDays(selected, 'm_b').days
  assert.equal(summarise(buildCells(one, DEFAULT_VIEW), '2026-10-22', '2026-10-22').total, 101)
  assert.deepEqual(channelDays(days, 'web-claude')[0].providers, {})
  assert.equal(summarise(buildCells(days, DEFAULT_VIEW), '2026-10-22', '2026-10-22').total, 441)
})

test('legacy rollups keep totals but do not invent channel provenance', () => {
  const days = normaliseDays(buildUsage(published, new Date()))
  delete days[0].providers.anthropic!.bySource
  assert.deepEqual(channelDays(days, 'claude-code')[0].providers, {})
  assert.equal(channelDays(days, 'all'), days)
})

test('estimated channels are counted apart from exact ones on both reports', () => {
  const cols = ['date', 'provider', 'source', 'model', 'q', 'in', 'cacheW', 'cacheR', 'out', 'events', 'prompts', 'promptsNoUsage', 'modelPrompts']
  const chat: Published = { machines: [{ id: 'm_a', meta: { label: 'm_a' }, usage: [{ schema: 2, cols, rows: [
    ['2026-10-22', 'anthropic', 'claude-code', 'code-model', '', 100, 0, 0, 20, 1, 1, 0, 1],
    ['2026-10-22', 'anthropic', 'web-claude', 'claude-fable-5-1', 'e', 900, 0, 0, 100, 2, 2, 0, 0],
    ['2026-10-22', 'anthropic', 'chat-claude', 'claude-fable-5-1', 'e', 50, 0, 0, 10, 1, 1, 0, 0],
    ['2026-10-22', 'anthropic', 'native-claude', 'cowork-model', '', 0, 20, 60, 20, 1, 1, 0, 0],
    ['2026-10-22', 'anthropic', 'native-claude', 'claude-fable-5-1', 'e', 100, 0, 0, 0, 1, 1, 0, 0],
  ] }] }] }
  const days = normaliseDays(buildUsage(chat, new Date('2026-10-22T12:00:00Z')))
  const all = claudeChannelCounts(days, '2026-10-22', '2026-10-22')
  const row = (s: string, rows = all) => rows.find(r => r.source === s)!
  assert.equal(row('claude-code').estimated, 0)
  assert.equal(row('web-claude').estimated, 1000)
  assert.equal(row('chat-claude').estimated, 60)
  assert.equal(row('chat-claude').prompts, 1)
  assert.equal(row('native-claude').estimated, 100)
  assert.equal(row('native-claude').estimatedApprox, false)
  const one = claudeChannelCounts(days, '2026-10-22', '2026-10-22', 'm_a')
  assert.equal(row('web-claude', one).estimated, 1000, 'an all-estimated channel stays exact per machine')
  assert.equal(row('native-claude', one).estimated, 100, 'a mixed day takes its ratio')
  assert.equal(row('native-claude', one).estimatedApprox, true)
  // Native's exact part (Code tab, Cowork) and estimated part (app chat) apart.
  assert.deepEqual(row('native-claude').parts, { exact: { in: 0, cacheW: 20, cacheR: 60, out: 20 }, estimated: { in: 100, cacheW: 0, cacheR: 0, out: 0 } })
  assert.deepEqual(row('native-claude', one).parts, row('native-claude').parts, 'one machine: each bucket takes the day ratio')
  assert.deepEqual(row('claude-code').parts.estimated, { in: 0, cacheW: 0, cacheR: 0, out: 0 })
  assert.equal(channelOptionLabel('web-claude'), 'Claude Web (estimated)')
  assert.equal(channelOptionLabel('native-claude'), 'Claude Native (apps)')
  const selected = channelDays(days, 'chat-claude')
  assert.equal(summarise(buildCells(selected, DEFAULT_VIEW), '2026-10-22', '2026-10-22').total, 60)
})
