import type { Ref } from 'vue'
import type { App } from '~/types'
import { CATEGORIES } from '~/composables/useData'

/**
 * Filtering, sorting and facet counting for the catalogue.
 *
 * Extracted from pages/catalogus/index.vue while fixing issue #34 so the
 * counting rule is testable. Facet counts are the kind of logic that looks right
 * and is quietly wrong — the original showed "Productie 2" beside
 * "0 resultaten" — and it cannot be checked at all while it lives inside a page.
 */

/** Lifecycle labels the catalogue filters on, in display order. */
export const ALL_STATUSES = ['Productie', 'Pilot', 'Testfase', 'Concept', 'Uitfaserend'] as const

/** The filter dimensions, so a facet can exclude its own when counting. */
export type Dimension = 'category' | 'status' | 'criteria' | 'text'

export type SortOrder = 'name' | 'installs' | 'bijgewerkt'

export interface CatalogueFilterState {
  categories: string[]
  statuses: string[]
  criteria: string[]
  text: string
}

/**
 * Whether an app survives the given filters, optionally ignoring one dimension.
 *
 * `except` is what makes facet counts correct. Three candidate rules:
 *
 *   - count over the whole dataset — the original bug: a 0-result search still
 *     advertised "Productie 2";
 *   - count over the fully filtered set — every unselected option collapses to 0
 *     as soon as one is picked, so the facet list stops being usable to refine;
 *   - count over everything *except* this facet's own dimension — answers "how
 *     many results would I get if I picked this too", and still drops every
 *     count to 0 when a text query matches nothing.
 *
 * The third is what faceted search means, and what this implements.
 */
export function matchesFilters(app: App, f: CatalogueFilterState, except?: Dimension): boolean {
  if (except !== 'category'
    && f.categories.length
    && !f.categories.includes(app.category)) return false

  if (except !== 'status'
    && f.statuses.length
    && !f.statuses.includes(app.status)) return false

  if (except !== 'criteria' && f.criteria.length) {
    for (const key of f.criteria) {
      if (app.scores[key] !== 'pass') return false
    }
  }

  if (except !== 'text' && f.text.trim()) {
    const q = f.text.trim().toLowerCase()
    if (
      !app.name.toLowerCase().includes(q)
      && !app.tagline.toLowerCase().includes(q)
      && !app.tags.some(t => t.toLowerCase().includes(q))
      && !app.eigenaar.toLowerCase().includes(q)
    ) return false
  }

  return true
}

/** Counts apps per category, excluding the category dimension itself. */
export function countByCategory(apps: App[], f: CatalogueFilterState): Record<string, number> {
  const pool = apps.filter(a => matchesFilters(a, f, 'category'))
  return Object.fromEntries(
    CATEGORIES.map(c => [c.slug, pool.filter(a => a.category === c.slug).length]),
  )
}

/** Counts apps per lifecycle status, excluding the status dimension itself. */
export function countByStatus(apps: App[], f: CatalogueFilterState): Record<string, number> {
  const pool = apps.filter(a => matchesFilters(a, f, 'status'))
  return Object.fromEntries(
    ALL_STATUSES.map(s => [s, pool.filter(a => a.status === s).length]),
  )
}

/** Sorts a copy; never mutates the input. */
export function sortApps(apps: App[], order: SortOrder): App[] {
  return [...apps].sort((a, b) => {
    if (order === 'name') return a.name.localeCompare(b.name)
    if (order === 'installs') return b.installs - a.installs
    return a.bijgewerkt.localeCompare(b.bijgewerkt)
  })
}

/**
 * Toggles a value in a string list, in place.
 *
 * Takes the unwrapped array, because that is what a template hands over.
 * Mutating it is still reactive — a ref wraps arrays with reactive().
 */
export function toggleValue(list: string[], value: string): void {
  const i = list.indexOf(value)
  if (i >= 0) list.splice(i, 1)
  else list.push(value)
}

/** Reactive catalogue filter state bound to a list of apps. */
export function useCatalogueFilters(apps: Ref<App[] | null>) {
  const selectedCategories = ref<string[]>([])
  const selectedStatuses   = ref<string[]>([])
  const selectedCriteria   = ref<string[]>([])
  const textQuery          = ref('')
  const sortOrder          = ref<SortOrder>('installs')

  const all = computed<App[]>(() => apps.value ?? [])

  const filters = computed<CatalogueFilterState>(() => ({
    categories: selectedCategories.value,
    statuses:   selectedStatuses.value,
    criteria:   selectedCriteria.value,
    text:       textQuery.value,
  }))

  const filteredApps = computed(() => all.value.filter(a => matchesFilters(a, filters.value)))
  const sortedApps   = computed(() => sortApps(filteredApps.value, sortOrder.value))

  const categoryCounts = computed(() => countByCategory(all.value, filters.value))
  const statusCounts   = computed(() => countByStatus(all.value, filters.value))

  const hasFilters = computed(() =>
    selectedCategories.value.length > 0
    || selectedStatuses.value.length > 0
    || selectedCriteria.value.length > 0
    || textQuery.value.trim().length > 0,
  )

  function clearFilters() {
    selectedCategories.value = []
    selectedStatuses.value   = []
    selectedCriteria.value   = []
    textQuery.value          = ''
  }

  return {
    selectedCategories, selectedStatuses, selectedCriteria, textQuery, sortOrder,
    filteredApps, sortedApps, categoryCounts, statusCounts,
    hasFilters, clearFilters, toggle: toggleValue,
  }
}
