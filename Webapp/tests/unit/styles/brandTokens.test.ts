// @vitest-environment node
//
// The brand token layer (#156). happy-dom resolves custom properties by
// document order and ignores specificity, so these tests read the emitted CSS
// and the stylesheet source instead of getComputedStyle.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import postcss from 'postcss'
import tailwindcss from 'tailwindcss'
import config from '~/tailwind.config'

const GOVNL_CSS = readFileSync(resolve(__dirname, '../../../assets/css/govnl.css'), 'utf8')

// Every brand class, plus govnl ramp classes that must stay literal.
const BRAND_CLASSES = {
  'bg-brand-cta': 'background-color:var(--brand-cta)',
  'hover:bg-brand-cta-hover': 'background-color:var(--brand-cta-hover)',
  'bg-brand-cta-active': 'background-color:var(--brand-cta-active)',
  'bg-brand-cta-subtle': 'background-color:var(--brand-cta-subtle)',
  'text-brand-primary': 'color:var(--brand-primary)',
  'bg-brand-primary-hover': 'background-color:var(--brand-primary-hover)',
  'bg-brand-primary-active': 'background-color:var(--brand-primary-active)',
  'bg-brand-primary-subtle': 'background-color:var(--brand-primary-subtle)',
  'bg-brand-secondary': 'background-color:var(--brand-secondary)',
  'bg-brand-surface': 'background-color:var(--brand-surface)',
  'text-brand-body': 'color:var(--brand-body)',
  'border-brand-border': 'border-color:var(--brand-border)',
  'text-brand-white': 'color:var(--brand-white)',
  'bg-brand-wedge': 'background-image:var(--brand-wedge-gradient)',
} as const

async function emit(classes: string[]): Promise<string> {
  const result = await postcss([
    tailwindcss({
      ...config,
      content: [{ raw: `<div class="${classes.join(' ')}"></div>`, extension: 'html' }],
    }),
  ]).process('@tailwind utilities;', { from: undefined })
  return result.css.replace(/\s+/g, '')
}

// Custom property declarations in govnl.css, last one wins (matches the
// cascade for same-specificity :root blocks).
function declarations(css: string): Map<string, string> {
  const vars = new Map<string, string>()
  for (const m of css.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) vars.set(m[1], m[2].trim())
  return vars
}

function resolveVar(name: string, vars: Map<string, string>, depth = 0): string {
  const value = vars.get(name)
  if (value === undefined) throw new Error(`${name} is not defined in govnl.css`)
  const ref = /^var\((--[\w-]+)\)$/.exec(value)
  if (!ref) return value
  if (depth > 10) throw new Error(`${name} has a var() cycle`)
  return resolveVar(ref[1], vars, depth + 1)
}

describe('Tailwind brand colours', () => {
  it('emit var(--brand-*) for every semantic class', async () => {
    const css = await emit(Object.keys(BRAND_CLASSES))
    for (const [cls, decl] of Object.entries(BRAND_CLASSES)) {
      expect(css, cls).toContain(decl)
    }
  })

  it('keep the govnl grey and feedback ramps literal', async () => {
    const css = await emit(['text-govnl-grijs-600', 'bg-govnl-rood-50', 'bg-govnl-lintblauw-500'])
    // #475569, #FBEAE8, #154273 as Tailwind's rgb channel form.
    expect(css).toContain('color:rgb(7185105/var(--tw-text-opacity')
    expect(css).toContain('background-color:rgb(251234232/var(--tw-bg-opacity')
    expect(css).toContain('background-color:rgb(2166115/var(--tw-bg-opacity')
    expect(css).not.toContain('var(--brand-')
  })
})

