import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const __dirname = fileURLToPath(new URL('.', import.meta.url))

// Vite's `define` does NOT reliably replace arbitrary `import.meta.*` flags
// (only import.meta.env.*), so the Nuxt client/server guards read `undefined`
// under Vitest. This tiny transform makes the app behave as the client build
// during tests: import.meta.client → true, import.meta.server → false.
const nuxtImportMetaFlags = {
  name: 'test-nuxt-import-meta-flags',
  enforce: 'post' as const,
  transform(code: string, id: string) {
    if (id.includes('node_modules')) return null
    if (!/\.(?:ts|js|vue)(?:\?|$)/.test(id)) return null
    if (!code.includes('import.meta.client') && !code.includes('import.meta.server')) return null
    return {
      code: code
        .replace(/import\.meta\.client/g, 'true')
        .replace(/import\.meta\.server/g, 'false'),
      map: null,
    }
  },
}

export default defineConfig({
  plugins: [vue(), nuxtImportMetaFlags],
  test: {
    globals: true,
    environment: 'happy-dom',
    include: ['tests/**/*.test.ts'],
    setupFiles: ['./tests/setup.ts'],
    clearMocks: true,
    coverage: {
      provider: 'v8',
      include: [
        'composables/**/*.ts',
        'utils/**/*.ts',
        'middleware/**/*.ts',
        'components/**/*.vue',
      ],
    },
  },
  resolve: {
    alias: {
      '~': resolve(__dirname),
      '@': resolve(__dirname),
    },
  },
})
