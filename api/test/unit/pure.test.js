// Unit tests for the pure helpers: ids, crypto, merge, rollup math,
// validation and owner-auth decisions.
import test from 'node:test'
import assert from 'node:assert/strict'
import { accountHash, eventId, kFingerprint, newInvite, INVITE_RE, newToken, sessionKey } from '../../src/lib/ids.js'
import { decryptJson, encryptJson, parseKey } from '../../src/lib/crypto.js'
import { mergeEvent, rowToEvent, sameEvent, storedFields } from '../../src/lib/merge.js'
import { aliasAccts, computeRollup, legacyShape } from '../../src/lib/rollup.js'
import {
  PROVIDERS, SOURCE_PROVIDER, checkCaps, cleanCountry, cleanLabel, localDate, truncateUtf8, utcMonth,
  validateActivity, validateHeartbeat, validatePrompt, validateUsage,
} from '../../src/lib/validate.js'
import { publicMachines, parseDays } from '../../src/lib/usage.js'
import { ownerDecision, parsePrincipal } from '../../src/lib/auth.js'
import { usage, activity, cursorUsage, geminiUsage, prompt, principal, testKey } from '../fixtures.js'

const NOW = new Date('2026-09-29T12:00:00Z')

// Expected values printed by collector/internal/model (model.go) via `go run`.
test('ids match the Go model helpers', () => {
  assert.equal(eventId('usage', 'anthropic', 'claude-code', 'msg_01ABCdef'), '0a4ea4e921783b56d5979a55deaf32f2')
  assert.equal(eventId('activity', 'openai', 'codex', 'rollout-1:0'), 'cf6d8b3c775af8c0d67b8b212f4bd7c0')
  assert.equal(eventId('prompt', 'xai', 'grok-cli', 'prompt-42'), 'e6728af711017ab796c6a9d7e574d3c6')
  assert.equal(sessionKey('anthropic', '11111111-2222-3333-4444-555555555555'), 's_00d5d220d968c459')
  assert.equal(sessionKey('openai', ''), '')
  const k = Buffer.from([...Array(32).keys()])
  assert.equal(kFingerprint(k), '630dcd2966c43366')
  assert.equal(accountHash(k, 'anthropic', 'acct-uuid-1'), 'a_1539dc8e48fdf4d2')
})

test('invites and tokens have the documented shapes', () => {
  for (let i = 0; i < 20; i++) {
    assert.match(newInvite(), INVITE_RE)
    assert.match(newToken(), /^[A-Za-z0-9_-]{43}$/)
  }
})

test('crypto round-trips and rejects tampering', () => {
  const key = parseKey(testKey())
  const id = eventId('prompt', 'anthropic', 'claude-code', 'x')
  const obj = { text: 'héllo ✓', workspace: '/w', acctLabel: 'l', acct: 'a_0', machine: 'm', session: '' }
  const enc = encryptJson(key, id, obj)
  assert.deepEqual(decryptJson(key, id, enc), obj)
  const raw = Buffer.from(enc, 'base64')
  assert.equal(raw.length, 12 + Buffer.byteLength(JSON.stringify(obj)) + 16)
  assert.notEqual(encryptJson(key, id, obj), enc, 'random IV per encryption')
  // Wrong AAD (a different id) must fail.
  assert.throws(() => decryptJson(key, eventId('prompt', 'anthropic', 'claude-code', 'y'), enc))
  // A flipped ciphertext bit must fail.
  raw[14] ^= 1
  assert.throws(() => decryptJson(key, id, raw.toString('base64')))
  // Wrong key must fail.
  assert.throws(() => decryptJson(parseKey(testKey()), id, enc))
  assert.throws(() => parseKey(Buffer.alloc(16).toString('base64')))
  assert.throws(() => parseKey(''))
})

const norm = (x) => {
  const r = (x.hasUsage === undefined ? validateUsage : validateActivity)(x, NOW)
  assert.ok(r.ok, r.error)
  return r.value
}

