import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleBrowserSession } from '../lib/browser-session.js'

app.http('browserSession', { methods: ['POST'], authLevel: 'anonymous', route: 'session', handler: adapt(handleBrowserSession) })
app.http('browserLogout', { methods: ['GET'], authLevel: 'anonymous', route: 'session/{action:regex(^logout$)}', handler: adapt(handleBrowserSession) })
