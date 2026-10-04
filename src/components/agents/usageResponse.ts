import type { UsageResponse } from './types'

export interface UsageTransfer {
  phase: 'waiting' | 'downloading' | 'processing'
  received: number
  total: number | null
}

/** Fetch exposes decoded bytes, so a compressed Content-Length cannot be used as a percentage. */
export async function readUsageResponse(response: Response, update: (progress: UsageTransfer) => void): Promise<UsageResponse> {
  const length = Number(response.headers.get('content-length'))
  const encoding = response.headers.get('content-encoding')
  const total = (!encoding || encoding === 'identity') && Number.isFinite(length) && length > 0 ? length : null
  update({ phase: 'downloading', received: 0, total })
  if (!response.body) return response.json() as Promise<UsageResponse>
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let text = ''
  let received = 0
  let lastUpdate = 0
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      received += value.byteLength
      text += decoder.decode(value, { stream: true })
      const now = Date.now()
      if (now - lastUpdate >= 100) {
        update({ phase: 'downloading', received, total })
        lastUpdate = now
      }
    }
    text += decoder.decode()
    update({ phase: 'processing', received, total })
    return JSON.parse(text) as UsageResponse
  } finally {
    reader.releaseLock()
  }
}
