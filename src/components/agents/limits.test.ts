// Assertions for limits.ts. Run: node --experimental-strip-types src/components/agents/limits.test.ts
import assert from 'node:assert/strict'
import { accountName, accountTrackingKey, buildAccounts, countdown, countdownParts, initializeTracking, mockLimits, parsePlanOverrides, parseTracking, resetSeverity, severityColor, trackedChoice, weeklyMeter, type LimitRow } from './limits.ts'

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

// One email (one login) that is both a Team seat and a personal Max plan is two accounts, each with its own
// plan, organisation and meters (owner report 2026-10-09: "they are separate distinct accounts").
const team: LimitRow = { ...base, id: 't-week', provider: 'anthropic', source: 'claude-code', acct: 'a_1111111111111111', window: 'week',
  label: 'dm@example.com', name: 'Example Team', plan: 'Team', org: 'a_00000000000000e1', orgKind: 'team', usedPercent: 97, resetsAt: iso(30 * H) }
const personal: LimitRow = { ...team, id: 'p-week', name: 'Dm Example', plan: 'Max (20x)', org: 'a_00000000000000e2', orgKind: 'personal', usedPercent: 23, resetsAt: iso(100 * H) }
{
  const teamSession: LimitRow = { ...team, id: 't-session', window: 'session', usedPercent: 100, status: 'full', resetsAt: iso(2 * H) }
  const accounts = buildAccounts([team, teamSession, personal], NOW)
  assert.equal(accounts.length, 2)
  const [max, seat] = accounts
  assert.deepEqual([max.label, seat.label], ['dm@example.com · Max (20x)', 'dm@example.com · Team'])
  assert.deepEqual([max.week?.used, seat.week?.used], [23, 97])
  assert.deepEqual([max.state, seat.state], ['ready', 'blocked'])
  assert.deepEqual([max.org, seat.org, max.orgKind, seat.orgKind], ['Dm Example', 'Example Team', 'personal', 'team'])
  assert.notEqual(max.key, seat.key)
  assert.notEqual(accountTrackingKey(max), accountTrackingKey(seat))
  // Without a plan the organisation tells them apart.
  const [unnamed] = buildAccounts([{ ...team, plan: undefined }], NOW)
  assert.equal(unnamed.label, 'dm@example.com · Example Team')
  // Two Team organisations of one email are two accounts; a personal plan's upgrade is the same account.
  assert.equal(buildAccounts([team, { ...team, id: 't2', name: 'Other Team', org: 'a_00000000000000e3' }], NOW).length, 2)
  const upgraded = buildAccounts([personal, { ...personal, id: 'p2', plan: 'Max (5x)', name: 'Renamed', observedAt: iso(-2 * 60_000) }], NOW)
  assert.equal(upgraded.length, 1)
  assert.equal(accountTrackingKey(upgraded[0]), accountTrackingKey(max))
}

