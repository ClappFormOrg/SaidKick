import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import DashboardSidebar from '~/components/DashboardSidebar.vue'
import { useAuth } from '~/composables/useAuth'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockMeResponse } from '../../utils/mock'

// The entries each role sees, from the API route table
// (API/internal/handlers/handlers.go) and the page guards.
const ORG_SLUG = 'acme'
const ENTRIES = {
  'Mijn dashboard': '/dashboard',
  'Mijn apps': '/dashboard/my-apps',
  Clusters: '/dashboard/clusters',
  Beoordelingen: '/dashboard/audit',
  'API-sleutels': '/dashboard/api-keys',
  Organisatie: `/dashboard/org/${ORG_SLUG}`,
  Huisstijl: '/dashboard/branding',
} as const
type Entry = keyof typeof ENTRIES

const EXPECTED: Record<string, Entry[]> = {
  developer: ['Mijn dashboard', 'Mijn apps', 'API-sleutels'],
  deployer: ['Mijn dashboard', 'Mijn apps', 'API-sleutels'],
  auditor: ['Mijn dashboard', 'Mijn apps', 'Beoordelingen', 'API-sleutels'],
  cluster_admin: ['Mijn dashboard', 'Mijn apps', 'Clusters'],
  org_admin: ['Mijn dashboard', 'Mijn apps', 'Organisatie'],
  platform_admin: ['Mijn dashboard', 'Mijn apps', 'Clusters', 'Organisatie', 'Huisstijl'],
  app_user: ['Mijn apps'],
}

let path = '/dashboard'

const stubs = {
  NuxtLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

function signIn(roles: string[], orgSlug: string | undefined = ORG_SLUG) {
  localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles, org_slug: orgSlug })))
}

function labels(w: ReturnType<typeof mount>): string[] {
  return w.findAll('a').map(a => a.text())
}

beforeEach(() => {
  localStorage.clear()
  path = '/dashboard'
  vi.stubGlobal('useAuth', useAuth)
  vi.stubGlobal('useRoute', () => ({ path }))
})

describe('DashboardSidebar role filtering', () => {
  it.each(Object.entries(EXPECTED))('shows %s exactly the entries the API admits', (role, expected) => {
    signIn([role])
    const w = mount(DashboardSidebar, { global: { stubs } })
    expect(labels(w)).toEqual(expected)
    for (const a of w.findAll('a')) {
      expect(a.attributes('href')).toBe(ENTRIES[a.text() as Entry])
    }
  })

  it('combines the entries of several roles without duplicates', () => {
    signIn(['developer', 'auditor', 'app_user'])
    const w = mount(DashboardSidebar, { global: { stubs } })
    expect(labels(w)).toEqual(['Mijn dashboard', 'Mijn apps', 'Beoordelingen', 'API-sleutels'])
  })

  it('shows no entries before the profile is known', () => {
    const w = mount(DashboardSidebar, { global: { stubs } })
    expect(w.findAll('a')).toHaveLength(0)
  })

  it('shows Mijn apps to a signed-in user without any role', () => {
    signIn([])
    const w = mount(DashboardSidebar, { global: { stubs } })
    expect(labels(w)).toEqual(['Mijn dashboard', 'Mijn apps'])
  })

  it('hides Organisatie from an org admin without an organisation', () => {
    signIn(['org_admin'], '')
    const w = mount(DashboardSidebar, { global: { stubs } })
    expect(labels(w)).not.toContain('Organisatie')
  })
})

describe('DashboardSidebar active entry', () => {
  it('marks /dashboard only on the dashboard itself', () => {
    signIn(['platform_admin'])
    path = '/dashboard/clusters/c1'
    const w = mount(DashboardSidebar, { global: { stubs } })
    const current = w.findAll('a[aria-current="page"]')
    expect(current.map(a => a.text())).toEqual(['Clusters'])
  })

  it('does not match a sibling route that shares a prefix', () => {
    signIn(['developer'])
    path = '/dashboard/api-keys-archive'
    const w = mount(DashboardSidebar, { global: { stubs } })
    expect(w.findAll('a[aria-current="page"]')).toHaveLength(0)
  })

  it('shows a count badge for an entry the page feeds', () => {
    signIn(['auditor'])
    const w = mount(DashboardSidebar, { props: { counts: { '/dashboard/audit': 3 } }, global: { stubs } })
    expect(w.findAll('a').find(a => a.text().startsWith('Beoordelingen'))!.text()).toBe('Beoordelingen3')
  })
})
