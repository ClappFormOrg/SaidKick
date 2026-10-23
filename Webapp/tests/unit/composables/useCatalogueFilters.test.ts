import { describe, it, expect } from 'vitest'
import {
  matchesFilters, countByCategory, countByStatus, sortApps, toggleValue,
  ALL_STATUSES, type CatalogueFilterState,
} from '~/composables/useCatalogueFilters'
import type { App } from '~/types'

/**
 * Issue #34: the catalogue's facet counts were computed from the full dataset, so
 * searching for something with no matches showed "0 resultaten" beside
 * "Productie 2". These tests pin the counting rule.
 */

function app(over: Partial<App> = {}): App {
  const category = over.category ?? 'chatbot'
  return {
    slug: 'a', name: 'Inspraakhulp', tagline: '', description: '', longDescription: [],
    status: 'Productie',
    eigenaar: 'Provincie Voorbeeld', medeontwikkelaars: [], versie: '1.0.0', bijgewerkt: '2026-01-01',
    licentie: 'EUPL-1.2', repo: 'o/r', scores: {}, riskCategory: 'Beperkt risico',
    installs: 0,
    ...over,
    category,
    // Tags mirror the category unless overridden, as mapCard produces them.
    tags: over.tags ?? [category],
  } as App
}

const NONE: CatalogueFilterState = { categories: [], statuses: [], criteria: [], text: '' }

const CORPUS: App[] = [
  app({ slug: 'a', name: 'Inspraakhulp', category: 'chatbot', status: 'Productie', installs: 5 }),
  app({ slug: 'b', name: 'Nginx Demo', category: 'infrastruct', status: 'Productie', installs: 9 }),
  app({ slug: 'c', name: 'Ragzoeker', category: 'rag', status: 'Concept', installs: 1 }),
  app({ slug: 'd', name: 'Chathulp', category: 'chatbot', status: 'Pilot', installs: 3 }),
]

describe('matchesFilters', () => {
  it('keeps everything when nothing is selected', () => {
    expect(CORPUS.filter(a => matchesFilters(a, NONE))).toHaveLength(4)
  })

  it('filters by category', () => {
    const f = { ...NONE, categories: ['chatbot'] }
    expect(CORPUS.filter(a => matchesFilters(a, f)).map(a => a.slug)).toEqual(['a', 'd'])
  })

  it('filters by status', () => {
    const f = { ...NONE, statuses: ['Productie'] }
    expect(CORPUS.filter(a => matchesFilters(a, f)).map(a => a.slug)).toEqual(['a', 'b'])
  })

  it('combines dimensions as AND', () => {
    const f = { ...NONE, categories: ['chatbot'], statuses: ['Pilot'] }
    expect(CORPUS.filter(a => matchesFilters(a, f)).map(a => a.slug)).toEqual(['d'])
  })

  it('searches name, tagline, tags and vendor', () => {
    expect(CORPUS.filter(a => matchesFilters(a, { ...NONE, text: 'inspraakhulp' }))).toHaveLength(1)
    expect(CORPUS.filter(a => matchesFilters(a, { ...NONE, text: 'Provincie Voorbeeld' }))).toHaveLength(4)
    expect(CORPUS.filter(a => matchesFilters(a, { ...NONE, text: 'infrastruct' }))).toHaveLength(1)
  })

  it('is case-insensitive and trims the query', () => {
    expect(CORPUS.filter(a => matchesFilters(a, { ...NONE, text: '  NGINX  ' }))).toHaveLength(1)
  })

  it('matches nothing for a query with no hits', () => {
    expect(CORPUS.filter(a => matchesFilters(a, { ...NONE, text: 'test123' }))).toHaveLength(0)
  })

  it('requires every selected criterion to pass', () => {
    const pool = [
      app({ slug: 'p', scores: { dpia: 'pass', bio: 'pass' } }),
      app({ slug: 'q', scores: { dpia: 'pass', bio: 'fail' } }),
      app({ slug: 'r', scores: { dpia: 'pending' } }),
    ]
    const f = { ...NONE, criteria: ['dpia', 'bio'] }
    expect(pool.filter(a => matchesFilters(a, f)).map(a => a.slug)).toEqual(['p'])
  })

  it('ignores the dimension named in `except`', () => {
    const f = { ...NONE, categories: ['chatbot'] }
    expect(CORPUS.filter(a => matchesFilters(a, f, 'category'))).toHaveLength(4)
    expect(CORPUS.filter(a => matchesFilters(a, f))).toHaveLength(2)
  })
})