describe('brand component classes', () => {
  async function components(classes: string[]): Promise<string> {
    const result = await postcss([
      tailwindcss({
        ...config,
        content: [{ raw: `<div class="${classes.join(' ')}"></div>`, extension: 'html' }],
      }),
    ]).process('@tailwind components;', { from: undefined })
    return result.css.replace(/\s+/g, '')
  }

  it('take their themeable colours from --brand-* variables', async () => {
    const css = await components(['brand-btn-primary', 'brand-btn-navy', 'brand-h1', 'brand-link', 'brand-card', 'brand-wedge'])
    expect(css).toContain('.brand-btn-primary{background-color:var(--brand-cta);border-color:var(--brand-cta)')
    expect(css).toContain('.brand-btn-navy{background-color:var(--brand-primary)')
    expect(css).toContain('.brand-h1{color:var(--brand-primary)')
    expect(css).toContain('.brand-link{color:var(--brand-primary)')
    expect(css).toContain('border:1pxsolidvar(--brand-border)')
    expect(css).toContain('background-image:var(--brand-wedge-gradient)')
  })

  it('reference only variables govnl.css defines', async () => {
    const css = await components([
      'brand-display', 'brand-h1', 'brand-h2', 'brand-h3', 'brand-lead', 'brand-prose', 'brand-link',
      'brand-btn', 'brand-btn-sm', 'brand-btn-lg', 'brand-btn-primary', 'brand-btn-navy', 'brand-btn-secondary',
      'brand-btn-ghost', 'brand-btn-danger', 'brand-btn-link', 'brand-card', 'brand-card-interactive',
      'brand-label', 'brand-input', 'brand-help', 'brand-table', 'brand-wedge',
    ])
    const vars = declarations(GOVNL_CSS)
    const used = new Set([...css.matchAll(/var\((--[\w-]+)\)/g)].map(m => m[1]))
    expect(used.size).toBeGreaterThan(0)
    for (const name of used) expect(vars.has(name), name).toBe(true)
  })
})

describe('--brand-* defaults in govnl.css', () => {
  const vars = declarations(GOVNL_CSS)

  it('define every variable the Tailwind config references', () => {
    const referenced = new Set(
      [...JSON.stringify(config.theme).matchAll(/var\((--brand-[\w-]+)\)/g)].map(m => m[1]),
    )
    expect(referenced.size).toBeGreaterThan(0)
    for (const name of referenced) expect(vars.has(name), name).toBe(true)
  })

  // The GovNL defaults. API/internal/branding.DefaultTokens serves the same
  // values for the untouched palette.
  it.each([
    ['--brand-cta', '#CA005D'],
    ['--brand-cta-hover', '#A30049'],
    ['--brand-cta-active', '#7D0038'],
    ['--brand-cta-subtle', '#F6DDE7'],
    ['--brand-primary', '#154273'],
    ['--brand-primary-hover', '#11355C'],
    ['--brand-primary-active', '#0D2845'],
    ['--brand-primary-subtle', '#DCE3EA'],
    ['--brand-secondary', '#E1F0FA'],
    ['--brand-surface', '#F1F5F9'],
    ['--brand-body', '#0F172A'],
    ['--brand-border', '#CBD5E1'],
    ['--brand-white', '#ffffff'],
    ['--brand-wedge-gradient', 'linear-gradient(135deg, #0A2750 0%, #154273 55%, #1B5391 100%)'],
  ])('%s resolves to %s', (name, expected) => {
    expect(resolveVar(name, vars)).toBe(expected)
  })

  // The old --govnl-* colour roles (document, interaction, line, form
  // control, brand accents) were removed in favour of --brand-*. Any --govnl-*
  // variable outside the colour ramps that still holds a colour must read the
  // brand layer.
  it('keep no --govnl-* colour role outside the ramps', () => {
    const roles = [...vars].filter(([name]) => name.startsWith('--govnl-') && !name.startsWith('--govnl-color-'))
    const colours = roles.filter(([, v]) => /#[0-9a-f]{3,8}\b|var\(--govnl-color-/i.test(v))
    expect(colours).toEqual([])
    expect(vars.get('--govnl-focus-outline-color')).toBe('var(--brand-cta)')
    for (const gone of ['--govnl-interaction-color', '--govnl-document-color', '--govnl-brand-accent-3']) {
      expect(vars.has(gone), gone).toBe(false)
    }
  })

  it('put Fira Sans first in the font stacks', () => {
    expect(vars.get('--govnl-font-family-sans')).toMatch(/^"Fira Sans",/)
    expect(JSON.stringify(config.theme.extend.fontFamily.sans)).toMatch(/^\["\\"Fira Sans\\""/)
  })

  it('load no stylesheet from a remote origin', () => {
    for (const m of GOVNL_CSS.matchAll(/@import\s+([^;]+);/g)) {
      expect(m[1]).not.toMatch(/(?:https?:)?\/\//)
    }
  })
})
