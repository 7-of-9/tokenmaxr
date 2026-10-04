import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleIngest } from '../lib/ingest.js'

app.http('ingest', { methods: ['POST'], authLevel: 'anonymous', route: 'ingest', handler: adapt(handleIngest) })