test('merge: same pv takes fieldwise max and min ts', () => {
  const a = norm(usage('m1', { out: 10, in: 5, ts: '2026-09-20T10:00:05Z' }))
  const b = norm(usage('m1', { out: 40, in: 3, cacheR: 2000, ts: '2026-09-20T10:00:01Z' }))
  const m = mergeEvent(a, b)
  assert.equal(m.out, 40)
  assert.equal(m.in, 5)
  assert.equal(m.cacheR, 2000)
  assert.equal(m.ts, '2026-09-20T10:00:01.000Z')
  // Order independence.
  const m2 = mergeEvent(b, a)
  assert.ok(sameEvent(m, m2))
  // Idempotence.
  assert.ok(sameEvent(m, mergeEvent(m, b)))
})

test('merge: higher pv replaces, lower pv is ignored', () => {
  const v1 = norm(usage('m2', { out: 500, pv: 1 }))
  const v2 = norm(usage('m2', { out: 20, pv: 2 }))
  const up = mergeEvent(v1, v2)
  assert.equal(up.out, 20)
  assert.equal(up.pv, 2)
  const down = mergeEvent(v2, v1)
  assert.equal(down.out, 20)
  assert.equal(down.pv, 2)
})

test('merge: account quality, model and session keep the better value', () => {
  const inferred = norm(usage('m3', { acct: 'a_' + '1'.repeat(16), acctQ: 'inferred', model: '' }))
  const timeline = norm(usage('m3', { acct: 'a_' + '2'.repeat(16), acctQ: 'timeline', model: 'claude-x', session: '' }))
  for (const m of [mergeEvent(inferred, timeline), mergeEvent(timeline, inferred)]) {
    assert.equal(m.acct, 'a_' + '2'.repeat(16))
    assert.equal(m.acctQ, 'timeline')
    assert.equal(m.model, 'claude-x')
    assert.ok(m.session)
  }
  const unknown = norm(usage('m3', { acct: '', acctQ: 'unknown' }))
  assert.equal(mergeEvent(unknown, inferred).acctQ, 'inferred')
  // A lower-pv sighting can still improve attribution.
  const v2 = norm(usage('m4', { pv: 2, acct: '', acctQ: 'unknown' }))
  const v1 = norm(usage('m4', { pv: 1, acctQ: 'session' }))
  assert.equal(mergeEvent(v2, v1).acctQ, 'session')
  assert.equal(mergeEvent(v2, v1).pv, 2)
})

test('merge: activity keeps min ts and ORs hasUsage', () => {
  const a = norm(activity('p1', { hasUsage: false, ts: '2026-09-20T10:00:00Z' }))
  const b = norm(activity('p1', { hasUsage: true, ts: '2026-09-20T09:00:00Z' }))
  const m = mergeEvent(a, b)
  assert.equal(m.hasUsage, true)
  assert.equal(m.ts, '2026-09-20T09:00:00.000Z')
})

test('merge: cacheW1h is a fieldwise max like every token field, and stored', () => {
  const a = norm(usage('w1', { cacheW: 100, cacheW1h: 60 }))
  const b = norm(usage('w1', { cacheW: 120, cacheW1h: 20 }))
  for (const m of [mergeEvent(a, b), mergeEvent(b, a)]) {
    assert.equal(m.cacheW, 120)
    assert.equal(m.cacheW1h, 60)
    assert.ok(m.cacheW1h <= m.cacheW)
  }
  // Higher pv replaces it too (the Claude parser bump to 2 re-sends every row).
  const v2 = norm(usage('w1', { pv: 2, cacheW: 90, cacheW1h: 90 }))
  assert.equal(mergeEvent(a, v2).cacheW1h, 90)
  // It round-trips through the table row; rows from before v1.1 read as 0.
  const row = { rowKey: a.id, ...storedFields(mergeEvent(null, a)) }
  assert.equal(row.cacheW1h, 60)
  assert.equal(rowToEvent(row).cacheW1h, 60)
  const legacy = { ...row }
  delete legacy.cacheW1h
  assert.equal(rowToEvent(legacy).cacheW1h, 0)
})

