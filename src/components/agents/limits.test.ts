// Assertions for limits.ts. Run: node --experimental-strip-types src/components/agents/limits.test.ts
import assert from 'node:assert/strict'
import { accountTrackingKey, buildAccounts, countdown, countdownParts, initializeTracking, mockLimits, parsePlanOverrides, parseTracking, resetSeverity, severityColor, weeklyMeter, type LimitRow } from './limits.ts'

const NOW = Date.parse('2026-10-01T12:00:00Z')
const iso = (ms: number) => new Date(NOW + ms).toISOString()
const H = 3_600_000

const base = { acct: '', acctQ: 'unknown', observedAt: iso(-60_000), label: 'a@example.com' } as const

// A stored "paused" from an older collector is not a block: Claude only marks which limit it leads with.
{
  const rows: LimitRow[] = [
    { ...base, id: '1', provider: 'anthropic', source: 'claude-code', window: 'session', usedPercent: 0, resetsAt: iso(3 * H), status: 'ok' },
    { ...base, id: '2', provider: 'anthropic', source: 'claude-code', window: 'week', usedPercent: 16, resetsAt: iso(90 * H), status: 'paused' },
  ]
  const [a] = buildAccounts(rows, NOW)
  assert.equal(a.state, 'ready')
  assert.equal(a.week?.used, 16)
}

// A full session blocks until it resets; a later full week outranks it.
{
  const rows: LimitRow[] = [
    { ...base, id: '1', provider: 'anthropic', source: 'claude-code', window: 'session', usedPercent: 100, resetsAt: iso(2 * H), status: 'full' },
    { ...base, id: '2', provider: 'anthropic', source: 'claude-code', window: 'week', usedPercent: 40, resetsAt: iso(90 * H) },
  ]
  const [a] = buildAccounts(rows, NOW)
  assert.equal(a.state, 'blocked')
  assert.equal(a.blockedBy?.window, 'session')
  rows[1] = { ...rows[1], usedPercent: 100, status: 'full' }
  assert.equal(buildAccounts(rows, NOW)[0].blockedBy?.window, 'week')
}

// A full window whose reset has passed no longer blocks, and its reading is marked lapsed.
{
  const rows: LimitRow[] = [{ ...base, id: '1', provider: 'openai', source: 'codex', window: 'week', usedPercent: 100, resetsAt: iso(-H), status: 'full' }]
  const [a] = buildAccounts(rows, NOW)
  assert.equal(a.state, 'unknown')
  assert.equal(a.week?.lapsed, true)
}

// A full model-scoped week blocks that model only.
{
  const rows: LimitRow[] = [
    { ...base, id: '1', provider: 'anthropic', source: 'claude-code', window: 'week', usedPercent: 50, resetsAt: iso(90 * H) },
    { ...base, id: '2', provider: 'anthropic', source: 'claude-code', window: 'week', scope: 'Fable', usedPercent: 100, resetsAt: iso(90 * H), status: 'full' },
  ]
  const [a] = buildAccounts(rows, NOW)
  assert.equal(a.state, 'ready')
  assert.equal(a.scoped[0].full, true)
}

// Plan-only accounts have no meter; internal tier codes are not plan names.
{
  const rows: LimitRow[] = [
    { ...base, id: '1', provider: 'xai', source: 'grok-cli', window: 'plan', plan: 'SuperGrok Heavy' },
    { ...base, id: '2', provider: 'anthropic', source: 'claude-code', window: 'week', plan: 'default_raven', usedPercent: 3, resetsAt: iso(H) },
  ]
  const accounts = buildAccounts(rows, NOW)
  assert.deepEqual(accounts.map((a) => a.provider), ['anthropic', 'xai'])
  assert.equal(accounts[0].plan, '')
  assert.equal(accounts[1].state, 'unknown')
  assert.equal(accounts[1].plan, 'SuperGrok Heavy')
}

// The mock covers every state.
{
  const states = new Set(buildAccounts(mockLimits(NOW), NOW).map((a) => a.state))
  assert.deepEqual([...states].sort(), ['blocked', 'ready', 'unknown'])
}

// Newer reported plans outrank older window snapshots regardless of API row order.
{
  const old: LimitRow = { ...base, id: 'old-plan', provider: 'openai', source: 'codex', window: 'week', plan: 'Plus', observedAt: iso(-2 * H) }
  const recent: LimitRow = { ...old, id: 'new-plan', window: 'session', plan: 'Pro', observedAt: iso(-H) }
  assert.equal(buildAccounts([old, recent], NOW)[0].plan, 'Pro')
  assert.equal(buildAccounts([recent, old], NOW)[0].plan, 'Pro')
  assert.equal(buildAccounts([{ ...old, plan: 'DEFAULT_RAVEN' }], NOW)[0].plan, '')
  assert.equal(buildAccounts([{ ...old, plan: undefined }], NOW)[0].plan, '')
}

