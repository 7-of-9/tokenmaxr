import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handlePrompts } from '../lib/prompts.js'

// Owner only: handlePrompts checks x-ms-client-principal itself.
app.http('prompts', { methods: ['GET'], authLevel: 'anonymous', route: 'prompts', handler: adapt(handlePrompts) })
app.http('promptItem', { methods: ['GET'], authLevel: 'anonymous', route: 'prompts/{id}', handler: adapt(handlePrompts) })
