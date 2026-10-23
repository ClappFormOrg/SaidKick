// @vitest-environment node
//
// Page markup takes themeable colours from the brand-* layer (#156, Q3). The
// raw govnl-* ramps are allowed only for feedback colours and greys, which a
// brand does not recolour. This scan fails on any other govnl ramp, as a
// Tailwind class (bg-govnl-lintblauw-500) or as a custom property
// (var(--govnl-color-robijnrood-500)).
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs'
import { join, resolve, relative, sep } from 'node:path'
import config from '~/tailwind.config'

// process.cwd() is the directory holding vitest.config.ts, i.e. Webapp/.
const ROOT = process.cwd()
const SCAN = ['pages', 'components', 'layouts', 'error.vue', 'app.vue']

// Greys, black and white, and the four feedback families: informative
// (hemelblauw), positive (groen), warning (geel, bruin, oranje) and negative
// (rood).
const ALLOWED = new Set(['grijs', 'wit', 'zwart', 'hemelblauw', 'groen', 'geel', 'bruin', 'oranje', 'rood'])

const RAMPS = new Set(Object.keys((config.theme.extend.colors as { govnl: Record<string, unknown> }).govnl))
const FORBIDDEN = [...RAMPS].filter(r => !ALLOWED.has(r))

function walk(p: string, out: string[] = []): string[] {
  if (!existsSync(p)) return out
  if (!statSync(p).isDirectory()) {
    if (/\.(vue|ts)$/.test(p)) out.push(p)
    return out
  }
  for (const e of readdirSync(p, { withFileTypes: true })) {
    const child = join(p, e.name)
    if (e.isDirectory()) walk(child, out)
    else if (/\.(vue|ts)$/.test(e.name)) out.push(child)
  }
  return out
}

const files = SCAN.flatMap(d => walk(resolve(ROOT, d)))
const rel = (f: string) => relative(ROOT, f).split(sep).join('/')

// Every govnl colour reference in a source, as "line: token".
function references(src: string): { ramp: string; where: string }[] {
  const out: { ramp: string; where: string }[] = []
  src.split('\n').forEach((line, i) => {
    for (const m of line.matchAll(/govnl-(?:color-)?([a-z]+)(?=-\d|-wit|-zwart|\b)/g)) {
      if (RAMPS.has(m[1]!)) out.push({ ramp: m[1]!, where: `${i + 1}: ${m[0]}` })
    }
  })
  return out
}

describe('govnl-* colours in markup', () => {
  it('finds files and ramps to check', () => {
    expect(files.length).toBeGreaterThan(20)
    expect(FORBIDDEN).toEqual(expect.arrayContaining(['lintblauw', 'robijnrood']))
  })

  it('use only feedback and grey ramps', () => {
    const offenders = files.flatMap(f =>
      references(readFileSync(f, 'utf8'))
        .filter(r => !ALLOWED.has(r.ramp))
        .map(r => `${rel(f)}:${r.where}`),
    )
    expect(offenders).toEqual([])
  })

  it('catches a forbidden class and custom property', () => {
    const src = '<div class="bg-govnl-lintblauw-500" :style="{ color: \'var(--govnl-color-robijnrood-500)\' }" />'
    expect(references(src).map(r => r.ramp)).toEqual(['lintblauw', 'robijnrood'])
    expect(references('<p class="text-govnl-grijs-600 bg-govnl-rood-50 govnl-button" />').map(r => r.ramp))
      .toEqual(['grijs', 'rood'])
  })
})
