import type { ModuleOptions } from 'nuxt-security'
import {
  apiOrigin, contentSecurityPolicy, cspApiBase, serializeCsp,
  PERMISSIONS_POLICY, REFERRER_POLICY, STRICT_TRANSPORT_SECURITY,
  X_CONTENT_TYPE_OPTIONS, X_FRAME_OPTIONS,
} from './utils/securityHeaders'

type SecurityHeaders = NonNullable<Exclude<ModuleOptions['headers'], false>>

const dev = process.env.NODE_ENV === 'development'
const devOrigin = `http://localhost:${process.env.NUXT_PORT || process.env.PORT || 3000}`
// connect-src is fixed at build time. A cross-origin API therefore needs
// API_BASE_URL (or NUXT_PUBLIC_API_BASE) set when the image is built.
const apiBase = cspApiBase(process.env, dev)

const csp = serializeCsp(contentSecurityPolicy({
  apiOrigin: apiOrigin(apiBase, dev ? devOrigin : undefined),
  dev,
  // The dev server and Vite's HMR socket.
  devOrigins: ['http://localhost:*', 'ws://localhost:*'],
}))

export default defineNuxtConfig({
  devtools: { enabled: false },
  routeRules: {
    // The dashboard is signed-in only: its auth middleware and every data
    // fetch run on the client (server: false), so server HTML could only ever
    // show an anonymous shell that hydration then contradicts. Render it on
    // the client instead.
    '/dashboard': { ssr: false },
    '/dashboard/**': { ssr: false },
  },
  modules: ['@nuxtjs/tailwindcss', 'nuxt-security'],
  // nuxt-security owns every security response header. Its other features
  // are off so the module changes nothing else. HTML must never be cached
  // (server/plugins/htmlNoStore.ts): a cached page would reuse its nonce.
  security: {
    nonce: true,
    headers: {
      // The policy and Permissions-Policy go in as header strings so the
      // module emits them as written instead of merging them with its own
      // defaults. Its runtime accepts the string form.
      contentSecurityPolicy: csp as unknown as SecurityHeaders['contentSecurityPolicy'],
      permissionsPolicy: PERMISSIONS_POLICY as unknown as SecurityHeaders['permissionsPolicy'],
      xFrameOptions: X_FRAME_OPTIONS,
      xContentTypeOptions: X_CONTENT_TYPE_OPTIONS,
      referrerPolicy: REFERRER_POLICY,
      strictTransportSecurity: { ...STRICT_TRANSPORT_SECURITY },
      crossOriginEmbedderPolicy: false,
      crossOriginOpenerPolicy: false,
      crossOriginResourcePolicy: false,
      originAgentCluster: false,
      xDNSPrefetchControl: false,
      xDownloadOptions: false,
      xPermittedCrossDomainPolicies: false,
      xXSSProtection: false,
    },
    rateLimiter: false,
    requestSizeLimiter: false,
    xssValidator: false,
    corsHandler: false,
    allowedMethodsRestricter: false,
    hidePoweredBy: true,
    // Not in the issue's list, but also off so behaviour does not change by
    // default: SRI rewrites asset tags and removeLoggers strips console calls.
    sri: false,
    removeLoggers: false,
  },
  tailwindcss: {
    // The config viewer defaults to on in dev and imports its server during
    // boot, while Vite is scanning. Nothing here uses /_tailwind.
    viewer: false,
  },
  css: ['~/assets/css/govnl.css'],
  app: {
    head: {
      htmlAttrs: { lang: 'nl' },
      titleTemplate: '%s — sAId-Kick Appstore',
    },
  },
  runtimeConfig: {
    // Server-only API origin for the brand fetched during SSR and the
    // /branding/logo proxy (#158). Set at runtime with NUXT_API_BASE_INTERNAL
    // or API_BASE_INTERNAL_URL (utils/branding.ts internalApiBase); empty
    // falls back to public.apiBase.
    apiBaseInternal: '',
    public: {
      apiBase: process.env.API_BASE_URL || 'http://localhost:8080',
    },
  },
  typescript: {
    strict: true,
    tsConfig: {
      compilerOptions: {
        // noUncheckedIndexedAccess is not part of our strictness contract; newer
        // nuxt versions enable it under strict:true but it produces too many
        // false positives across the codebase. Keep it off explicitly so the
        // generated .nuxt/tsconfig.json is stable across nuxt upgrades.
        noUncheckedIndexedAccess: false,
        // Newer nuxt generates types:[] which drops @types/node, causing
        // `process` (nuxt.config.ts) and `node:*` imports (vitest.config.ts)
        // to be unrecognised. Pin node here so it is always included.
        types: ['node'],
      },
    },
  },
  ssr: true,
  vite: {
    build: {
      // Small font files would otherwise be inlined as data: URIs, which the
      // CSP's font-src 'self' blocks. Other assets keep Vite's default.
      assetsInlineLimit: (file: string) => (/\.(?:woff2?|ttf|otf|eot)$/i.test(file) ? false : undefined),
    },
    // No optimizeDeps.include for @tabler/icons-vue: pre-bundling the barrel
    // made esbuild process every icon module on each cold start. Icons are
    // imported one at a time from ~/lib/icons, which Vite discovers during
    // its entry crawl. Do not add a glob such as
    // '@tabler/icons-vue/dist/esm/icons/*.mjs' either: Vite expands include
    // globs against the filesystem, which would pull in every icon again.
  },
})
