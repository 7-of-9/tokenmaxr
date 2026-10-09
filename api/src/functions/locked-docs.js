import { app } from '@azure/functions'
import { lowerHeaders } from '../lib/http.js'
import { createLockedDocs } from '../lib/locked-docs.js'

const locked = createLockedDocs()

const adapt = (handler) => async (request) => {
  const res = handler({
    method: request.method,
    headers: lowerHeaders(request.headers.entries()),
    params: { ...request.params },
    body: request.method === 'POST' ? await request.text() : null,
  })
  return { status: res.status, headers: res.headers, body: res.body }
}

app.http('lockedUnlock', { methods: ['POST'], authLevel: 'anonymous', route: 'locked/unlock', handler: adapt(locked.unlock) })
app.http('lockedSession', { methods: ['GET'], authLevel: 'anonymous', route: 'locked/session', handler: adapt(locked.session) })
app.http('lockedDoc', { methods: ['GET'], authLevel: 'anonymous', route: 'locked/doc/{id}', handler: adapt(locked.doc) })
