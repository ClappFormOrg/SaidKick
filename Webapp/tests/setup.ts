import { ref, computed, reactive, watch, watchEffect, onMounted, onUnmounted } from 'vue'
import { vi } from 'vitest'

// Provide Vue reactivity primitives that Nuxt auto-imports at build time.
vi.stubGlobal('ref', ref)
vi.stubGlobal('computed', computed)
vi.stubGlobal('reactive', reactive)
vi.stubGlobal('watch', watch)
vi.stubGlobal('watchEffect', watchEffect)
vi.stubGlobal('onMounted', onMounted)
vi.stubGlobal('onUnmounted', onUnmounted)

// Nuxt state / routing globals
vi.stubGlobal('useState', (_key: string, init?: () => unknown) => ref(init?.() ?? null))
vi.stubGlobal('useRuntimeConfig', () => ({
  public: { apiBase: 'http://localhost:8080' },
}))
vi.stubGlobal('navigateTo', vi.fn())
// Not hydrating by default; tests that cover the hydration path override this.
vi.stubGlobal('useNuxtApp', () => ({ isHydrating: false, hooks: { hookOnce: vi.fn() } }))
vi.stubGlobal('$fetch', vi.fn())
vi.stubGlobal('useAsyncData', vi.fn())

// useApi is auto-imported by Nuxt; provide a minimal stub. Its clients merge
// their defaults into the options like $fetch.create does, then call whatever
// $fetch the test has stubbed, so tests can assert on the final request.
vi.stubGlobal('useApi', () => {
  const baseURL = 'http://localhost:8080'
  const client = (defaults: Record<string, unknown>) =>
    (url: string, opts: Record<string, unknown> = {}) =>
      (globalThis as unknown as { $fetch: (u: string, o: unknown) => unknown }).$fetch(url, { ...defaults, ...opts })
  return {
    baseURL,
    api: client({ baseURL, credentials: 'include' }),
    publicApi: client({ baseURL }),
  }
})
// defineNuxtRouteMiddleware just returns its callback so we can call it directly.
vi.stubGlobal('defineNuxtRouteMiddleware', (fn: unknown) => fn)

// useBranding is auto-imported too; the real composable runs on the stubs
// above (useState, useApi, $fetch).
const { useBranding } = await import('~/composables/useBranding')
vi.stubGlobal('useBranding', useBranding)
