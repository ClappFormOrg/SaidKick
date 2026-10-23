import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs'
import { join, resolve, relative, sep } from 'node:path'

// process.cwd() is the directory holding vitest.config.ts, i.e. Webapp/.
const ROOT = process.cwd()
const SCAN = [
  'components', 'layouts', 'pages', 'composables', 'utils', 'middleware', 'plugins',
  'app.vue', 'error.vue',
]

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

describe('icon imports', () => {
  // One import of the @tabler/icons-vue barrel anywhere puts every icon module
  // back in Vite's dep-optimizer graph. Import from '~/lib/icons' instead.
  it('never reaches for the @tabler/icons-vue barrel', () => {
    const offenders = SCAN.flatMap(d => walk(resolve(ROOT, d)))
      .filter(f => /from ['"]@tabler\/icons-vue['"]/.test(readFileSync(f, 'utf8')))
      .map(f => relative(ROOT, f).split(sep).join('/'))

    expect(offenders).toEqual([])
  })

  // Guards against `export * from '@tabler/icons-vue'` in lib/icons.ts, which
  // would restore the problem while the check above stayed green.
  it('re-exports only per-icon deep paths from lib/icons.ts', () => {
    const lines = readFileSync(resolve(ROOT, 'lib/icons.ts'), 'utf8')
      .split('\n')
      .map(l => l.trim())
      .filter(l => l.startsWith('export'))

    expect(lines.length).toBeGreaterThan(0)
    for (const line of lines) {
      expect(line).toMatch(
        /^export \{ default as Icon\w+ \} from '@tabler\/icons-vue\/dist\/esm\/icons\/Icon\w+\.mjs'$/,
      )
    }
  })
})
