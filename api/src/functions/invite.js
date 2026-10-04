import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleInvite } from '../lib/enroll.js'

app.http('invite', { methods: ['POST'], authLevel: 'anonymous', route: 'invite', handler: adapt(handleInvite) })
