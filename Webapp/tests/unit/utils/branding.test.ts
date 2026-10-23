import { describe, it, expect } from 'vitest'
import {
  BRAND_TOKEN_VARS, CONTRAST_PAIR_LABELS, brandColors, brandLogoSrc, brandStyle, contrastFailures,
  internalApiBase, parseBranding,
} from '~/utils/branding'

const TOKENS = {
  cta: '#0b5cad', cta_hover: '#094a8a', cta_active: '#073768', cta_subtle: '#dae7f3',
  primary: '#1a1a6e', primary_hover: '#151558', primary_active: '#101042', primary_subtle: '#dddde9',
  secondary: '#eef3ff', surface: '#f7f7f7', body_text: '#111111', border: '#d0d0d0',
  wedge_start: '#101042', wedge_mid: '#1a1a6e', wedge_end: '#21218b',
}

const RESPONSE = {
  colors: { cta: '#0b5cad', primary: '#1a1a6e', secondary: '#eef3ff', surface: '#f7f7f7', body_text: '#111111', border: '#d0d0d0' },
  tokens: TOKENS,
  is_default: false,
  logo: { url: '/api/v1/branding/logo', width: 240, height: 80, alt: 'Gemeente Voorbeeld' },
  contrast_failures: [],
  updated_at: '2026-10-11T10:00:00.123456Z',
}

describe('brandStyle', () => {
  it('overrides every --brand-* variable under :root:root', () => {
    const css = brandStyle(TOKENS)!
    expect(css.startsWith(':root:root{')).toBe(true)
    for (const [key, cssVar] of BRAND_TOKEN_VARS) {
      expect(css).toContain(`${cssVar}:${TOKENS[key]}`)
    }
    expect(css).toContain('--brand-wedge-gradient:linear-gradient(135deg, #101042 0%, #1a1a6e 55%, #21218b 100%)')
  })

  it('lowercases accepted values', () => {
    expect(brandStyle({ ...TOKENS, cta: '#0B5CAD' })).toContain('--brand-cta:#0b5cad')
  })

  // Values containing }, ;, < or anything outside #rrggbb never reach the
  // stylesheet: the whole palette is dropped instead.
  it.each([
    '#0b5cad}body{display:none',
    '#0b5cad;--brand-primary:red',
    '</style><script>alert(1)</script>',
    'red',
    '#fff',
    '#0b5cad00',
    'url(https://evil.example/x.png)',
    'var(--brand-primary)',
    ' #0b5cad',
    '',
    42,
    null,
  ])('rejects %j', (bad) => {
    expect(brandStyle({ ...TOKENS, cta: bad })).toBeNull()
    expect(brandStyle({ ...TOKENS, wedge_end: bad })).toBeNull()
  })

  it('rejects a missing token or a non-object', () => {
    const { border: _border, ...partial } = TOKENS
    expect(brandStyle(partial)).toBeNull()
    expect(brandStyle(null)).toBeNull()
    expect(brandStyle([TOKENS])).toBeNull()
    expect(brandStyle('x')).toBeNull()
  })
})

describe('parseBranding', () => {
  it('adds no style for the default palette, which the stylesheet holds', () => {
    expect(parseBranding({ ...RESPONSE, is_default: true, logo: null })).toEqual({ style: null, logo: null })
  })

  it('returns the rule and the logo of a custom brand', () => {
    const state = parseBranding(RESPONSE)!
    expect(state.style).toBe(brandStyle(TOKENS))
    expect(state.logo).toEqual({ width: 240, height: 80, alt: 'Gemeente Voorbeeld', version: RESPONSE.updated_at })
  })

  it('drops a custom palette with a bad token instead of applying half of it', () => {
    expect(parseBranding({ ...RESPONSE, tokens: { ...TOKENS, primary: 'red' } })).toBeNull()
  })

  it.each([
    [{ width: 0, height: 80, alt: 'x' }],
    [{ width: 240, height: 801, alt: 'x' }],
    [{ width: 2001, height: 80, alt: 'x' }],
    [{ width: 1.5, height: 80, alt: 'x' }],
    [{ width: 240, height: 80, alt: '  ' }],
    [{ width: '240', height: 80, alt: 'x' }],
    ['logo'],
  ])('ignores an unusable logo %j', (logo) => {
    expect(parseBranding({ ...RESPONSE, logo })!.logo).toBeNull()
  })

  it('trims and caps the alt text', () => {
    const logo = parseBranding({ ...RESPONSE, logo: { width: 16, height: 16, alt: ` ${'a'.repeat(300)} ` } })!.logo!
    expect(logo.alt).toHaveLength(255)
  })

  it('returns null for a response that is not an object', () => {
    expect(parseBranding(null)).toBeNull()
    expect(parseBranding('x')).toBeNull()
  })
})

describe('brandLogoSrc', () => {
  it('is same-origin and changes with each write', () => {
    expect(brandLogoSrc({ width: 1, height: 1, alt: 'x', version: '2026-10-11T10:00:00Z' }))
      .toBe('/branding/logo?v=2026-10-11T10%3A00%3A00Z')
    expect(brandLogoSrc({ width: 1, height: 1, alt: 'x', version: '' })).toBe('/branding/logo')
  })
})

describe('brandColors', () => {
  it('reads the six colours', () => {
    expect(brandColors(RESPONSE)).toEqual(RESPONSE.colors)
  })
  it('returns null when one is missing or malformed', () => {
    expect(brandColors({ colors: { ...RESPONSE.colors, border: 'grey' } })).toBeNull()
    expect(brandColors({})).toBeNull()
  })
})

describe('contrastFailures', () => {
  const failure = { pair: 'white_on_cta', foreground: '#ffffff', background: '#009dff', ratio: 2.88, required: 4.5 }

  it('reads the failing pairs from the API error envelope', () => {
    const err = { status: 422, data: { error: { code: 'validation_failed', details: { contrast_failures: [failure] } } } }
    expect(contrastFailures(err)).toEqual([failure])
  })

  it('also reads an unwrapped body and skips malformed entries', () => {
    const err = { data: { details: { contrast_failures: [failure, { pair: 'x' }, { ...failure, background: '<b>' }, null] } } }
    expect(contrastFailures(err)).toEqual([failure])
  })

  it('returns nothing for other errors', () => {
    expect(contrastFailures(new Error('network'))).toEqual([])
    expect(contrastFailures({ data: { error: { code: 'forbidden' } } })).toEqual([])
    expect(contrastFailures(null)).toEqual([])
  })

  it('has a Dutch label for every pair the API checks', () => {
    expect(Object.keys(CONTRAST_PAIR_LABELS)).toHaveLength(8)
  })
})

describe('internalApiBase', () => {
  it('prefers runtime config, then API_BASE_INTERNAL_URL, then the public base', () => {
    expect(internalApiBase({ apiBaseInternal: 'http://api.ns.svc:8080/', public: { apiBase: 'https://x' } }, {}))
      .toBe('http://api.ns.svc:8080')
    expect(internalApiBase({ apiBaseInternal: '', public: { apiBase: 'https://x' } }, { API_BASE_INTERNAL_URL: 'http://y:8080' }))
      .toBe('http://y:8080')
    expect(internalApiBase({ public: { apiBase: 'https://appstore.example.org' } }, {}))
      .toBe('https://appstore.example.org')
  })

  it('is empty when no base is an absolute http(s) URL', () => {
    expect(internalApiBase({ public: { apiBase: '' } }, {})).toBe('')
    expect(internalApiBase({ public: { apiBase: '/api' } }, { API_BASE_INTERNAL_URL: 'file:///etc' })).toBe('')
  })
})