const M1 = 'm_' + '1'.repeat(16)
const M2 = 'm_' + '2'.repeat(16)
const at = (ev, machine, cc) => ({ ...ev, machine, cc })

test('merge: machine and cc are set on insert and never changed', () => {
  const first = at(norm(usage('f1', { out: 5 })), M1, 'DE')
  const second = at(norm(usage('f1', { out: 9, ts: '2026-09-20T09:00:00Z' })), M2, 'GB')
  const inserted = mergeEvent(null, first)
  assert.equal(inserted.machine, M1)
  assert.equal(inserted.cc, 'DE')
  // Same pv, higher pv and lower pv sightings from another machine.
  for (const later of [second, { ...second, pv: 5 }, { ...second, pv: 0 }]) {
    const m = mergeEvent(inserted, later)
    assert.equal(m.machine, M1)
    assert.equal(m.cc, 'DE')
  }
  assert.equal(mergeEvent(inserted, second).out, 9)
  // The first reporter keeps an unknown country; another machine cannot fill it.
  const noCc = mergeEvent(null, at(norm(usage('f2')), M1, ''))
  assert.equal(mergeEvent(noCc, second).cc, '')
  // Activity follows the same rule.
  const act = mergeEvent(null, at(norm(activity('f3')), M1, 'DE'))
  const actM = mergeEvent(act, at(norm(activity('f3', { hasUsage: false })), M2, 'GB'))
  assert.deepEqual([actM.machine, actM.cc], [M1, 'DE'])
  // A row written before v1.1 has no reporter: its first later sighting fills
  // both, and from then on they are fixed.
  const legacyRow = rowToEvent({ rowKey: first.id, ...storedFields({ ...norm(usage('f1', { out: 5 })), machine: '', cc: '' }) })
  assert.equal(legacyRow.machine, '')
  const filled = mergeEvent(legacyRow, second)
  assert.deepEqual([filled.machine, filled.cc], [M2, 'GB'])
  assert.deepEqual([mergeEvent(filled, first).machine, mergeEvent(filled, first).cc], [M2, 'GB'])
  // Re-sending from the first reporter is a no-op.
  assert.ok(sameEvent(inserted, mergeEvent(inserted, first)))
  assert.ok(!sameEvent(inserted, { ...inserted, cc: 'GB' }))
})

test('rollup v2: tiers, models, prompts, accts, byMachine and byCountry', () => {
  const evs = [
    at(norm(usage('r1', { in: 100, cacheW: 20, cacheW1h: 5, cacheR: 1000, out: 50, reasoning: 10, calls: 2 })), M1, 'DE'),
    at(norm(usage('r2', { in: 1, cacheW: 0, cacheR: 3, out: 1, model: '' })), M2, ''),
    at(norm(usage('r3', { q: 'estimated', in: 10, cacheW: 4, cacheW1h: 4, cacheR: 0, out: 5 })), M1, 'DE'),
    at(norm(activity('a1', { hasUsage: true })), M1, 'DE'),
    at(norm(activity('a2', { hasUsage: false, acct: 'a_' + 'f'.repeat(16) })), '', ''),
  ]
  const r = computeRollup('2026-09-20', evs)
  assert.equal(r.v, 3)
  assert.equal(r.date, '2026-09-20')
  const p = r.providers.anthropic
  assert.deepEqual(p.exact, { in: 101, cacheW: 20, cacheW1h: 5, cacheR: 1003, out: 51, reasoning: 10, calls: 3, events: 2 })
  assert.deepEqual(p.estimated, { in: 10, cacheW: 4, cacheW1h: 4, cacheR: 0, out: 5, reasoning: 0, calls: 1, events: 1 })
  assert.equal(p.prompts, 2)
  assert.equal(p.promptsNoUsage, 1)
  assert.equal(p.accts.length, 2)
  assert.deepEqual(p.models, {
    exact: {
      'claude-test-model': { in: 100, cacheW: 20, cacheW1h: 5, cacheR: 1000, out: 50, reasoning: 10, calls: 2 },
      unknown: { in: 1, cacheW: 0, cacheW1h: 0, cacheR: 3, out: 1, reasoning: 0, calls: 1 },
    },
    estimated: {
      'claude-test-model': { in: 10, cacheW: 4, cacheW1h: 4, cacheR: 0, out: 5, reasoning: 0, calls: 1 },
    },
  })
  // Exact + estimated tokens, all activity; unknown reporter and country keyed.
  assert.deepEqual(p.byMachine, {
    [M1]: { in: 110, cacheW: 24, cacheR: 1000, out: 55, prompts: 1 },
    [M2]: { in: 1, cacheW: 0, cacheR: 3, out: 1, prompts: 0 },
    unknown: { in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 1 },
  })
  assert.deepEqual(p.byCountry, {
    DE: { in: 110, cacheW: 24, cacheR: 1000, out: 55, prompts: 1 },
    ZZ: { in: 1, cacheW: 0, cacheR: 3, out: 1, prompts: 1 },
  })
  assert.deepEqual(Object.keys(r.providers), ['anthropic'])
  assert.deepEqual(computeRollup('2026-09-21', []), { v: 3, date: '2026-09-21', providers: {} })
})

