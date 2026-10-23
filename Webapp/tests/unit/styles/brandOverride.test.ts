// @vitest-environment node
//
// The injected brand rule must win over the --brand-* defaults in govnl.css
// whatever order the <style> and the stylesheet <link> end up in the head
// (#48). happy-dom ignores specificity for custom properties, so this pins
// the selectors instead of reading getComputedStyle.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import postcss from 'postcss'
import { brandStyle } from '~/utils/branding'

const GOVNL_CSS = readFileSync(resolve(__dirname, '../../../assets/css/govnl.css'), 'utf8')

const TOKENS = Object.fromEntries([
  'cta', 'cta_hover', 'cta_active', 'cta_subtle', 'primary', 'primary_hover', 'primary_active',
  'primary_subtle', 'secondary', 'surface', 'body_text', 'border', 'wedge_start', 'wedge_mid', 'wedge_end',
].map(k => [k, '#123456']))

// Specificity (a, b, c) of a simple selector made of type selectors,
// classes, ids and pseudo-classes, which is all these rules use.
function specificity(selector: string): [number, number, number] {
  const ids = (selector.match(/#[\w-]+/g) ?? []).length
  const classes = (selector.match(/(\.[\w-]+|:(?!:)[\w-]+|\[[^\]]*\])/g) ?? []).length
  const types = (selector.replace(/[#.:[][^\s>+~]*/g, ' ').match(/\b[a-z][\w-]*/gi) ?? []).length
  return [ids, classes, types]
}

function compare(a: [number, number, number], b: [number, number, number]): number {
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return a[i] - b[i]
  return 0
}

// Every selector in govnl.css that declares a --brand-* property.
function defaultBrandSelectors(): string[] {
  const out: string[] = []
  postcss.parse(GOVNL_CSS).walkRules((rule) => {
    if (rule.nodes.some(n => n.type === 'decl' && n.prop.startsWith('--brand-'))) out.push(...rule.selectors)
  })
  return out
}

describe('brand override specificity', () => {
  const override = postcss.parse(brandStyle(TOKENS)!).first as postcss.Rule

  it('uses :root:root', () => {
    expect(override.selector).toBe(':root:root')
    expect(specificity(override.selector)).toEqual([0, 2, 0])
  })

  it('outranks every default --brand-* rule in govnl.css', () => {
    const selectors = defaultBrandSelectors()
    expect(selectors.length).toBeGreaterThan(0)
    for (const s of selectors) {
      expect(compare(specificity(override.selector), specificity(s)), s).toBeGreaterThan(0)
    }
  })

  it('overrides every --brand-* property the stylesheet defines', () => {
    const defined = new Set<string>()
    postcss.parse(GOVNL_CSS).walkDecls(/^--brand-/, d => { defined.add(d.prop) })
    const overridden = new Set<string>()
    override.walkDecls(d => { overridden.add(d.prop) })
    // --brand-white stays white: text on the brand colours is checked
    // against white, so it is not themeable.
    defined.delete('--brand-white')
    expect([...overridden].sort()).toEqual([...defined].sort())
  })
})
