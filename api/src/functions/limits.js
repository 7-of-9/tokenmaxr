import { withBrowserSession } from '../lib/browser-session.js'
import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleLimits } from '../lib/limits.js'
import { handleLimitProjects } from '../lib/limit-projects.js'

// Owner only: handleLimits checks x-ms-client-principal itself.
app.http('limits', { methods: ['GET'], authLevel: 'anonymous', route: 'limits', handler: adapt(withBrowserSession(handleLimits)) })
app.http('limitProjects', { methods: ['GET'], authLevel: 'anonymous', route: 'limits/{id}/projects', handler: adapt(withBrowserSession(handleLimitProjects)) })
