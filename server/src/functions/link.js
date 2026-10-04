import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleLink } from '../lib/link.js'

// Owner only: handleLink checks x-ms-client-principal itself.
app.http('link', { methods: ['POST'], authLevel: 'anonymous', route: 'link', handler: adapt(handleLink) })