test('rollup v2: models, byMachine and byCountry always add up to the provider totals', () => {
  let x = 7
  // High bits: an LCG's low bits cycle with a tiny period, so `x % 4` would
  // never draw every provider.
  const rnd = (n) => {
    x = (x * 1103515245 + 12345) & 0x7fffffff
    return (x >>> 16) % n
  }
  const kinds = [
    ['anthropic', 'claude-code'], ['openai', 'codex'], ['xai', 'grok-cli'], ['cursor', 'cursor'], ['google', 'gemini-cli'],
  ]
  const evs = []
  for (let i = 0; i < 400; i++) {
    const [provider, source] = kinds[rnd(kinds.length)]
    const machine = ['', M1, M2][rnd(3)]
    const cc = ['', 'DE', 'GB', 'US'][rnd(4)]
    const base = { id: eventId('usage', provider, source, `sum${i}`), provider, source }
    if (rnd(4) === 0) {
      evs.push(at(norm(activity(`sum${i}`, { ...base, id: eventId('activity', provider, source, `sum${i}`), hasUsage: rnd(2) === 0, session: '' })), machine, cc))
      continue
    }
    const cacheW = rnd(5000)
    evs.push(at(norm(usage(`sum${i}`, {
      ...base, session: '', model: ['', 'm-a', 'm-b'][rnd(3)], q: rnd(3) ? 'exact' : 'estimated',
      in: rnd(100000), cacheW, cacheW1h: rnd(cacheW + 1), cacheR: rnd(10000000), out: rnd(50000), reasoning: rnd(1000), calls: 1 + rnd(3),
    })), machine, cc))
  }
  const r = computeRollup('2026-09-20', evs)
  assert.deepEqual(Object.keys(r.providers), ['anthropic', 'cursor', 'google', 'openai', 'xai'])
  const sum = (objs, f) => objs.reduce((s, o) => s + o[f], 0)
  for (const p of Object.values(r.providers)) {
    for (const f of ['in', 'cacheW', 'cacheR', 'out']) {
      const total = p.exact[f] + p.estimated[f]
      assert.equal(sum(Object.values(p.byMachine), f), total, `byMachine ${f}`)
      assert.equal(sum(Object.values(p.byCountry), f), total, `byCountry ${f}`)
    }
    assert.equal(sum(Object.values(p.byMachine), 'prompts'), p.prompts)
    assert.equal(sum(Object.values(p.byCountry), 'prompts'), p.prompts)
    for (const tier of ['exact', 'estimated']) {
      for (const f of ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'calls']) {
        assert.equal(sum(Object.values(p.models[tier]), f), p[tier][f], `models.${tier} ${f}`)
      }
      assert.ok(p[tier].cacheW1h <= p[tier].cacheW)
    }
    assert.ok(Object.keys(p.byCountry).every((k) => /^[A-Z]{2}$/.test(k)))
  }
})

