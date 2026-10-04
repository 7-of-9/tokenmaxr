#!/usr/bin/env node
// Bootstrap: writes a single-use, 15-minute invite straight into the invites
// table and prints it. The first machine installs with --join D0M1-<invite>
// (no K part); the collector then generates the fleet key K and later
// machines join with the code printed by `tokenmaxr invite`.
//
//   node api/scripts/create-invite.js
//   AGENTS_TABLE_PREFIX=tdev node api/scripts/create-invite.js   (test tables)
import { azureStore } from '../src/lib/tables.js'
import { createInvite } from '../src/lib/enroll.js'
import { resolveConnectionString } from './storage-env.js'

try {
  const prefix = process.env.AGENTS_TABLE_PREFIX || ''
  const store = azureStore({ connectionString: resolveConnectionString(), prefix })
  const { invite, expiresAt } = await createInvite(store, new Date(), 'bootstrap')
  console.log(`Invite:      ${invite}`)
  console.log(`Expires at:  ${expiresAt} (single use, 15 minutes)`)
  if (prefix) console.log(`Tables:      prefix "${prefix}" (test data, not production)`)
  console.log('')
  console.log('First machine (no fleet key yet):')
  console.log(`  tokenmaxr install --join D0M1-${invite}`)
  console.log('')
  console.log('Join code format: D0M1-<invite>[-<base32(K) without padding>]')
  console.log('Later machines use the full code from `tokenmaxr invite` on an enrolled machine.')
} catch (err) {
  console.error(`create-invite failed: ${err.message}`)
  process.exit(1)
}
