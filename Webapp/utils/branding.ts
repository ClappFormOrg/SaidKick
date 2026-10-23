// Instance branding (#158). The API serves six colours and the tokens it
// derives from them; this file turns that response into the one <style> rule
// that overrides the --brand-* defaults in assets/css/govnl.css, and into the
// header logo. Every value is checked here before it can reach a stylesheet
// or an attribute: anything that is not #rrggbb drops the whole palette, so
// the stylesheet defaults apply instead of half a brand.

// Path the webapp serves the logo on. A Nitro route (server/routes) proxies
// the API, so the image is same-origin and the CSP's img-src 'self' covers
// it wherever the API runs.
export const BRANDING_LOGO_PATH = '/branding/logo'

const HEX = /^#[0-9a-f]{6}$/

// The API origin the webapp server uses for the brand and the logo proxy:
// runtimeConfig.apiBaseInternal (NUXT_API_BASE_INTERNAL), then
// API_BASE_INTERNAL_URL as the hub chart sets it, then the browser-facing
// base. Empty when none is an absolute http(s) URL.
export function internalApiBase(
  config: { apiBaseInternal?: unknown; public: { apiBase?: unknown } },
  env: Readonly<Record<string, string | undefined>>,
): string {
  for (const c of [config.apiBaseInternal, env.API_BASE_INTERNAL_URL, config.public.apiBase]) {
    if (typeof c === 'string' && /^https?:\/\//.test(c)) return c.replace(/\/+$/, '')
  }
  return ''
}

// Token key in the API response, then the custom property it sets. The wedge
// gradient is composed from its three stops below.
export const BRAND_TOKEN_VARS = [
  ['cta', '--brand-cta'],
  ['cta_hover', '--brand-cta-hover'],
  ['cta_active', '--brand-cta-active'],
  ['cta_subtle', '--brand-cta-subtle'],
  ['primary', '--brand-primary'],
  ['primary_hover', '--brand-primary-hover'],
  ['primary_active', '--brand-primary-active'],
  ['primary_subtle', '--brand-primary-subtle'],
  ['secondary', '--brand-secondary'],
  ['surface', '--brand-surface'],
  ['body_text', '--brand-body'],
  ['border', '--brand-border'],
] as const

export type BrandTokenKey = typeof BRAND_TOKEN_VARS[number][0] | 'wedge_start' | 'wedge_mid' | 'wedge_end'
export type BrandTokens = Record<BrandTokenKey, string>

// The six colours an admin sets, as the API names them.
export const BRAND_COLOR_KEYS = ['cta', 'primary', 'secondary', 'surface', 'body_text', 'border'] as const
export type BrandColorKey = typeof BRAND_COLOR_KEYS[number]
export type BrandColors = Record<BrandColorKey, string>

export interface BrandLogo {
  width: number
  height: number
  alt: string
  // Changes on every write, so a new logo gets a new URL.
  version: string
}

// What the site renders: the override rule (null for the default palette,
// which the stylesheet already holds) and the logo, if any.
export interface BrandState {
  style: string | null
  logo: BrandLogo | null
}

export interface ContrastFailure {
  pair: string
  foreground: string
  background: string
  ratio: number
  required: number
}

function hex(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const v = value.toLowerCase()
  return HEX.test(v) ? v : null
}

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

// The override rule for tokens, or null when any token is not #rrggbb.
//
// The selector is :root:root. Its specificity (0,2,0) beats the stylesheet's
// :root (0,1,0), so the brand wins whatever order the <style> and the
// stylesheet <link> end up in the head (#48).
export function brandStyle(tokens: unknown): string | null {
  const t = record(tokens)
  if (!t) return null
  const decls: string[] = []
  for (const [key, cssVar] of BRAND_TOKEN_VARS) {
    const v = hex(t[key])
    if (!v) return null
    decls.push(`${cssVar}:${v}`)
  }
  const stops = [hex(t.wedge_start), hex(t.wedge_mid), hex(t.wedge_end)]
  if (stops.some(s => !s)) return null
  decls.push(`--brand-wedge-gradient:linear-gradient(135deg, ${stops[0]} 0%, ${stops[1]} 55%, ${stops[2]} 100%)`)
  return `:root:root{${decls.join(';')}}`
}

// Logo limits, mirroring the API's (internal/branding/logo.go).
const MAX_LOGO_WIDTH = 2000
const MAX_LOGO_HEIGHT = 800
const MAX_ALT = 255

function dimension(value: unknown, max: number): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 && value <= max ? value : null
}

function brandLogo(value: unknown, version: string): BrandLogo | null {
  const l = record(value)
  if (!l) return null
  const width = dimension(l.width, MAX_LOGO_WIDTH)
  const height = dimension(l.height, MAX_LOGO_HEIGHT)
  const alt = typeof l.alt === 'string' ? l.alt.trim().slice(0, MAX_ALT) : ''
  if (!width || !height || !alt) return null
  return { width, height, alt, version }
}

// The site's brand from a GET /api/v1/branding response, or null when the
// response is unusable (the stylesheet defaults then apply).
export function parseBranding(raw: unknown): BrandState | null {
  const r = record(raw)
  if (!r) return null
  const version = typeof r.updated_at === 'string' ? r.updated_at : ''
  const style = r.is_default === true ? null : brandStyle(r.tokens)
  // A non-default palette that fails the checks is dropped, not half applied.
  if (r.is_default !== true && !style) return null
  return { style, logo: brandLogo(r.logo, version) }
}

// Same-origin URL of the stored logo.
export function brandLogoSrc(logo: BrandLogo): string {
  return logo.version ? `${BRANDING_LOGO_PATH}?v=${encodeURIComponent(logo.version)}` : BRANDING_LOGO_PATH
}

// The six colours of a response, or null when one is missing or malformed.
export function brandColors(raw: unknown): BrandColors | null {
  const c = record(record(raw)?.colors)
  if (!c) return null
  const out = {} as BrandColors
  for (const key of BRAND_COLOR_KEYS) {
    const v = hex(c[key])
    if (!v) return null
    out[key] = v
  }
  return out
}

// The failing pairs of a 422 from PUT /api/v1/branding. The API wraps errors
// as { error: { ..., details: { contrast_failures } } }.
export function contrastFailures(err: unknown): ContrastFailure[] {
  const data = record(record(err)?.data)
  const body = record(data?.error) ?? data
  const list = record(body?.details)?.contrast_failures
  if (!Array.isArray(list)) return []
  return list.flatMap((item): ContrastFailure[] => {
    const f = record(item)
    if (!f || typeof f.pair !== 'string' || typeof f.ratio !== 'number' || typeof f.required !== 'number') return []
    const foreground = hex(f.foreground)
    const background = hex(f.background)
    if (!foreground || !background) return []
    return [{ pair: f.pair, foreground, background, ratio: f.ratio, required: f.required }]
  })
}

// Dutch names for the checked pairs (internal/branding.Contrast).
export const CONTRAST_PAIR_LABELS: Record<string, string> = {
  white_on_cta: 'Witte tekst op de actiekleur',
  white_on_cta_hover: 'Witte tekst op de actiekleur (hover)',
  white_on_primary: 'Witte tekst op de primaire kleur',
  white_on_primary_hover: 'Witte tekst op de primaire kleur (hover)',
  primary_on_white: 'Primaire kleur als tekst op wit',
  primary_on_secondary: 'Primaire kleur als tekst op de secundaire kleur',
  body_text_on_white: 'Tekstkleur op wit',
  body_text_on_surface: 'Tekstkleur op de achtergrondkleur',
}
