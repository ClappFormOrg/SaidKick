import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs'
import { join, resolve, relative, sep } from 'node:path'

// Guards against placeholder links coming back. The shared header and footer
// carried a dozen href="#" links that rendered on every page and went
// nowhere, and the app detail page had a "Toegang aanvragen" button that did
// the same.
//
// A source scan rather than a mounted-component assertion: the point is that
// no such placeholder exists anywhere, including in files no test mounts.

// process.cwd() is the directory holding vitest.config.ts, i.e. Webapp/.
const ROOT = process.cwd()
const SCAN = ['components', 'layouts', 'pages', 'app.vue', 'error.vue']

function walk(p: string, out: string[] = []): string[] {
  if (!existsSync(p)) return out
  if (!statSync(p).isDirectory()) {
    if (p.endsWith('.vue')) out.push(p)
    return out
  }
  for (const e of readdirSync(p, { withFileTypes: true })) {
    const child = join(p, e.name)
    if (e.isDirectory() && e.name !== '.nuxt' && e.name !== 'node_modules') walk(child, out)
    else if (e.isFile() && e.name.endsWith('.vue')) out.push(child)
  }
  return out
}

const files = SCAN.flatMap(d => walk(resolve(ROOT, d)))
const rel = (f: string) => relative(ROOT, f).split(sep).join('/')

// Comments are stripped first so documentation that quotes a banned pattern
// does not trip the check.
function stripComments(src: string): string {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^[ \t]*\/\/.*$/gm, '')
}

const sources = files.map(f => ({ file: rel(f), src: stripComments(readFileSync(f, 'utf8')) }))

function offenders(re: RegExp): string[] {
  return sources.filter(s => re.test(s.src)).map(s => s.file)
}

describe('no placeholder links', () => {
  it('finds Vue files to scan', () => {
    expect(files.length).toBeGreaterThan(10)
  })

  it('has no href="#" anywhere', () => {
    expect(offenders(/href\s*=\s*"#"/)).toEqual([])
  })

  it('has no :href="\'#\'" or to="#" binding', () => {
    expect(offenders(/:href\s*=\s*"'#'"/)).toEqual([])
    expect(offenders(/\bto\s*=\s*"#"/)).toEqual([])
  })

  it("has no `to: '#'` or `path: '#'` placeholder in link data", () => {
    expect(offenders(/\b(?:to|path):\s*['"]#['"]/)).toEqual([])
  })
})

describe('footer link data', () => {
  const layout = stripComments(readFileSync(resolve(ROOT, 'layouts/default.vue'), 'utf8'))

  it('renders only entries that have a destination', () => {
    expect(layout).toContain('col.links.filter(l => l.to)')
    expect(layout).toMatch(/v-for="col in visibleFooterColumns"/)
    expect(layout).toMatch(/v-for="link in visibleLegalLinks"/)
  })
})
