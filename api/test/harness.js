#!/usr/bin/env node
// Local API harness: serves the pure handlers at http://127.0.0.1:7071/api/*.
//
//   AGENTS_TABLE_PREFIX=tdev node test/harness.js   isolated Azure tables
//   node test/harness.js --memory                    in-memory store, no Azure
//   node test/harness.js --production                real tables (explicit only)
//
// Unlike SWA, the harness trusts an x-ms-client-principal header sent by the
// caller, so owner routes can be exercised locally.
import http from 'node:http'
import { fileURLToPath } from 'node:url'
import { runHandler } from '../src/lib/http.js'
import { azureStore, memoryStore } from '../src/lib/tables.js'
import { createInvite, handleEnroll, handleInvite } from '../src/lib/enroll.js'
import { handleIngest } from '../src/lib/ingest.js'
import { handleUsage } from '../src/lib/usage.js'
import { handlePrompts } from '../src/lib/prompts.js'
import { handlePromptCounts } from '../src/lib/prompt-counts.js'
import { handleLimits } from '../src/lib/limits.js'
import { handleLink } from '../src/lib/link.js'
import { handleFleetGitHub } from '../src/lib/fleet-github.js'
import { resolveConnectionString } from '../scripts/storage-env.js'

const ROUTES = [
  ['POST', /^\/api\/enroll$/, handleEnroll],
  ['POST', /^\/api\/invite$/, handleInvite],
  ['POST', /^\/api\/link$/, handleLink],
  ['POST', /^\/api\/ingest$/, handleIngest],
  ['GET', /^\/api\/usage$/, handleUsage],
  ['GET', /^\/api\/prompt-counts$/, handlePromptCounts],
  ['GET', /^\/api\/limits$/, handleLimits],
  ['GET', /^\/api\/prompts$/, handlePrompts],
  ['GET', /^\/api\/prompts\/([^/]+)$/, handlePrompts, 'id'],
  ['GET,PUT,DELETE', /^\/api\/fleet\/github$/, handleFleetGitHub],
]

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.on('error', reject)
  })
}

// ctx: { store, env } handed to every handler.
export function createHarness(ctx) {
  return http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1')
    let status = 404
    let headers = { 'Content-Type': 'application/json' }
    let payload = { ok: false, error: 'not found' }
    const route = ROUTES.find(([, re]) => re.test(url.pathname))
    if (route && !route[0].split(',').includes(req.method)) {
      status = 405
      payload = { ok: false, error: 'method not allowed' }
    } else if (route) {
      const [, re, handler, param] = route
      const m = url.pathname.match(re)
      const out = await runHandler(handler, {
        method: req.method,
        headers: Object.fromEntries(Object.entries(req.headers).map(([k, v]) => [k, Array.isArray(v) ? v.join(',') : v])),
        query: Object.fromEntries(url.searchParams),
        params: param ? { [param]: decodeURIComponent(m[1]) } : {},
        readBody: () => readBody(req),
      }, ctx)
      status = out.status
      headers = { ...headers, ...out.headers }
      payload = out.jsonBody
    }
    res.writeHead(status, headers)
    res.end(JSON.stringify(payload))
    // Method, path and status only: never bodies.
    if (!ctx.quiet) console.log(`${req.method} ${url.pathname} -> ${status}`)
  })
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const args = process.argv.slice(2)
  const env = { ...process.env }
  let store
  if (args.includes('--memory')) {
    store = memoryStore()
    env.PROMPT_ENC_KEY ||= Buffer.alloc(32, 7).toString('base64')
  } else {
    const prefix = env.AGENTS_TABLE_PREFIX || ''
    if (!prefix && !args.includes('--production')) {
      console.error('Refusing to use production tables: set AGENTS_TABLE_PREFIX, or pass --memory or --production.')
      process.exit(1)
    }
    store = azureStore({ connectionString: resolveConnectionString(env), prefix })
  }
  const port = Number(env.AGENTS_HARNESS_PORT || 7071)
  createHarness({ store, env }).listen(port, '127.0.0.1', async () => {
    console.log(`agents API harness on http://127.0.0.1:${port}/api (${store.kind}${store.prefix ? `, prefix ${store.prefix}` : ''})`)
    if (store.kind === 'memory') {
      const { invite } = await createInvite(store, new Date(), 'harness')
      console.log(`bootstrap invite (15 min): ${invite}`)
    }
  })
}
