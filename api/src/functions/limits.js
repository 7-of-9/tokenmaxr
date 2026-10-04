import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleLimits } from '../lib/limits.js'

// Owner only: handleLimits checks x-ms-client-principal itself.
app.http('limits', { methods: ['GET'], authLevel: 'anonymous', route: 'limits', handler: adapt(handleLimits) })