// Rows from an older collector (no organisation) while machines run mixed versions, for the same login (acct).
{
  const labels = (rows: LimitRow[]) => buildAccounts(rows, NOW).map((a) => a.label)
  const old = (over: Partial<LimitRow>): LimitRow => ({ ...base, id: 'old-week', provider: 'anthropic', source: 'claude-code', acct: team.acct,
    window: 'week', label: 'dm@example.com', usedPercent: 40, resetsAt: iso(30 * H), ...over })
  // An old collector on the Team seat while a new one is on the personal plan: the Team keeps its own line.
  const oldTeam = [old({ name: 'Example Team', plan: 'Team', observedAt: iso(-2 * H) }), old({ id: 'old-session', window: 'session', name: 'Example Team', plan: 'Team', usedPercent: 10, resetsAt: iso(H) })]
  const mixed = buildAccounts([...oldTeam, personal], NOW)
  assert.deepEqual(mixed.map((a) => a.label), ['dm@example.com · Max (20x)', 'dm@example.com · Team'])
  assert.deepEqual(mixed.map((a) => [a.week?.used, a.session?.used]), [[23, undefined], [40, 10]])
  // An old collector on another machine, same Team seat, with newer readings: they join the Team account, one line.
  const newerElsewhere = old({ name: 'Example Team', plan: 'Team', usedPercent: 99, observedAt: iso(-1000) })
  const joined = buildAccounts([newerElsewhere, team, personal], NOW)
  assert.deepEqual(joined.map((a) => a.label), ['dm@example.com · Max (20x)', 'dm@example.com · Team'])
  assert.equal(joined[1].week?.used, 99)
  assert.equal(joined[1].orgKind, 'team')
  // ... and an older reading of a window the Team account has newer is left out.
  assert.equal(buildAccounts([{ ...newerElsewhere, observedAt: iso(-H) }, team], NOW)[0].week?.used, 97)
  // Upgrade: the personal plan's valid week, read by the old collector before the switch to the Team seat, stays.
  const before = old({ name: 'Dm Example', plan: 'Max (20x)', usedPercent: 55, observedAt: iso(-3 * H), resetsAt: iso(90 * H) })
  const upgraded = buildAccounts([before, team], NOW)
  assert.deepEqual(upgraded.map((a) => [a.label, a.week?.used]), [['dm@example.com · Max (20x)', 55], ['dm@example.com · Team', 97]])
  // When the personal plan reports itself, the old reading joins it, and only while it is the newer one.
  assert.deepEqual(labels([before, team, personal]), ['dm@example.com · Max (20x)', 'dm@example.com · Team'])
  // A Team row must name the same organisation to join it.
  assert.deepEqual(labels([old({ name: 'Other Team', plan: 'Team' }), team]), ['dm@example.com · Team', 'dm@example.com · Team · organisation unknown'])
  // An old row naming no plan says its organisation is unknown; an old Team row with no plan joins by name.
  assert.deepEqual(labels([old({}), team]), ['dm@example.com · organisation unknown', 'dm@example.com · Team'])
  assert.deepEqual(labels([old({ name: 'Example Team', observedAt: iso(-1000) }), team]), ['dm@example.com · Team'])
  // An old bare plan row adds nothing beside the login's organisation-aware accounts.
  assert.deepEqual(labels([old({ window: 'plan', usedPercent: undefined, resetsAt: null }), team]), ['dm@example.com · Team'])
  // An old extra-usage row (no reset) whose plan matches none of them is a leftover whose id is never written again.
  assert.deepEqual(labels([old({ id: 'old-extra', window: 'extra', plan: 'Pro', usedPercent: 12, resetsAt: null }), team]), ['dm@example.com · Team'])
  // Another login's old rows are untouched, and old rows of one email still merge across machines.
  const legacy = old({ id: 'legacy', observedAt: iso(-2 * H) })
  const elsewhere = old({ id: 'elsewhere', acct: 'a_2222222222222222', window: 'session', resetsAt: iso(H) })
  const merged = buildAccounts([legacy, elsewhere], NOW)
  assert.equal(merged.length, 1)
  assert.equal(merged[0].orgKind, '')
  assert.equal(merged[0].label, 'dm@example.com')
}

// A Team organisation's identity is its hash: a rename keeps the account, its line and its saved choice.
{
  const renamed: LimitRow = { ...team, id: 't-renamed', name: 'Renamed Team', observedAt: iso(-1000) }
  const [one] = buildAccounts([team], NOW)
  const both = buildAccounts([team, renamed], NOW)
  assert.equal(both.length, 1)
  assert.equal(accountTrackingKey(both[0]), accountTrackingKey(one))
  assert.ok(accountTrackingKey(one).includes(team.org as string))
  // A choice saved under the organisation's name (before the hash was used) still applies.
  const byName = JSON.stringify(['anthropic', 'email', 'dm@example.com', 'team', 'example team'])
  assert.equal(trackedChoice({ [byName]: false }, one), false)
  assert.equal(trackedChoice({ [byName]: false, [JSON.stringify(['anthropic', 'email', 'dm@example.com'])]: true }, one), false)
  // An account with no email keeps the choice saved under its identity before organisations were reported.
  const noEmail: LimitRow = { ...personal, label: undefined }
  const [anon] = buildAccounts([noEmail], NOW)
  const before = JSON.stringify(['anthropic', 'account', `anthropic|${personal.acct}`])
  assert.notEqual(accountTrackingKey(anon), before)
  assert.equal(trackedChoice({ [before]: false }, anon), false)
  assert.equal(initializeTracking([anon], { [before]: false })[accountTrackingKey(anon)], false)
}

