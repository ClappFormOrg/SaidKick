import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve, relative } from 'node:path'

// Nuxt treats pages/foo.vue next to a pages/foo/ directory as a parent route
// and renders the child pages inside it. A parent without <NuxtPage> swallows
// every child: /dashboard/apps/<slug>/new-version used to render the app
// detail page because apps/[slug].vue sat next to apps/[slug]/.

const PAGES = resolve(__dirname, '../../../pages')

function parentsWithoutOutlet(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      const sibling = `${full}.vue`
      try {
        if (!readFileSync(sibling, 'utf8').includes('<NuxtPage')) {
          found.push(relative(PAGES, sibling))
        }
      } catch {
        // no sibling page file: a plain directory of routes
      }
      found.push(...parentsWithoutOutlet(full))
    }
  }
  return found
}

describe('page routes', () => {
  it('has no parent page that would hide its child routes', () => {
    expect(parentsWithoutOutlet(PAGES)).toEqual([])
  })
})
