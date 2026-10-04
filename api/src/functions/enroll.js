import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleEnroll } from '../lib/enroll.js'

app.http('enroll', { methods: ['POST'], authLevel: 'anonymous', route: 'enroll', handler: adapt(handleEnroll) })