assert.equal(countdown(2 * H + 14 * 60_000 + 3_000), '2h 14m')
assert.equal(countdown(12 * 60_000 + 5_000), '12m 05s')
assert.equal(countdown(45_000), '45s')
assert.equal(countdown(50 * H), '2d 2h')

// Weekly comparisons include a real 0% reading, but never missing, invalid or already-reset readings.
{
  const row: LimitRow = { ...base, id: 'week', provider: 'openai', source: 'codex', window: 'week', usedPercent: 0, resetsAt: iso(7 * 24 * H) }
  const [zero] = buildAccounts([row], NOW)
  assert.equal(weeklyMeter(zero)?.used, 0)
  assert.equal(zero.state, 'ready')
  for (const change of [{ usedPercent: undefined }, { usedPercent: NaN }, { usedPercent: -1 }, { usedPercent: 101 }, { observedAt: 'invalid' }, { resetsAt: iso(0) }]) {
    const [account] = buildAccounts([{ ...row, ...change }], NOW)
    assert.equal(weeklyMeter(account), undefined)
    assert.equal(account.state, 'unknown')
  }
}

// Explicit full status still blocks without a percentage; offset timestamps reset at the exact instant.
{
  const row: LimitRow = { ...base, id: 'full', provider: 'anthropic', source: 'claude-code', window: 'week', status: 'full', resetsAt: '2026-10-01T20:00:00+07:00' }
  const [full] = buildAccounts([row], NOW)
  assert.equal(full.state, 'blocked')
  assert.equal(weeklyMeter(full), undefined)
  assert.equal(full.blockedBy?.resetsAt, NOW + H)
  assert.equal(buildAccounts([row], NOW + H)[0].state, 'unknown')
  const [unparsed] = buildAccounts([{ ...row, resetsAt: '2026-10-01T20:00:00', status: 'ok', usedPercent: 25 }], NOW)
  assert.equal(unparsed.week?.resetsAt, null)
  assert.equal(weeklyMeter(unparsed)?.used, 25)
}

// Both colours share the same direction: more used or a longer wait is worse, with safe bounds.
{
  assert.equal(severityColor(-1), severityColor(0))
  assert.equal(severityColor(2), severityColor(1))
  assert.equal(severityColor(0.5), 'rgb(224, 173, 67)')
  assert.notEqual(severityColor(NaN), severityColor(0))
  assert.equal(resetSeverity(NOW - H, NOW), 0)
  assert.equal(resetSeverity(NOW + 3.5 * 24 * H, NOW), 0.5)
  assert.equal(resetSeverity(NOW + 8 * 24 * H, NOW), 1)
  assert.equal(resetSeverity(null, NOW), null)
  assert.equal(resetSeverity(Infinity, NOW), null)
}

// Large countdowns keep meaningful units; invalid values must not imply a reset is due now.
{
  assert.deepEqual(countdownParts(78 * H), [{ value: 3, unit: 'day' }, { value: 6, unit: 'hour' }])
  assert.deepEqual(countdownParts(-1), [{ value: 0, unit: 'second' }])
  assert.deepEqual(countdownParts(NaN), [])
  assert.equal(countdown(NaN), '—')
}

// Tracking survives new row IDs, plan changes, machine account IDs, and email casing; providers stay separate.
{
  const row: LimitRow = { ...base, id: 'old-row', provider: 'openai', source: 'codex', acct: 'machine-a-account', window: 'week', usedPercent: 20, resetsAt: iso(90 * H) }
  const updated: LimitRow = { ...row, id: 'new-row', acct: 'machine-b-account', label: ' A@Example.COM ', plan: 'Pro', observedAt: iso(-1000) }
  const [before] = buildAccounts([row], NOW)
  const [after] = buildAccounts([updated], NOW)
  assert.equal(accountTrackingKey(before), accountTrackingKey(after))
  assert.equal(before.key, after.key)
  assert.equal(buildAccounts([row, updated], NOW).length, 1)
  const [anotherProvider] = buildAccounts([{ ...row, provider: 'anthropic', source: 'claude-code' }], NOW)
  assert.notEqual(accountTrackingKey(before), accountTrackingKey(anotherProvider))
  const [unlabeled] = buildAccounts([{ ...row, label: '' }], NOW)
  const [unlabeledRefresh] = buildAccounts([{ ...row, id: 'another-row', label: '', plan: 'Plus' }], NOW)
  assert.equal(accountTrackingKey(unlabeled), accountTrackingKey(unlabeledRefresh))
  const [otherUnlabeled] = buildAccounts([{ ...row, label: '', acct: 'another-account' }], NOW)
  assert.notEqual(accountTrackingKey(unlabeled), accountTrackingKey(otherUnlabeled))
}