test('a v1 rollup is served in the v2 shape until recomputed', () => {
  const v1 = {
    date: '2026-09-20',
    providers: { anthropic: { exact: { in: 1, cacheW: 2, cacheR: 3, out: 4, reasoning: 0, calls: 1, events: 1 }, estimated: { in: 0, cacheW: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 }, prompts: 3, promptsNoUsage: 1, accts: ['a_x'], models: { m: 12.3 } } },
  }
  const s = legacyShape(v1)
  assert.equal(s.v, 1)
  assert.deepEqual(s.providers.anthropic.exact, { in: 1, cacheW: 2, cacheW1h: 0, cacheR: 3, out: 4, reasoning: 0, calls: 1, events: 1 })
  assert.deepEqual(s.providers.anthropic.models, { exact: {}, estimated: {} })
  assert.deepEqual(s.providers.anthropic.byMachine, {})
  assert.deepEqual(s.providers.anthropic.byCountry, {})
  assert.equal(s.providers.anthropic.prompts, 3)
  const v2 = computeRollup('2026-09-20', [])
  assert.equal(legacyShape(v2), v2)
})

test('aliasAccts hides hashes but keeps distinct counts across days', () => {
  const a = 'a_' + '1'.repeat(16)
  const b = 'a_' + '2'.repeat(16)
  const out = aliasAccts([
    { date: 'd1', providers: { anthropic: { accts: [a, b] } } },
    { date: 'd2', providers: { openai: { accts: [b] } } },
  ])
  const s = JSON.stringify(out)
  assert.ok(!s.includes(a) && !s.includes(b))
  assert.deepEqual(out[0].providers.anthropic.accts, ['acct1', 'acct2'])
  assert.deepEqual(out[1].providers.openai.accts, ['acct2'])
})

test('validation accepts good items and rejects bad ones individually', () => {
  assert.ok(validateUsage(usage('v1'), NOW).ok)
  assert.ok(validateActivity(activity('v1'), NOW).ok)
  assert.ok(validatePrompt(prompt('v1'), NOW).ok)
  // Every source pairs with exactly one provider, cursor with itself.
  assert.deepEqual(new Set(Object.values(SOURCE_PROVIDER)), new Set(PROVIDERS))
  const k = validateUsage(cursorUsage('v1', { in: 300, out: 40 }), NOW)
  assert.ok(k.ok)
  assert.equal(k.value.provider, 'cursor')
  assert.equal(k.value.source, 'cursor')
  assert.deepEqual([k.value.in, k.value.cacheW, k.value.cacheR, k.value.out], [300, 0, 0, 40])
  assert.ok(validatePrompt(prompt('v1', { provider: 'cursor', source: 'cursor' }), NOW).ok)
  // Gemini CLI reports for google (v1.3).
  const g = validateUsage(geminiUsage('v1'), NOW)
  assert.ok(g.ok)
  assert.deepEqual([g.value.provider, g.value.source], ['google', 'gemini-cli'])
  assert.deepEqual([g.value.in, g.value.cacheW, g.value.cacheR, g.value.out, g.value.reasoning], [120, 0, 800, 60, 25])
  assert.ok(validateActivity(activity('v1', { provider: 'google', source: 'gemini-cli' }), NOW).ok)
  assert.ok(validatePrompt(prompt('v1', { provider: 'google', source: 'gemini-cli' }), NOW).ok)
  const bad = [
    usage('v2', { id: 'XYZ' }),
    usage('v2', { provider: 'openai' }),
    usage('v2', { source: 'cursor' }),
    cursorUsage('v2', { provider: 'anthropic' }),
    usage('v2', { source: 'gemini-cli' }),
    geminiUsage('v2', { provider: 'anthropic' }),
    geminiUsage('v2', { source: 'gemini' }),
    usage('v2', { source: 'nope' }),
    usage('v2', { ts: 'yesterday' }),
    usage('v2', { ts: '0001-01-01T00:00:00Z' }),
    usage('v2', { ts: '2026-10-09T00:00:00Z' }),
    usage('v2', { tzOffsetMin: 900 }),
    usage('v2', { in: -1 }),
    usage('v2', { out: 1.5 }),
    usage('v2', { acct: 'bob' }),
    usage('v2', { acctQ: 'guess' }),
    usage('v2', { q: 'maybe' }),
    usage('v2', { session: 'raw-session-id' }),
  ]
  for (const b of bad) {
    const r = validateUsage(b, NOW)
    assert.equal(r.ok, false, JSON.stringify(b))
    assert.ok(r.error)
  }
  assert.equal(validateUsage(usage('v2', { id: 'XYZ' }), NOW).id, null)
  assert.equal(validateActivity(activity('v3', { hasUsage: 'yes' }), NOW).ok, false)
  assert.equal(validatePrompt(prompt('v3', { text: 42 }), NOW).ok, false)
  // Missing acct forces acctQ unknown; missing calls defaults to 1.
  const u = usage('v4', { acct: '' })
  delete u.calls
  const r = validateUsage(u, NOW)
  assert.equal(r.value.acctQ, 'unknown')
  assert.equal(r.value.calls, 1)
  // Nanosecond timestamps from Go parse and normalise.
  assert.equal(validateUsage(usage('v5', { ts: '2026-09-20T10:00:00.123456789Z' }), NOW).value.ts, '2026-09-20T10:00:00.123Z')
  assert.equal(validateHeartbeat([], NOW).ok, false)
  assert.ok(validateHeartbeat({ machineLabel: 'x', os: 'windows' }, NOW).ok)
})

