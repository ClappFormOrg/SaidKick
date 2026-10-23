import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import AppCard from '~/components/AppCard.vue'
import type { App } from '~/types'

function makeApp(overrides: Partial<App> = {}): App {
  return {
    id: 'a1',
    slug: 'inspraakhulp',
    name: 'Inspraakhulp',
    tagline: 'WOO-documentclassificatie',
    description: '',
    longDescription: [],
    category: 'chatbot',
    status: 'Productie',
    tags: ['chatbot'],
    eigenaar: 'Example-Vendor',
    medeontwikkelaars: [],
    versie: '1.2.0',
    bijgewerkt: '18 juni 2026',
    licentie: 'EUPL-1.2',
    repo: 'Example-Vendor/inspraakhulp',
    scores: { dpia: 'pass', bio: 'pass', a11y: 'pass', risk: 'pass', oss: 'pass', local: 'pass' },
    riskCategory: 'Beperkt risico',
    installs: 0,
    ...overrides,
  } as App
}

const stubs = {
  NuxtLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  UiTypeBadge: { template: '<span class="ui-type"><slot /></span>' },
  UiStatusPill: { props: ['status'], template: '<span class="ui-status">{{ status }}</span>' },
  UiScoreDots: { props: ['scores'], template: '<span class="ui-dots" />' },
}

describe('AppCard', () => {
  it('links to the app detail page by slug', () => {
    const w = mount(AppCard, { props: { app: makeApp() }, global: { stubs } })
    expect(w.get('a').attributes('href')).toBe('/catalogus/inspraakhulp')
  })

  it('resolves a known category slug to its human title (happy path)', () => {
    const w = mount(AppCard, { props: { app: makeApp({ category: 'chatbot' }) }, global: { stubs } })
    expect(w.get('.ui-type').text()).toBe('Chatbots')
  })

  it('falls back to the raw category slug when unknown (unhappy path)', () => {
    const w = mount(AppCard, { props: { app: makeApp({ category: 'weird-slug' }) }, global: { stubs } })
    expect(w.get('.ui-type').text()).toBe('weird-slug')
  })

  it('renders name, tagline, owner and status', () => {
    const w = mount(AppCard, { props: { app: makeApp() }, global: { stubs } })
    expect(w.text()).toContain('Inspraakhulp')
    expect(w.text()).toContain('WOO-documentclassificatie')
    expect(w.text()).toContain('Example-Vendor')
    expect(w.get('.ui-status').text()).toBe('Productie')
  })

  it('has no per-category colour', () => {
    const w = mount(AppCard, { props: { app: makeApp({ category: 'agent' }) }, global: { stubs } })
    expect(w.get('a').attributes('style')).toBeUndefined()
  })

  it('shows the version when present and hides it when empty', () => {
    const withVer = mount(AppCard, { props: { app: makeApp({ versie: '2.0.0' }) }, global: { stubs } })
    expect(withVer.text()).toContain('2.0.0')
    const noVer = mount(AppCard, { props: { app: makeApp({ versie: '' }) }, global: { stubs } })
    expect(noVer.text()).not.toContain('versie ')
  })
})
