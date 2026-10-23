import { describe, it, expect } from 'vitest'
import {
  NONCE_SOURCE, apiOrigin, contentSecurityPolicy, cspApiBase, securityHeaders, serializeCsp,
} from '~/utils/securityHeaders'

describe('securityHeaders', () => {
  it('keeps the baseline header values', () => {
    expect(securityHeaders({ apiOrigin: null })).toMatchObject({
      'X-Frame-Options': 'DENY',
      'X-Content-Type-Options': 'nosniff',
      'Referrer-Policy': 'strict-origin-when-cross-origin',
      'Strict-Transport-Security': 'max-age=31536000; includeSubDomains',
      'Permissions-Policy': 'geolocation=(), camera=(), microphone=()',
    })
  })
})

describe('contentSecurityPolicy', () => {
  it('is the production policy, in order, when the API shares the origin', () => {
    expect(serializeCsp(contentSecurityPolicy({ apiOrigin: null }))).toBe([
      "default-src 'self'",
      "script-src 'self' 'nonce-{{nonce}}' 'strict-dynamic'",
      "style-src 'self' 'nonce-{{nonce}}'",
      "style-src-attr 'unsafe-inline'",
      "img-src 'self' data: https://raw.githubusercontent.com",
      "font-src 'self'",
      "connect-src 'self' https://raw.githubusercontent.com",
      "frame-ancestors 'none'",
      "base-uri 'self'",
      "object-src 'none'",
      "form-action 'self'",
      'upgrade-insecure-requests',
    ].join('; '))
  })

  it('adds a cross-origin API to connect-src only', () => {
    const csp = contentSecurityPolicy({ apiOrigin: 'https://api.example.org' })
    expect(csp['connect-src']).toEqual(["'self'", 'https://raw.githubusercontent.com', 'https://api.example.org'])
    expect(serializeCsp(csp).match(/api\.example\.org/g)).toHaveLength(1)
  })

  it('puts the nonce placeholder on scripts and styles, with strict-dynamic for scripts', () => {
    const csp = contentSecurityPolicy({ apiOrigin: null })
    expect(NONCE_SOURCE).toBe("'nonce-{{nonce}}'")
    expect(csp['script-src']).toEqual(["'self'", NONCE_SOURCE, "'strict-dynamic'"])
    expect(csp['style-src']).toEqual(["'self'", NONCE_SOURCE])
  })

  it('never allows inline script, eval or inline style elements', () => {
    const csp = contentSecurityPolicy({ apiOrigin: null })
    for (const [name, sources] of Object.entries(csp)) {
      if (sources === true) continue
      expect(sources, name).not.toContain("'unsafe-eval'")
      if (name !== 'style-src-attr') expect(sources, name).not.toContain("'unsafe-inline'")
    }
  })

  it('in dev drops upgrade-insecure-requests and allows the dev server', () => {
    const csp = contentSecurityPolicy({
      apiOrigin: 'http://localhost:8080', dev: true, devOrigins: ['ws://localhost:*'],
    })
    expect(csp).not.toHaveProperty('upgrade-insecure-requests')
    expect(csp['connect-src']).toEqual([
      "'self'", 'https://raw.githubusercontent.com', 'http://localhost:8080', 'ws://localhost:*',
    ])
  })

  it('ignores dev origins outside dev', () => {
    const csp = contentSecurityPolicy({ apiOrigin: null, devOrigins: ['ws://localhost:*'] })
    expect(csp['connect-src']).not.toContain('ws://localhost:*')
    expect(csp['upgrade-insecure-requests']).toBe(true)
  })
})

describe('cspApiBase', () => {
  it('adds no API origin to a production build with no API base configured', () => {
    const base = cspApiBase({}, false)
    expect(base).toBe('')
    expect(contentSecurityPolicy({ apiOrigin: apiOrigin(base) })['connect-src'])
      .toEqual(["'self'", 'https://raw.githubusercontent.com'])
  })

  it('falls back to the local API in dev', () => {
    expect(cspApiBase({}, true)).toBe('http://localhost:8080')
  })

  it('prefers NUXT_PUBLIC_API_BASE over API_BASE_URL', () => {
    expect(cspApiBase({ API_BASE_URL: 'https://a.example.org' }, false)).toBe('https://a.example.org')
    expect(cspApiBase({ NUXT_PUBLIC_API_BASE: 'https://b.example.org', API_BASE_URL: 'https://a.example.org' }, false))
      .toBe('https://b.example.org')
  })
})

describe('apiOrigin', () => {
  it('returns the origin of an absolute API base', () => {
    expect(apiOrigin('https://api.example.org/base/')).toBe('https://api.example.org')
    expect(apiOrigin('http://localhost:8080')).toBe('http://localhost:8080')
  })

  it('omits the API when it is on the app origin', () => {
    expect(apiOrigin('http://localhost:3000/api', 'http://localhost:3000')).toBeNull()
    expect(apiOrigin('http://localhost:8080', 'http://localhost:3000')).toBe('http://localhost:8080')
  })

  it('omits an empty or relative base, which the browser resolves against the app', () => {
    expect(apiOrigin('')).toBeNull()
    expect(apiOrigin('/api')).toBeNull()
  })
})