test('validation: cacheW1h defaults to 0 and is clamped to cacheW with a note', () => {
  const none = usage('c1')
  delete none.cacheW1h
  const r0 = validateUsage(none, NOW)
  assert.equal(r0.value.cacheW1h, 0)
  assert.equal(r0.value.clamped, undefined)
  assert.equal(validateUsage(usage('c2', { cacheW: 20, cacheW1h: 20 }), NOW).value.cacheW1h, 20)
  const over = validateUsage(usage('c3', { cacheW: 20, cacheW1h: 50 }), NOW)
  assert.ok(over.ok)
  assert.equal(over.value.cacheW1h, 20)
  assert.match(over.value.clamped, /cacheW1h clamped/)
  assert.equal(validateUsage(usage('c4', { cacheW1h: -1 }), NOW).ok, false)
  assert.equal(validateUsage(usage('c4', { cacheW1h: 1.5 }), NOW).ok, false)
})

test('heartbeat: public label cleaned, tz validated', () => {
  assert.equal(cleanLabel('  Studio \t\n Mac  '), 'Studio Mac')
  assert.equal(cleanLabel('a\u0000b\u0007c\u007f\u0085d'), 'abcd')
  assert.equal(cleanLabel('evil‮gnp.exe'), 'evilgnp.exe')
  assert.equal(cleanLabel('x'.repeat(100)), 'x'.repeat(48))
  assert.equal([...cleanLabel('\u{1F600}'.repeat(60))].length, 48)
  // Never ends in a separator when truncated.
  assert.equal(cleanLabel('x'.repeat(47) + ' yz'), 'x'.repeat(47))
  assert.equal(cleanLabel(42), '')
  for (const [raw, want] of [['DE', 'DE'], ['gb', 'GB'], ['', ''], ['THA', ''], ['1A', ''], [null, ''], [7, '']]) {
    assert.equal(cleanCountry(raw), want, String(raw))
  }

  const hb = (over) => validateHeartbeat({ machineLabel: 'Studio', os: 'darwin', ...over }, NOW)
  assert.equal(hb({}).value.tz, null, 'no tz: the machine row keeps its country')
  assert.deepEqual(hb({ tz: { iana: 'Europe/Berlin', windowsId: 'W. Europe Standard Time', country: 'DE', source: 'windows+region' } }).value.tz, { iana: 'Europe/Berlin', cc: 'DE' })
  assert.deepEqual(hb({ tz: { iana: 'America/Argentina/Buenos_Aires', cc: 'AR' } }).value.tz, { iana: 'America/Argentina/Buenos_Aires', cc: 'AR' })
  assert.deepEqual(hb({ tz: { iana: 'Etc/GMT+5', country: '', source: 'unknown' } }).value.tz, { iana: 'Etc/GMT+5', cc: '' })
  assert.deepEqual(hb({ tz: { iana: '<script>', country: 'Germany' } }).value.tz, { iana: '', cc: '' })
  assert.equal(hb({ tz: 'Europe/Berlin' }).ok, false)
  assert.equal(hb({ machineLabel: ' \u0001 ' }).value.machineLabel, '')
  assert.equal(hb({ machineLabel: 'y'.repeat(200) }).value.machineLabel, 'y'.repeat(48))
  assert.equal(hb({ machineLabel: 'y'.repeat(2000) }).ok, false)
})

