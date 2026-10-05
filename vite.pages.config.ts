// Builds the tokenmaxr GitHub Pages dashboard (pages/dashboard) into pages/site, the static folder the usage
// repository's Pages workflow deploys beside its data/. The same /tokens and /tokens/agents pages as d0m1.com,
// in the same shell (the site-wide sheets included, no d0m1 page besides) but dressed as a page of GitHub's
// (pages/dashboard/github.css), reading the published files instead of the d0m1 API: no site plugins, no API
// origin, relative URLs throughout, so it works under https://<user>.github.io/<repository>/. Run: npm run
// build:pages, which then runs scripts/sync-pages-site.mjs to stamp site/version.json and copy the build into the
// collector (collector/internal/ghpub/site), whose publisher keeps every usage repository's site/ current.
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

const here = (path: string) => fileURLToPath(new URL(path, import.meta.url))

// TokensShell ends with d0m1.com's site footer (hidden on the token pages, but in the page): its copyright and
// personal links have no place on anyone's usage dashboard, so the shell's Footer is an empty component here.
const slashes = (path: string) => path.replace(/\\/g, '/')
const FOOTER = slashes(here('./src/components/Footer.tsx'))
const NO_FOOTER = '\0tokenmaxr:no-footer'
const noD0m1Footer = (): Plugin => ({
  name: 'tokenmaxr:no-d0m1-footer',
  enforce: 'pre',
  async resolveId(source, importer, options) {
    if (!importer || !/(^|\/)Footer(\.tsx)?$/.test(source)) return null
    const resolved = await this.resolve(source, importer, { ...options, skipSelf: true })
    return resolved && slashes(resolved.id) === FOOTER ? NO_FOOTER : null
  },
  load: id => (id === NO_FOOTER ? 'export default function Footer() { return null }' : null),
})

export const DEFAULT_RELAY = 'https://d0m1.com/api/github/device'

/** The relay base: https only (or a local harness), no trailing slash. */
function relayUrl(): string {
  const value = (process.env.TOKENMAXR_RELAY_URL || DEFAULT_RELAY).trim().replace(/\/+$/, '')
  if (!/^https:\/\/[^\s?#]+$/.test(value) && !/^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?(\/[^\s?#]*)?$/.test(value)) {
    throw new Error(`TOKENMAXR_RELAY_URL must be an https URL (got ${value})`)
  }
  return value
}

export default defineConfig({
  root: here('./pages/dashboard'),
  base: './',
  publicDir: false,
  plugins: [noD0m1Footer(), react()],
  define: {
    // source.ts's d0m1 default; the Pages dashboard always provides its own source.
    __AGENTS_API_ORIGIN__: JSON.stringify(''),
    __BUILD_TIME__: JSON.stringify(new Date().toISOString()),
    // The GitHub sign-in relay (api/src/lib/github-device.js): GitHub's device-flow endpoints send no CORS headers.
    // TOKENMAXR_RELAY_URL=<base> npm run build:pages points a build at another deployment of it.
    __TOKENMAXR_RELAY__: JSON.stringify(relayUrl()),
  },
  build: {
    outDir: here('./pages/site'),
    emptyOutDir: true,
    sourcemap: false,
    reportCompressedSize: false,
    // The collector publishes the site through GitHub's text API, so every file must be UTF-8 text: any font would
    // have to travel inside the stylesheet as a data: URL rather than as a .ttf file (the dashboard uses none now).
    assetsInlineLimit: (file) => (file.endsWith('.ttf') ? true : undefined),
    rollupOptions: {
      output: {
        // Flag.tsx loads each country's flag on demand: one shared chunk, not one per country.
        manualChunks: (id) => (id.includes('country-flag-icons') ? 'flags' : undefined),
      },
    },
  },
})