// Defaults are established once; lapsing, disappearing and returning never overwrite a user's choices.
{
  const rows: LimitRow[] = [
    { ...base, id: 'meter', provider: 'openai', source: 'codex', window: 'week', usedPercent: 0, resetsAt: iso(H) },
    { ...base, id: 'no-meter', provider: 'xai', source: 'grok-cli', window: 'plan' },
  ]
  const accounts = buildAccounts(rows, NOW)
  const [metered, noMeter] = accounts
  const initial = initializeTracking(accounts, {})
  assert.equal(initial[accountTrackingKey(metered)], true)
  assert.equal(initial[accountTrackingKey(noMeter)], false)
  assert.equal(initializeTracking(buildAccounts(rows, NOW + 2 * H), initial), initial)
  assert.equal(initializeTracking([], initial), initial)
  const explicit = { ...initial, [accountTrackingKey(metered)]: false, [accountTrackingKey(noMeter)]: true }
  assert.equal(initializeTracking(accounts, explicit), explicit)
  assert.equal(initializeTracking(buildAccounts(rows, NOW + 2 * H), explicit), explicit)
  const [newAccount] = buildAccounts([{ ...rows[0], label: 'new@example.com' }], NOW)
  const expanded = initializeTracking([...accounts, newAccount], explicit)
  assert.notEqual(expanded, explicit)
  assert.equal(expanded[accountTrackingKey(newAccount)], true)
  assert.equal(expanded[accountTrackingKey(metered)], false)
  assert.equal(expanded[accountTrackingKey(noMeter)], true)
  assert.equal(Object.hasOwn(explicit, accountTrackingKey(newAccount)), false)
}

// Browser storage is untrusted: malformed shapes/types cannot silently opt accounts in or out.
{
  for (const raw of [null, '', '{broken', 'null', '[]', 'true', '42']) assert.deepEqual(parseTracking(raw), {})
  assert.deepEqual(parseTracking('{"checked":true,"unchecked":false,"string":"false","number":0,"nil":null,"":true}'), { checked: true, unchecked: false })
  const stored = { '["openai","email","a@example.com"]': false, '["xai","email","a@example.com"]': true }
  assert.deepEqual(parseTracking(JSON.stringify(stored)), stored)
}

// Owner clarifications affect only one account and one source plan; new provider information wins.
{
  const row: LimitRow = { ...base, id: 'plan', provider: 'openai', source: 'codex', window: 'plan', plan: 'Pro' }
  const [account] = buildAccounts([row], NOW)
  const overrides = { [accountTrackingKey(account)]: { name: 'Pro 200', reportedPlan: ' pro ' } }
  assert.equal(buildAccounts([row], NOW, overrides)[0].plan, 'Pro 200')
  assert.equal(buildAccounts([{ ...row, label: 'other@example.com' }], NOW, overrides)[0].plan, 'Pro')
  assert.equal(buildAccounts([{ ...row, provider: 'anthropic', source: 'claude-code' }], NOW, overrides)[0].plan, 'Pro')
  assert.equal(buildAccounts([{ ...row, plan: 'Plus' }], NOW, overrides)[0].plan, 'Plus')
  assert.equal(buildAccounts([{ ...row, plan: 'Pro 400' }], NOW, overrides)[0].plan, 'Pro 400')
  const missing: LimitRow = { ...row, provider: 'anthropic', source: 'claude-code', plan: undefined }
  const [unknown] = buildAccounts([missing], NOW)
  const free = { [accountTrackingKey(unknown)]: { name: 'Free', reportedPlan: '' } }
  assert.equal(buildAccounts([missing], NOW, free)[0].plan, 'Free')
  assert.equal(buildAccounts([{ ...missing, label: 'other@example.com' }], NOW, free)[0].plan, '')
  assert.equal(buildAccounts([{ ...missing, plan: 'Max (5x)' }], NOW, free)[0].plan, 'Max (5x)')
}

// Plan-label storage accepts only bounded explicit string pairs, including an intentionally unknown source.
{
  for (const raw of [null, '{broken', 'null', '[]', '42']) assert.deepEqual(parsePlanOverrides(raw), {})
  assert.deepEqual(parsePlanOverrides(JSON.stringify({
    valid: { name: ' Free ', reportedPlan: '' },
    invalid: { name: 'Pro 200' },
    number: { name: 200, reportedPlan: 'Pro' },
    blank: { name: ' ', reportedPlan: '' },
    long: { name: 'x'.repeat(81), reportedPlan: '' },
    control: { name: 'Free\n', reportedPlan: '' },
  })), { valid: { name: 'Free', reportedPlan: '' } })
}

console.log('limits.test.ts: ok')