test('heartbeat: homes is an optional list of at most 16 paths of at most 260 characters', () => {
  const hb = (over) => validateHeartbeat({ machineLabel: 'Studio', os: 'windows', ...over }, NOW)
  assert.equal(hb({}).value.homes, null, 'no homes: the machine row keeps what it had')
  assert.deepEqual(hb({ homes: [] }).value.homes, [])
  const homes = ['C:\\Users\\synthetic', '\\\\wsl$\\Synthetic-Distro\\home\\synthetic', '/synthetic/root']
  assert.deepEqual(hb({ homes }).value.homes, homes)
  assert.deepEqual(hb({ homes: Array.from({ length: 16 }, (_, i) => `/h${i}`) }).value.homes.length, 16)
  assert.equal(hb({ homes: ['x'.repeat(260)] }).ok, true)
  for (const [homes, want] of [
    [Array.from({ length: 17 }, (_, i) => `/h${i}`), /homes exceeds 16/],
    [['x'.repeat(261)], /home path too long/],
    ['/one', /homes must be an array/],
    [{ 0: '/one' }, /homes must be an array/],
    [['/ok', 7], /homes must be strings/],
    [[null], /homes must be strings/],
  ]) {
    const r = hb({ homes })
    assert.equal(r.ok, false)
    assert.match(r.error, want)
  }
  // The stored heartbeat JSON keeps the list too, and tz is independent.
  const both = hb({ homes, tz: { iana: 'Europe/Berlin', country: 'DE' } }).value
  assert.deepEqual(both.tz, { iana: 'Europe/Berlin', cc: 'DE' })
  assert.deepEqual(JSON.parse(both.json).homes, homes)
})

test('usage: days parameter and the public machine allow-list', () => {
  assert.equal(parseDays('all'), null)
  assert.equal(parseDays(undefined), 371)
  assert.equal(parseDays('junk'), 371)
  assert.equal(parseDays('30'), 30)
  assert.equal(parseDays('0'), 1)
  assert.equal(parseDays('999999'), 3700)
  const now = new Date('2026-09-29T12:00:00Z')
  const rows = [
    { rowKey: M2, label: 'B\u0007ox', cc: 'gb', os: 'darwin', enrolledAt: '2026-09-02T00:00:00Z', lastSeenAt: '2026-09-29T11:55:00Z', tokenHash: 'h', heartbeat: '{}', tzIana: 'Europe/London' },
    { rowKey: M1, label: '', cc: 'nope', enrolledAt: '2026-09-01T00:00:00Z', lastSeenAt: '2026-09-29T11:00:00Z' },
  ]
  assert.deepEqual(publicMachines(rows, now), [
    { id: M1, label: M1, cc: '', os: '', live: false, lastSeenAt: '2026-09-29T11:00:00Z', firstSeenAt: '2026-09-01T00:00:00Z' },
    { id: M2, label: 'Box', cc: 'GB', os: 'darwin', live: true, lastSeenAt: '2026-09-29T11:55:00Z', firstSeenAt: '2026-09-02T00:00:00Z' },
  ])
})

