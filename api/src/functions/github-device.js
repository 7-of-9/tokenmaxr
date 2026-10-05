import { app } from '@azure/functions'
import { adapt } from '../lib/adapter.js'
import { handleGithubDevice } from '../lib/github-device.js'

// Public: the GitHub Pages dashboard's device-flow sign-in (POST /api/github/device/code, /api/github/device/token),
// relayed to GitHub with the tokenmaxor App's client id pinned. OPTIONS answers CORS preflights.
app.http('githubDevice', { methods: ['POST', 'OPTIONS'], authLevel: 'anonymous', route: 'github/device/{step}', handler: adapt(handleGithubDevice) })
