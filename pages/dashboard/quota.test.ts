// Run: node --experimental-strip-types --no-warnings pages/dashboard/quota.test.ts
import assert from 'node:assert/strict'
import test from 'node:test'
import type { PublishedMeter } from './githubSource.ts'
import { quotaMeters } from './quota.ts'

const NOW = Date.parse('2026-09-30T12:00:00Z')
const H1 = 'a_9f8e7d6c5b4a3f2e'
const H2 = 'a_0123456789abcdef'
const meter = (values: Partial<PublishedMeter>): PublishedMeter => ({
  machine: 'm_aaaaaaaaaaaa', provider: 'anthropic', source: 'claude-code', acct: H1, window: 'week',
  usedPercent: 40, resetsAt: '2026-10-03T00:00:00Z', observedAt: '2026-09-30T11:30:00Z', ...values,
})

test('the newest reading wins per account and window; week-old readings are dropped', () => {
  const views = quotaMeters([
    meter({ machine: 'm_bbbbbbbbbbbb', usedPercent: 10, observedAt: '2026-09-30T10:00:00Z' }),
    meter({}),
    meter({ window: 'session', usedPercent: 95, resetsAt: '2026-09-30T14:00:00Z' }),
    meter({ source: 'codex', provider: 'openai', acct: H2, observedAt: '2026-09-22T11:00:00Z' }),
  ], NOW)
  assert.deepEqual(views.map(v => [v.name, v.usedPercent]), [['Claude · session', 95], ['Claude · week', 40]])
  assert.equal(views[1].resets, 'resets in 2d 12h')
  assert.equal(views[1].read, 'read 30 min ago')
  assert.equal(views[1].stale, false)
  assert.equal(views[1].account, null, 'one account per tool needs no alias')
})

test('a window that reset since its reading shows no stale percentage', () => {
  const [view] = quotaMeters([meter({ resetsAt: '2026-09-30T06:00:00Z', observedAt: '2026-09-30T02:00:00Z' })], NOW)
  assert.equal(view.resetSinceRead, true)
  assert.equal(view.usedPercent, null)
  assert.equal(view.resets, '')
  assert.equal(view.stale, true)
})

test('several accounts on one tool read as aliases, never as hashes; machine filter', () => {
  const all = [meter({ scope: 'Fable' }), meter({ acct: H2, machine: 'm_bbbbbbbbbbbb', usedPercent: 70, plan: 'max' })]
  const views = quotaMeters(all, NOW)
  assert.deepEqual(views.map(v => [v.account, v.plan, v.name]), [['acct1', 'max', 'Claude · week'], ['acct2', '', 'Claude · week · Fable']])
  assert.equal(JSON.stringify(views).includes(H1) || JSON.stringify(views).includes(H2), false)
  assert.deepEqual(quotaMeters(all, NOW, 'm_bbbbbbbbbbbb').map(v => [v.usedPercent, v.account]), [[70, null]])
})
