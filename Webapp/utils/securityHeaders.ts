// Security response headers for the webapp. nuxt-security emits them (see
// the `security` block in nuxt.config.ts); this file is the single place the
// values and the Content-Security-Policy directives are defined. The API sets
// its own headers in SecurityHeadersMiddleware.
//
// The policy is nonce based: nuxt-security generates a fresh nonce per
// response, adds it to every <script>, <style> and <link> element the server
// renders and substitutes it for NONCE_SOURCE in the header. Under
// 'strict-dynamic' a script without the nonce is blocked even when it is
// same-origin, and scripts loaded by a nonced script are trusted.

// nuxt-security replaces this placeholder with the per-response nonce.
export const NONCE_SOURCE = "'nonce-{{nonce}}'"

// Catalogue app descriptions and screenshots (pages/catalogus/[slug].vue).
const GITHUB_RAW = 'https://raw.githubusercontent.com'

// A directive's sources, or true for a directive that takes none.
export type CspDirectives = Readonly<Record<string, readonly string[] | true>>

export interface CspOptions {
  // Origin of the API when the browser reaches it cross-origin; null when it
  // is served from the webapp's own origin.
  apiOrigin: string | null
  // `nuxt dev`: plain HTTP, plus the dev server and its HMR socket.
  dev?: boolean
  // Extra connect-src origins for the dev server.
  devOrigins?: readonly string[]
}

export function contentSecurityPolicy({ apiOrigin, dev = false, devOrigins = [] }: CspOptions): CspDirectives {
  const connect = ["'self'", GITHUB_RAW]
  if (apiOrigin) connect.push(apiOrigin)
  if (dev) connect.push(...devOrigins)

  const directives: Record<string, readonly string[] | true> = {
    'default-src': ["'self'"],
    'script-src': ["'self'", NONCE_SOURCE, "'strict-dynamic'"],
    'style-src': ["'self'", NONCE_SOURCE],
    // Vue :style bindings (GovnlButton's CSS custom properties). Style
    // attributes cannot run script.
    'style-src-attr': ["'unsafe-inline'"],
    'img-src': ["'self'", 'data:', GITHUB_RAW],
    'font-src': ["'self'"],
    'connect-src': connect,
    'frame-ancestors': ["'none'"],
    'base-uri': ["'self'"],
    'object-src': ["'none'"],
    'form-action': ["'self'"],
  }
  // The dev server runs on plain HTTP.
  if (!dev) directives['upgrade-insecure-requests'] = true
  return directives
}

// Header form of the directives, in the order given. nuxt-security accepts
// the policy as this string; an object would be merged with the module's
// default policy, which reorders the directives and adds its own.
export function serializeCsp(directives: CspDirectives): string {
  return Object.entries(directives)
    .map(([name, sources]) => (sources === true ? name : `${name} ${sources.join(' ')}`))
    .join('; ')
}

// The origin of the API base URL, or null when the browser reaches the API
// on the webapp's own origin: an empty or relative base, or one whose origin
// equals appOrigin.
export function apiOrigin(apiBase: string, appOrigin?: string): string | null {
  if (!apiBase) return null
  let origin: string
  try {
    origin = new URL(apiBase).origin
  } catch {
    return null
  }
  if (origin === 'null') return null
  return appOrigin && origin === new URL(appOrigin).origin ? null : origin
}

// The API base the CSP is built for, read at build time. A production build
// with neither variable set gets no API origin: a same-origin API is covered
// by 'self'. Dev falls back to the local API the runtime config defaults to.
export function cspApiBase(env: Readonly<Record<string, string | undefined>>, dev: boolean): string {
  return env.NUXT_PUBLIC_API_BASE ?? env.API_BASE_URL ?? (dev ? 'http://localhost:8080' : '')
}

// The other security headers, in nuxt-security's option names.
export const X_FRAME_OPTIONS = 'DENY'
export const X_CONTENT_TYPE_OPTIONS = 'nosniff'
export const REFERRER_POLICY = 'strict-origin-when-cross-origin'
// Browsers ignore HSTS over plain HTTP, so local dev is unaffected.
export const STRICT_TRANSPORT_SECURITY = { maxAge: 31536000, includeSubdomains: true } as const
export const PERMISSIONS_POLICY = 'geolocation=(), camera=(), microphone=()'

// Every response header nuxt-security emits for an HTML page, by header name,
// with the nonce left as its placeholder. Tests and the CSP crawl compare
// against this.
export function securityHeaders(csp: CspOptions): Readonly<Record<string, string>> {
  return {
    'Content-Security-Policy': serializeCsp(contentSecurityPolicy(csp)),
    'X-Frame-Options': X_FRAME_OPTIONS,
    'X-Content-Type-Options': X_CONTENT_TYPE_OPTIONS,
    'Referrer-Policy': REFERRER_POLICY,
    'Strict-Transport-Security': `max-age=${STRICT_TRANSPORT_SECURITY.maxAge}; includeSubDomains`,
    'Permissions-Policy': PERMISSIONS_POLICY,
  }
}