// Screen readers can tell the accounts of one email apart.
{
  const accounts = buildAccounts([team, personal, { ...team, id: 't2', name: 'Other Team', org: 'a_00000000000000e3' }], NOW)
  const names = accounts.map(accountName)
  assert.equal(new Set(names).size, 3)
  assert.ok(names.includes('dm@example.com · Team · Example Team'))
  assert.ok(names.includes('dm@example.com · Max (20x)'))
}

// A full meter that never said when it resets stops blocking once a whole window has passed since the reading.
{
  const row: LimitRow = { ...base, id: 'full', provider: 'anthropic', source: 'claude-code', window: 'session', usedPercent: 100, status: 'full', resetsAt: null, observedAt: iso(-4 * H) }
  assert.equal(buildAccounts([row], NOW)[0].state, 'blocked')
  const [later] = buildAccounts([row], NOW + 2 * H)
  assert.equal(later.state, 'unknown')
  assert.equal(later.session?.lapsed, true)
  const week: LimitRow = { ...row, window: 'week', observedAt: iso(-6 * 24 * H) }
  assert.equal(buildAccounts([week], NOW)[0].state, 'blocked')
  assert.equal(buildAccounts([week], NOW + 25 * H)[0].state, 'unknown')
  // A window of unknown length keeps its reading, and so does a source whose windows of that name vary in length.
  assert.equal(buildAccounts([{ ...row, window: '90m', observedAt: iso(-30 * 24 * H) }], NOW)[0].scoped[0].full, true)
  assert.equal(buildAccounts([{ ...week, provider: 'openai', source: 'codex' }], NOW + 25 * H)[0].state, 'blocked')
}

// Saved choices made per email carry over to each organisation's account, and a new choice is kept per account.
{
  const legacyKey = JSON.stringify(['anthropic', 'email', 'dm@example.com'])
  const accounts = buildAccounts([team, personal], NOW)
  const [max, seat] = accounts
  assert.equal(trackedChoice({}, max), true)
  assert.equal(trackedChoice({ [legacyKey]: false }, max), false)
  assert.equal(trackedChoice({ [legacyKey]: false, [accountTrackingKey(max)]: true }, max), true)
  const migrated = initializeTracking(accounts, { [legacyKey]: false })
  assert.equal(migrated[accountTrackingKey(max)], false)
  assert.equal(migrated[accountTrackingKey(seat)], false)
  assert.equal(migrated[legacyKey], false)
  const chosen = { ...migrated, [accountTrackingKey(seat)]: true }
  assert.equal(initializeTracking(accounts, chosen), chosen)
  assert.equal(trackedChoice(chosen, seat), true)
  assert.equal(trackedChoice(chosen, max), false)
  // A legacy account keeps its key.
  const [legacy] = buildAccounts([{ ...team, orgKind: undefined, org: undefined }], NOW)
  assert.equal(accountTrackingKey(legacy), legacyKey)
  // Plan labels too: a label saved per email applies until one is saved per account.
  const planless: LimitRow[] = [{ ...team, plan: undefined }, { ...personal, plan: undefined }]
  const relabelled = buildAccounts(planless, NOW, { [legacyKey]: { name: 'Free', reportedPlan: '' } })
  assert.deepEqual(relabelled.map((a) => a.plan), ['Free', 'Free'])
  const [planlessMax] = buildAccounts([planless[1]], NOW)
  const specific = { [legacyKey]: { name: 'Free', reportedPlan: '' }, [accountTrackingKey(planlessMax)]: { name: 'Max 20x', reportedPlan: '' } }
  assert.deepEqual(buildAccounts(planless, NOW, specific).map((a) => [a.orgKind, a.plan]).sort(), [['personal', 'Max 20x'], ['team', 'Free']])
}

// The mock previews one email with a Team seat and a personal plan, its older collector's row hidden.
{
  const both = buildAccounts(mockLimits(NOW), NOW).filter((a) => a.email === 'both@example.dev')
  assert.deepEqual(both.map((a) => a.label), ['both@example.dev · Max (20x)', 'both@example.dev · Team'])
}

console.log('limits.test.ts: ok')
