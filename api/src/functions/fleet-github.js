import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleFleetGitHub } from '../lib/fleet-github.js'

// Enrolled machines only: handleFleetGitHub checks X-D0M1-Token itself.
app.http('fleetGitHub', { methods: ['GET', 'PUT', 'DELETE'], authLevel: 'anonymous', route: 'fleet/github', handler: adapt(handleFleetGitHub) })
