import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleUsage } from '../lib/usage.js'
import { withOptionalBrowserSession } from '../lib/browser-session.js'

app.http('usage', { methods: ['GET'], authLevel: 'anonymous', route: 'usage', handler: adapt(withOptionalBrowserSession(handleUsage)) })