describe('countByCategory / countByStatus — the reported bug', () => {
  it('drops every count to 0 for a 0-result search', () => {
    // The exact repro from the issue: q=test123 returns nothing, so no facet may
    // still advertise a count.
    const f = { ...NONE, text: 'test123' }

    const cats = countByCategory(CORPUS, f)
    const stats = countByStatus(CORPUS, f)

    expect(Object.values(cats).every(n => n === 0)).toBe(true)
    expect(Object.values(stats).every(n => n === 0)).toBe(true)
  })

  it('reflects a 1-result search, not the whole dataset', () => {
    const f = { ...NONE, text: 'Inspraakhulp' }

    expect(countByCategory(CORPUS, f).chatbot).toBe(1)
    expect(countByCategory(CORPUS, f).infrastruct).toBe(0)
    expect(countByStatus(CORPUS, f).Productie).toBe(1)
  })

  it('counts sum to the visible result count across a dimension', () => {
    const f = { ...NONE, text: 'Provincie Voorbeeld' }
    const visible = CORPUS.filter(a => matchesFilters(a, f)).length
    const summed = Object.values(countByStatus(CORPUS, f)).reduce((a, b) => a + b, 0)
    expect(summed).toBe(visible)
  })

  it('keeps sibling categories countable while one is selected', () => {
    // Counting from the fully filtered set would zero these and make the facet
    // list useless for widening a selection.
    const f = { ...NONE, categories: ['chatbot'] }
    const cats = countByCategory(CORPUS, f)

    expect(cats.chatbot).toBe(2)
    expect(cats.infrastruct).toBe(1)
    expect(cats.rag).toBe(1)
  })

  it('narrows a facet by the other active dimensions', () => {
    // Status=Productie is active, so category counts reflect only Productie apps.
    const f = { ...NONE, statuses: ['Productie'] }
    const cats = countByCategory(CORPUS, f)

    expect(cats.chatbot).toBe(1)
    expect(cats.infrastruct).toBe(1)
    expect(cats.rag).toBe(0)
  })

  it('returns an entry for every known facet value', () => {
    const cats = countByCategory(CORPUS, NONE)
    const stats = countByStatus(CORPUS, NONE)
    expect(Object.keys(cats)).toHaveLength(6)
    for (const s of ALL_STATUSES) expect(stats).toHaveProperty(s)
  })

  it('handles an empty corpus without throwing', () => {
    expect(Object.values(countByCategory([], NONE)).every(n => n === 0)).toBe(true)
    expect(Object.values(countByStatus([], NONE)).every(n => n === 0)).toBe(true)
  })
})

describe('sortApps', () => {
  it('sorts by installs descending', () => {
    expect(sortApps(CORPUS, 'installs').map(a => a.slug)).toEqual(['b', 'a', 'd', 'c'])
  })

  it('sorts by name', () => {
    expect(sortApps(CORPUS, 'name').map(a => a.name)[0]).toBe('Chathulp')
  })

  it('does not mutate its input', () => {
    const before = CORPUS.map(a => a.slug)
    sortApps(CORPUS, 'name')
    expect(CORPUS.map(a => a.slug)).toEqual(before)
  })
})

describe('toggleValue', () => {
  it('adds then removes', () => {
    const list: string[] = []
    toggleValue(list, 'x')
    expect(list).toEqual(['x'])
    toggleValue(list, 'x')
    expect(list).toEqual([])
  })

  it('leaves other entries alone', () => {
    const list = ['a', 'b']
    toggleValue(list, 'a')
    expect(list).toEqual(['b'])
  })
})
