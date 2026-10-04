import assert from 'node:assert/strict'
import { readUsageResponse, type UsageTransfer } from './usageResponse.ts'
import { readUsageCache, writeUsageCache } from './usageCache.ts'
import type { UsageResponse } from './types.ts'

const data: UsageResponse = { generatedAt: '2026-10-02T05:00:00Z', lastIngestAt: null, days: [],
  machines: [{ id: 'test', label: 'Café', cc: 'TH', os: 'test', live: true, lastSeenAt: null }],
  totals: { accounts: 0, accountsByProvider: {}, machines: 1, machinesLive: 1 } }
const bytes = new TextEncoder().encode(JSON.stringify(data))
const stream = () => new ReadableStream<Uint8Array>({ start(controller) {
  // Splitting every byte also splits UTF-8 characters across chunks.
  for (const byte of bytes) controller.enqueue(Uint8Array.of(byte))
  controller.close()
} })
for (const encoding of [null, 'br']) {
  const progress: UsageTransfer[] = []
  const headers: Record<string, string> = { 'content-length': String(bytes.length) }
  if (encoding) headers['content-encoding'] = encoding
  assert.deepEqual(await readUsageResponse(new Response(stream(), { headers }), p => progress.push(p)), data)
  assert.equal(progress[0].total, encoding ? null : bytes.length, 'never compare decoded bytes to a compressed length')
  assert.equal(progress.at(-1)!.received, bytes.length)
  assert.equal(progress.at(-1)!.phase, 'processing')
}
await assert.rejects(readUsageResponse(new Response('{invalid'), () => {}), SyntaxError)
await assert.rejects(readUsageResponse(new Response(new ReadableStream({start(c) { c.error(new Error('download failed')) }})), () => {}), /download failed/)

const values = new Map<string, string>()
const storage = { getItem: (k: string) => values.get(k) ?? null, setItem: (k: string, v: string) => { values.set(k, v) } }
const now = Date.now()
writeUsageCache(storage, 'https://d0m1.com', { data, loadedAt: now })
assert.deepEqual(readUsageCache(storage, 'https://d0m1.com', now), { data, loadedAt: now })
assert.equal(readUsageCache(storage, 'http://localhost:7094', now), null, 'production and local data never share a cache')
assert.equal(readUsageCache(storage, 'https://d0m1.com', now + 25 * 3600000), null)
const blocked = { getItem() { throw new Error('disabled') }, setItem() { throw new Error('quota') } } as unknown as Storage
assert.equal(readUsageCache(blocked, 'https://d0m1.com'), null)
assert.doesNotThrow(() => writeUsageCache(blocked, 'https://d0m1.com', { data, loadedAt: now }))
console.log('usage download progress and isolated snapshots: ok')
