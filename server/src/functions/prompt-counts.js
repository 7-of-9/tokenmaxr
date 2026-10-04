import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handlePromptCounts } from '../lib/prompt-counts.js'

app.http('promptCounts', { methods: ['GET'], authLevel: 'anonymous', route: 'prompt-counts', handler: adapt(handlePromptCounts) })
