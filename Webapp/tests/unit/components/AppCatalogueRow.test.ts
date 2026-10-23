import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import AppCatalogueRow from '~/components/AppCatalogueRow.vue'
import UiCategoryIcon from '~/components/UiCategoryIcon.vue'
import type { App } from '~/types'

function makeApp(overrides: Partial<App> = {}): App {
  return {
    id: 'a1', slug: 'inspraakhulp', name: 'Inspraakhulp', tagline: 'tag',
    description: '', longDescription: [], category: 'chatbot', status: 'Productie',
    tags: ['chatbot'], eigenaar: 'Example-Vendor', medeontwikkelaars: [], versie: '1.0.0',
    bijgewerkt: '18 juni 2026', licentie: 'EUPL-1.2', repo: 'Example-Vendor/inspraakhulp',
    scores: { dpia: 'pass', bio: 'pass', a11y: 'pass', risk: 'pass', oss: 'pass', local: 'pass' },
    riskCategory: 'Beperkt risico', installs: 3,
    ...overrides,
  } as App
}

const stubs = {
  NuxtLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  UiStatusPill: { props: ['status'], template: '<span class="ui-status">{{ status }}</span>' },
  UiScoreDots: { props: ['scores'], template: '<span class="ui-dots" />' },
  UiCategoryIcon,
}

describe('AppCatalogueRow', () => {
  it('shows "6/6 voldoet" when all criteria pass (happy path)', () => {
    const w = mount(AppCatalogueRow, { props: { app: makeApp() }, global: { stubs } })
    expect(w.text()).toContain('6/6 voldoet')
  })

  it('counts only passing criteria when some fail/pending (unhappy path)', () => {
    const scores: App['scores'] = { dpia: 'pass', bio: 'fail', a11y: 'pending', risk: 'pass', oss: 'pass', local: 'pass' }
    const w = mount(AppCatalogueRow, { props: { app: makeApp({ scores }) }, global: { stubs } })
    expect(w.text()).toContain('4/6 voldoet')
  })

  it('links the app name to its detail page', () => {
    const w = mount(AppCatalogueRow, { props: { app: makeApp() }, global: { stubs } })
    expect(w.get('a').attributes('href')).toBe('/catalogus/inspraakhulp')
  })

  it('labels the category icon with the resolved category title', () => {
    const w = mount(AppCatalogueRow, { props: { app: makeApp({ category: 'chatbot' }) }, global: { stubs } })
    expect(w.get('[role="img"]').attributes('aria-label')).toBe('Chatbots')
  })

  it('falls back to the raw slug on the category icon when unknown', () => {
    const w = mount(AppCatalogueRow, { props: { app: makeApp({ category: 'mystery' }) }, global: { stubs } })
    expect(w.get('[role="img"]').attributes('aria-label')).toBe('mystery')
  })

  it('renders owner, updated date and install count', () => {
    const w = mount(AppCatalogueRow, { props: { app: makeApp() }, global: { stubs } })
    expect(w.text()).toContain('Example-Vendor')
    expect(w.text()).toContain('18 juni 2026')
    expect(w.text()).toContain('3 installaties')
  })
})