test('caps return 413 and oversize prompts are truncated server side', () => {
  assert.equal(checkCaps({ usage: new Array(201).fill({}) }).status, 413)
  assert.equal(checkCaps({ activity: new Array(501).fill({}) }).status, 413)
  assert.equal(checkCaps({ prompts: new Array(51).fill({}) }).status, 413)
  assert.equal(checkCaps({ prompts: [{ text: 'x'.repeat(300 * 1024) }, { text: 'x'.repeat(300 * 1024) }] }).status, 413)
  assert.equal(checkCaps({ usage: {} }).status, 400)
  assert.equal(checkCaps({ usage: [], prompts: [] }), null)
  const r = validatePrompt(prompt('big', { text: 'é'.repeat(200 * 1024) }), NOW)
  assert.ok(r.ok)
  assert.ok(Buffer.byteLength(r.value.text) <= 256 * 1024 + 64)
  assert.match(r.value.text, /truncated by d0m1 server/)
})

test('local date and month bucketing', () => {
  const ts = Date.parse('2026-09-28T23:30:00Z')
  assert.equal(localDate(ts, 0), '2026-09-28')
  assert.equal(localDate(ts, 60), '2026-09-29')
  assert.equal(localDate(Date.parse('2026-09-29T02:00:00Z'), -300), '2026-09-28')
  assert.equal(utcMonth(Date.parse('2026-09-30T23:59:59Z')), '2026-09')
})

test('truncateUtf8 never splits a code point', () => {
  const s = 'aé✓😀'.repeat(100)
  for (let n = 0; n < 40; n++) {
    const { text } = truncateUtf8(s, n)
    assert.ok(Buffer.byteLength(text) <= n)
    assert.ok(s.startsWith(text))
  }
})

test('owner auth decisions', () => {
  const ids = 'github:7-of-9,aad:person@example.com'
  assert.equal(ownerDecision({}, ids).status, 401)
  assert.equal(ownerDecision({ 'x-ms-client-principal': 'not base64 json' }, ids).status, 401)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('github', 'someone') }, ids).status, 403)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('github', '7-of-9') }, ids).status, 200)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('GitHub', '7-Of-9') }, ids).status, 200)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('aad', 'PERSON@example.com') }, ids).status, 200)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('twitter', 'x', ['authenticated', 'owner']) }, ids).status, 200)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('github', '7-of-9', ['anonymous']) }, ids).status, 401)
  assert.equal(ownerDecision({ 'x-ms-client-principal': principal('github', '7-of-9') }, '').status, 403)
  assert.equal(parsePrincipal({ 'x-ms-client-principal': principal('github', 'a') }).userDetails, 'a')
})

test('workspace tag: validated, kept through a merge, stored', () => {
  const tag = 'w_0123456789abcdef'
  assert.equal(validateUsage(usage('u', { ws: '/Users/me/src/app' }), NOW).ok, false)
  const v = validateUsage(usage('u', { ws: tag }), NOW)
  assert.ok(v.ok)
  assert.equal(v.value.ws, tag)
  const a = validateActivity(activity('a', { ws: tag }), NOW)
  assert.equal(a.value.ws, tag)
  const old = { ...v.value, ws: '' }
  assert.equal(mergeEvent(old, { ...v.value, kind: 'usage' }).ws, tag)
  assert.equal(storedFields({ ...a.value, kind: 'activity' }).ws, tag)
})

test('workspace aliases stay consistent across providers and days, keeping unknown separate', () => {
  const w1 = 'w_' + 'a'.repeat(16)
  const w2 = 'w_' + 'b'.repeat(16)
  const days = [
    { date: 'd1', providers: { anthropic: { accts: [], byWorkspace: { [w1]: { prompts: 1 }, [w2]: { prompts: 2 }, unknown: { prompts: 3 } } } } },
    { date: 'd2', providers: { openai: { accts: [], byWorkspace: { [w1]: { prompts: 4 } } } } },
  ]
  const before = JSON.stringify(days)
  const result = aliasAccts(days)
  assert.deepEqual(result[0].providers.anthropic.byWorkspace, { workspace1: { prompts: 1 }, workspace2: { prompts: 2 }, unknown: { prompts: 3 } })
  assert.deepEqual(result[1].providers.openai.byWorkspace, { workspace1: { prompts: 4 } })
  assert.equal(JSON.stringify(days), before)
  assert.ok(!JSON.stringify(result).includes(w1))
  assert.ok(!JSON.stringify(result).includes(w2))
})
