// Resolves the agents storage connection string for local tools: the
// AGENTS_STORAGE_CONNECTION_STRING env var, else the Azure CLI. The value is
// never printed.
import { execSync } from 'node:child_process'

export function resolveConnectionString(env = process.env) {
  if (env.AGENTS_STORAGE_CONNECTION_STRING) return env.AGENTS_STORAGE_CONNECTION_STRING
  try {
    // AGENTS_STORAGE_ACCOUNT / AGENTS_RESOURCE_GROUP name your own storage account.
    const account = env.AGENTS_STORAGE_ACCOUNT
    const group = env.AGENTS_RESOURCE_GROUP
    if (!account || !group) throw new Error('set AGENTS_STORAGE_CONNECTION_STRING, or AGENTS_STORAGE_ACCOUNT and AGENTS_RESOURCE_GROUP')
    const out = execSync(`az storage account show-connection-string -n ${account} -g ${group} -o tsv`, {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 60000,
    }).trim()
    if (out) return out
  } catch {
    // Fall through to the error below without echoing CLI output.
  }
  throw new Error('No storage connection string: set AGENTS_STORAGE_CONNECTION_STRING or run `az login`.')
}
