/*
 * tokenmaxr Claude Web capture: the service worker. It queues records from
 * claude.ai tabs and hands them to tokenmaxr through Chrome native messaging
 * (no network, no listener: Chrome starts the host named in config.json,
 * which only accepts this extension's pinned id). It keeps the collector's
 * settings (prompt capture, sweeper) and schedules the opt-in history sweep.
 *
 * Every change to the queue, the sent list and the sweep state runs through
 * one chain (serial), so a record arriving while a batch is delivered is
 * never overwritten. Delivered records leave the queue by key, swept turns
 * join the sent list only once the host has answered for them, and nothing
 * queued is dropped (see mergeQueue).
 *
 * Prompt text is kept in the queue for TEXT_TTL at most (expireText), and a
 * message to the host is capped by bytes as well as records: a message the
 * host still finds too large is split, and a single record that is too
 * large loses its text (shrinkRecord), so the queue never stalls.
 */
import {
  decideSweep, expireText, finishSweep, mergeQueue, pickBatch, sanitize, settleQueue, shrinkRecord, withoutText,
} from './queue.js'

const VERSION = chrome.runtime.getManifest().version
const DEFAULT_HOST = 'com.d0m1.tokenmaxr'
const CONFIG_TTL = 10 * 60 * 1000
const BATCH = 200

let hostName = null
let flushing = null
let chain = Promise.resolve()

// serial runs fn after every change queued before it, and returns its result.
function serial(fn) {
  const run = chain.then(fn)
  chain = run.catch(() => {})
  return run
}

async function host() {
  if (hostName) return hostName
  try {
    const r = await fetch(chrome.runtime.getURL('config.json'))
    const c = await r.json()
    if (typeof c.host === 'string' && /^[a-z0-9_.]+$/.test(c.host)) hostName = c.host
  } catch { /* use the default */ }
  return (hostName ||= DEFAULT_HOST)
}

async function native(msg) {
  const name = await host()
  return new Promise((resolve) => {
    try {
      chrome.runtime.sendNativeMessage(name, msg, (reply) => {
        const err = chrome.runtime.lastError
        resolve(err ? { ok: false, transport: true, error: String(err.message || err) } : reply || { ok: false })
      })
    } catch (e) {
      resolve({ ok: false, transport: true, error: String(e) })
    }
  })
}

const get = async (k, d) => (await chrome.storage.local.get(k))[k] ?? d
const set = (o) => chrome.storage.local.set(o)

function keep(reply) {
  if (!reply || typeof reply !== 'object' || reply.transport || !('prompts' in reply)) return null
  return {
    prompts: reply.prompts === true, sweepPrompts: reply.sweepPrompts === true,
    sweeper: reply.sweeper === true, inferApps: reply.inferApps !== false,
    settleMinutes: Number(reply.settleMinutes) || 0, t0: String(reply.t0 || ''), version: String(reply.version || ''),
    at: Date.now(),
  }
}

async function config(force = false) {
  const cfg = await get('cfg', null)
  if (!force && cfg && Date.now() - cfg.at < CONFIG_TTL) return cfg
  const fresh = keep(await native({ type: 'hello', ext: VERSION }))
  if (fresh) {
    await set({ cfg: fresh, hostError: '' })
    return fresh
  }
  await set({ hostError: 'tokenmaxr did not answer' })
  return cfg
}

// queueNow is the queue with expired prompt text removed (and saved).
async function queueNow() {
  const q = await get('queue', [])
  const x = expireText(q, Date.now())
  if (x.changed) await set({ queue: x.queue })
  return x.queue
}

async function flush() {
  if (flushing) return flushing
  flushing = (async () => {
    let limit = BATCH
    for (let i = 0; i < 40; i++) {
      const batch = await serial(async () => pickBatch(await queueNow(), limit))
      if (!batch.length) return
      // The host is called outside the chain: records keep arriving meanwhile.
      const reply = await native({ type: 'records', ext: VERSION, records: batch })
      if (reply && reply.tooLarge) {
        // Split the batch; a single record that is still too large loses
        // its text, or is dropped when it has none.
        if (batch.length > 1) limit = Math.max(1, Math.floor(batch.length / 2))
        else await serial(async () => set({ queue: shrinkRecord(await get('queue', []), batch[0]) }))
        continue
      }
      limit = BATCH
      if (!reply || !reply.ok) {
        await set({ hostError: String((reply && reply.error) || 'tokenmaxr did not answer').slice(0, 120) })
        chrome.alarms.create('flush', { delayInMinutes: 5 })
        return
      }
      const fresh = keep(reply)
      // Accepted or rejected, the host has answered for these: they leave
      // the queue by key, and swept ones join the sent list.
      await serial(async () => {
        const done = settleQueue(await get('queue', []), batch, await get('sentSweep', []))
        await set({ queue: done.queue, sentSweep: done.sent, hostError: '', lastSent: Date.now(), ...(fresh ? { cfg: fresh } : {}) })
      })
    }
  })().finally(() => { flushing = null })
  return flushing
}

function onRecords(records) {
  return serial(async () => {
    if (await get('disabled', false)) return { ok: false }
    const cfg = await config()
    const clean = sanitize(records, { prompts: !!(cfg && cfg.prompts), sweepPrompts: !!(cfg && cfg.sweepPrompts), ext: VERSION })
    if (!clean.length) return { ok: true, queued: 0 }
    const merged = mergeQueue(await queueNow(), clean, await get('sentSweep', []), Date.now())
    const update = { queue: merged.queue }
    if (merged.refused && clean.some((r) => r.kind === 'sweep')) {
      // The running sweep must not move its cursor past what was refused.
      update.sweep = { ...(await get('sweep', {})), refused: true }
    }
    try {
      await set(update)
    } catch {
      // Storage is full: keep every record, without prompt text.
      update.queue = withoutText(update.queue)
      await set(update)
    }
    return { ok: true, queued: merged.added, refused: merged.refused }
  }).then((r) => { flush(); return r })
}

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  // Only this extension's own content scripts on claude.ai.
  if (sender.id !== chrome.runtime.id || !sender.url || !/^https:\/\/claude\.ai\//.test(sender.url) || !msg) return
  const run = async () => {
    switch (msg.type) {
      case 'config': {
        const disabled = await get('disabled', false)
        const cfg = await config()
        return { enabled: !disabled && !!cfg, prompts: !!(cfg && cfg.prompts), sweepPrompts: !!(cfg && cfg.sweepPrompts) }
      }
      case 'records':
        return Array.isArray(msg.records) ? onRecords(msg.records) : { ok: false }
      case 'sweep-due': {
        if (await get('disabled', false)) return { due: false }
        const cfg = await config()
        return serial(async () => {
          const d = decideSweep(await get('sweep', {}), cfg, Date.now(), await get('sweepOnly', ''), (await get('queue', [])).length)
          if (d.due) await set({ sweep: d.state })
          return d.reply
        })
      }
      case 'sweep-done':
        return serial(async () => {
          await set({ sweep: finishSweep(await get('sweep', {}), msg.result, Date.now()) })
          return { ok: true }
        })
    }
    return null
  }
  run().then(reply, () => reply(null))
  return true
})

chrome.alarms.onAlarm.addListener((a) => { if (a.name === 'flush') flush() })
chrome.runtime.onInstalled.addListener(() => {
  chrome.alarms.create('flush', { periodInMinutes: 5 })
  config(true)
})
chrome.runtime.onStartup.addListener(() => { config(true); flush() })
