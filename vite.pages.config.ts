// Builds the tokenmaxr GitHub Pages dashboard (pages/dashboard) into pages/site, the static folder the usage
// repository's Pages workflow deploys beside its data/. The same /tokens page as d0m1.com, reading the
// published files instead of the d0m1 API: no site plugins, no API origin, relative URLs throughout, so it
// works under https://<user>.github.io/<repository>/. Run: npm run build:pages, which then runs
// scripts/sync-pages-site.mjs to stamp site/version.json and copy the build into the collector
// (collector/internal/ghpub/site), whose publisher keeps every usage repository's site/ current.
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

const here = (path: string) => fileURLToPath(new URL(path, import.meta.url))

export default defineConfig({
  root: here('./pages/dashboard'),
  base: './',
  publicDir: false,
  plugins: [react()],
  define: {
    // source.ts's d0m1 default; the Pages dashboard always provides its own source.
    __AGENTS_API_ORIGIN__: JSON.stringify(''),
    __BUILD_TIME__: JSON.stringify(new Date().toISOString()),
  },
  build: {
    outDir: here('./pages/site'),
    emptyOutDir: true,
    sourcemap: false,
    reportCompressedSize: false,
    rollupOptions: {
      output: {
        // Flag.tsx loads each country's flag on demand: one shared chunk, not one per country.
        manualChunks: (id) => (id.includes('country-flag-icons') ? 'flags' : undefined),
      },
    },
  },
})
