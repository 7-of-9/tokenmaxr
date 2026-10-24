/*
 * tokenmaxr Claude Web capture: the relay (content script, isolated world).
 * It passes records from main-world.js to the service worker and passes the
 * collector's settings and sweep requests back. Messages cross the page
 * boundary as JSON strings only.
 *
 * Any script in the page can send these events, so the relay takes only
 * what main-world.js would send: arrays of record-shaped objects, live
 * turns and claims at a human rate (RATE per minute), swept records and one
 * sweep result only while a sweep it started is running. The service worker
 * and the host validate every field again and bound the numbers; a forged
 * record can still add an estimate (SPEC "Claude chat").
 */
(function () {
  'use strict'
  var EV_RECORDS = '__tokenmaxr:records'
  var EV_CONFIG = '__tokenmaxr:config'
  var EV_SWEEP = '__tokenmaxr:sweep'
  var EV_SWEEP_DONE = '__tokenmaxr:sweep-done'
  var MAX_DETAIL = 8 << 20
  var MAX_RECORDS = 500
  var RATE = 60 // live records and claims per minute (a turn sends one of each)
  var SWEEP_LEASE = 15 * 60 * 1000

  var sweepUntil = 0
  var recent = [] // times of accepted live records and claims, last minute

  function send(msg) {
    return new Promise(function (resolve) {
      try {
        chrome.runtime.sendMessage(msg, function (reply) { void chrome.runtime.lastError; resolve(reply || null) })
      } catch (e) { resolve(null) } // the extension was reloaded: this page keeps working
    })
  }

  function parse(detail) {
    if (typeof detail !== 'string' || detail.length > MAX_DETAIL) return null
    try { return JSON.parse(detail) } catch (e) { return null }
  }

  // accept keeps the records main-world.js could have sent now.
  function accept(records, now) {
    var out = []
    var sweeping = now < sweepUntil
    recent = recent.filter(function (t) { return now - t < 60000 })
    for (var i = 0; i < records.length; i++) {
      var r = records[i]
      if (!r || typeof r !== 'object' || Array.isArray(r) || typeof r.assistant !== 'string') continue
      if (r.kind === 'sweep') {
        if (sweeping) out.push(r)
      } else if (r.kind === 'live' || r.kind === 'claim') {
        if (recent.length < RATE) { recent.push(now); out.push(r) }
      }
    }
    return out
  }

  window.addEventListener(EV_RECORDS, function (e) {
    var records = parse(e.detail)
    if (!Array.isArray(records) || !records.length || records.length > MAX_RECORDS) return
    records = accept(records, Date.now())
    if (records.length) send({ type: 'records', records: records })
  })

  window.addEventListener(EV_SWEEP_DONE, function (e) {
    if (Date.now() >= sweepUntil) return // no sweep of ours is running
    var out = parse(e.detail)
    if (!out || typeof out !== 'object' || Array.isArray(out)) return
    sweepUntil = 0
    send({
      type: 'sweep-done',
      result: {
        ok: out.ok === true, cursor: typeof out.cursor === 'string' ? out.cursor.slice(0, 40) : '', more: out.more === true,
        conversations: Number(out.conversations) || 0, records: Number(out.records) || 0,
      },
    })
  })

  function pushConfig() {
    return send({ type: 'config' }).then(function (cfg) {
      var c = { enabled: !!(cfg && cfg.enabled), prompts: !!(cfg && cfg.prompts), sweepPrompts: !!(cfg && cfg.sweepPrompts) }
      window.dispatchEvent(new CustomEvent(EV_CONFIG, { detail: JSON.stringify(c) }))
    })
  }

  function maybeSweep() {
    if (Date.now() < sweepUntil) return Promise.resolve()
    return send({ type: 'sweep-due' }).then(function (s) {
      if (!s || !s.due) return
      sweepUntil = Date.now() + SWEEP_LEASE
      window.dispatchEvent(new CustomEvent(EV_SWEEP, { detail: JSON.stringify(s) }))
    })
  }

  pushConfig()
  setInterval(pushConfig, 10 * 60 * 1000)
  setTimeout(maybeSweep, 30 * 1000)
  setInterval(maybeSweep, 5 * 60 * 1000)
})()
