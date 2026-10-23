import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, ref, Suspense, type Component } from 'vue'
import { useAuth } from '~/composables/useAuth'
import { useAsyncAction } from '~/composables/useAsyncAction'
import { useRoleGuard } from '~/composables/useRoleGuard'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockAppDetailAPI, createMockMeResponse } from '../../utils/mock'

import ClustersIndex from '~/pages/dashboard/clusters/index.vue'
import ClusterDetail from '~/pages/dashboard/clusters/[id].vue'
import DeploymentDetail from '~/pages/dashboard/deployments/[id].vue'
import DeploymentNew from '~/pages/dashboard/deployments/new.vue'
import OrgMembers from '~/pages/dashboard/org/[slug].vue'
import AppNew from '~/pages/dashboard/apps/new.vue'
import AppDetail from '~/pages/dashboard/apps/[slug]/index.vue'
import AppNewVersion from '~/pages/dashboard/apps/[slug]/new-version.vue'
import Dashboard from '~/pages/dashboard/index.vue'
import AuditQueue from '~/pages/dashboard/audit/index.vue'
import MyApps from '~/pages/dashboard/my-apps/index.vue'
import Branding from '~/pages/dashboard/branding/index.vue'

// ─── Role oracle: the API route table (API/internal/handlers/handlers.go) ────
// Any route not listed only needs an authenticated user.

const ROLES = ['developer', 'deployer', 'auditor', 'cluster_admin', 'org_admin', 'platform_admin', 'app_user'] as const
type Role = typeof ROLES[number]

const CLUSTER_WRITE: Role[] = ['cluster_admin', 'platform_admin']
const ORG_MEMBERS: Role[]   = ['org_admin', 'platform_admin']

const ROUTE_TABLE: { method: string; path: RegExp; roles: Role[] }[] = [
  { method: 'POST',   path: /^\/api\/v1\/apps$/,                                  roles: ['developer'] },
  { method: 'GET',    path: /^\/api\/v1\/apps\/[^/]+\/versions$/,                 roles: ['developer', 'auditor'] },
  { method: 'POST',   path: /^\/api\/v1\/apps\/[^/]+\/(versions|submit)$/,        roles: ['developer'] },
  { method: '*',      path: /^\/api\/v1\/audit-submissions/,                      roles: ['auditor'] },
  { method: 'POST',   path: /^\/api\/v1\/clusters$/,                              roles: CLUSTER_WRITE },
  { method: 'PATCH',  path: /^\/api\/v1\/clusters\/[^/]+$/,                       roles: CLUSTER_WRITE },
  { method: 'DELETE', path: /^\/api\/v1\/clusters\/[^/]+$/,                       roles: CLUSTER_WRITE },
  { method: 'POST',   path: /^\/api\/v1\/clusters\/[^/]+\/rotate-credential$/,    roles: CLUSTER_WRITE },
  { method: 'GET',    path: /^\/api\/v1\/clusters\/[^/]+\/access$/,               roles: ['platform_admin'] },
  { method: 'POST',   path: /^\/api\/v1\/clusters\/[^/]+\/(scope|access)$/,       roles: ['platform_admin'] },
  { method: 'DELETE', path: /^\/api\/v1\/clusters\/[^/]+\/access\/[^/]+$/,        roles: ['platform_admin'] },
  { method: 'POST',   path: /^\/api\/v1\/deployments$/,                           roles: ['deployer'] },
  { method: 'POST',   path: /^\/api\/v1\/deployments\/[^/]+\/(upgrade|rollback)$/, roles: ['deployer'] },
  { method: 'DELETE', path: /^\/api\/v1\/deployments\/[^/]+$/,                    roles: ['deployer'] },
  { method: 'POST',   path: /^\/api\/v1\/orgs$/,                                  roles: ['platform_admin'] },
  { method: 'POST',   path: /^\/api\/v1\/orgs\/[^/]+\/oidc$/,                     roles: ['platform_admin'] },
  { method: 'GET',    path: /^\/api\/v1\/orgs\/[^/]+\/members$/,                  roles: ORG_MEMBERS },
  { method: 'POST',   path: /^\/api\/v1\/orgs\/[^/]+\/members\/[^/]+\/roles$/,    roles: ORG_MEMBERS },
  { method: 'POST',   path: /^\/api\/v1\/advisories$/,                            roles: ['platform_admin'] },
  { method: 'GET',    path: /^\/api\/v1\/audit-log$/,                             roles: ['platform_admin'] },
  { method: 'POST',   path: /^\/api\/v1\/api-keys$/,                              roles: ['developer', 'deployer', 'auditor'] },
  { method: 'PUT',    path: /^\/api\/v1\/branding$/,                              roles: ['platform_admin'] },
  { method: 'DELETE', path: /^\/api\/v1\/branding(\/logo)?$/,                    roles: ['platform_admin'] },
  { method: 'POST',   path: /^\/api\/v1\/branding\/logo$/,                        roles: ['platform_admin'] },
]

function requiredRoles(method: string, url: string): Role[] | null {
  const row = ROUTE_TABLE.find(r => (r.method === '*' || r.method === method) && r.path.test(url))
  return row ? row.roles : null
}

// ─── Fake API ────────────────────────────────────────────────────────────────

const CLUSTER = {
  id: 'c1', name: 'prod-01', description: '', scope: 'restricted', state: 'ready',
  agent_version: '1.0.0', last_heartbeat_at: '2026-01-01T00:00:00Z', registered_at: '2026-01-01T00:00:00Z',
}
const DEPLOYMENT = {
  id: 'd1', app_id: 'a1', app_version_id: 'v1', cluster_id: 'c1', namespace: 'prod',
  state: 'ready', last_state_change_at: '2026-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z',
  values: {}, values_schema: { type: 'object', properties: {} },
}

const MY_APPS = {
  items: [
    {
      deployment_id: 'd1', app_slug: 'govchat', display_name: 'GovChat', categories: ['chat'], cluster_name: 'prod-01',
      availability: 'available', access: 'web', url: 'https://chat.acme.example.nl',
      maintainer: { name: 'Dana', email: 'dana@acme.example.nl' },
    },
    {
      deployment_id: 'd2', app_slug: 'indexer', display_name: 'Indexer', categories: [],
      availability: 'available', access: 'none', maintainer: { vendor_name: 'Vendor', vendor_contact: 'help@vendor.example.nl' },
    },
    {
      deployment_id: 'd3', app_slug: 'pending', display_name: 'Pending app', categories: [],
      availability: 'preparing', access: 'web', maintainer: {},
    },
  ],
  total: 3,
}

const MEMBER = {
  id: 'u2', display_name: 'Bob', email: 'bob@example.com', roles: ['developer'], created_at: '2026-01-01T00:00:00Z',
}

function fixture(method: string, url: string): unknown {
  if (method === 'GET') {
    if (url === '/api/v1/me') return createMockMeResponse({ roles: [currentRole], org_slug: 'acme' })
    if (url === '/api/v1/clusters') return { items: [CLUSTER], total: 1 }
    if (url === '/api/v1/clusters/c1') return { ...CLUSTER }
    if (url === '/api/v1/clusters/c1/access') {
      return { items: [{ organization_id: 'o2', organization_name: 'Other', granted_at: '2026-01-01T00:00:00Z' }] }
    }
    if (url === '/api/v1/deployments/d1') return { ...DEPLOYMENT }
    if (url === '/api/v1/orgs/acme/members') {
      return { items: [{ ...MEMBER, roles: [...MEMBER.roles] }] }
    }
    if (url === '/api/v1/apps/my-app') return createMockAppDetailAPI({ slug: 'my-app', state: 'draft' })
    if (url === '/api/v1/my-apps') return MY_APPS
    return { items: [], total: 0 }
  }
  if (method === 'POST' && url === '/api/v1/clusters') {
    return { cluster: CLUSTER, installer: { helm_command: 'helm install agent', join_token_jwt: 'jwt' } }
  }
  if (method === 'POST' && url === '/api/v1/apps') return { slug: 'my-app', id: 'a1' }
  if (method === 'POST' && url === '/api/v1/deployments') return { id: 'd2' }
  return {}
}

interface Forbidden { phase: 'load' | 'click'; method: string; url: string }

let currentRole: Role = 'developer'
let phase: 'load' | 'click' = 'load'
let forbidden: Forbidden[] = []
let calls: { method: string; url: string; body?: unknown }[] = []

const fakeFetch = vi.fn(async (url: string, opts: { method?: string; body?: unknown } = {}) => {
  const method = (opts.method ?? 'GET').toUpperCase()
  calls.push({ method, url, body: opts.body })
  const roles = requiredRoles(method, url)
  if (roles && !roles.includes(currentRole)) {
    forbidden.push({ phase, method, url })
    throw { status: 403, data: { message: 'forbidden' } }
  }
  return fixture(method, url)
})

// ─── Nuxt runtime stubs used by the pages ────────────────────────────────────

const mockNavigate = vi.fn()
let currentRoute: { params: Record<string, string>; query: Record<string, string> } = { params: {}, query: {} }

class FakeEventSource {
  addEventListener() {}
  close() {}
  onerror: (() => void) | null = null
}

beforeEach(() => {
  localStorage.clear()
  forbidden = []
  calls = []
  phase = 'load'
  mockNavigate.mockReset()

  vi.stubGlobal('$fetch', fakeFetch)
  vi.stubGlobal('navigateTo', mockNavigate)
  vi.stubGlobal('useAuth', useAuth)
  vi.stubGlobal('useAsyncAction', useAsyncAction)
  vi.stubGlobal('useRoleGuard', useRoleGuard)
  vi.stubGlobal('definePageMeta', () => {})
  vi.stubGlobal('useHead', () => {})
  vi.stubGlobal('useRoute', () => currentRoute)
  vi.stubGlobal('useRouter', () => ({ push: vi.fn() }))
  vi.stubGlobal('createError', (e: Record<string, unknown>) => Object.assign(new Error(String(e.statusMessage ?? '')), e))
  vi.stubGlobal('showError', vi.fn())
  vi.stubGlobal('EventSource', FakeEventSource)
  vi.stubGlobal('useAsyncData', async <T>(
    _key: string,
    fn: () => Promise<T>,
    opts: { default?: () => T } = {},
  ) => {
    const data = ref<T | null>(opts.default?.() ?? null)
    const error = ref<unknown>(null)
    const refresh = async () => {
      try {
        data.value = await fn()
      } catch (err) {
        error.value = err
      }
    }
    await refresh()
    return { data, error, refresh, pending: ref(false) }
  })

  window.confirm = () => true
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText: async () => {} },
    configurable: true,
  })
})

// ─── Mounting and exercising pages ───────────────────────────────────────────

const slotStub = (tag: string) => defineComponent({
  setup(_, { slots }) {
    return () => h(tag, slots.default?.())
  },
})

const STUBS = {
  NuxtLink: slotStub('a'),
  GovnlButton: slotStub('a'),
  UiStatusPill: slotStub('span'),
  UiTag: slotStub('span'),
  DeployValuesForm: slotStub('div'),
}

interface PageCase {
  name: string
  page: Component
  route?: { params?: Record<string, string>; query?: Record<string, string> }
}

const PAGES: PageCase[] = [
  { name: 'clusters/index', page: ClustersIndex },
  { name: 'clusters/[id]', page: ClusterDetail, route: { params: { id: 'c1' } } },
  { name: 'deployments/[id]', page: DeploymentDetail, route: { params: { id: 'd1' } } },
  {
    name: 'deployments/new',
    page: DeploymentNew,
    route: { query: { app_id: 'a1', app_version_id: 'v1', app_name: 'My app', app_slug: 'my-app' } },
  },
  { name: 'org/[slug]', page: OrgMembers, route: { params: { slug: 'acme' } } },
  { name: 'apps/new', page: AppNew },
  { name: 'apps/[slug]', page: AppDetail, route: { params: { slug: 'my-app' } } },
  { name: 'apps/[slug]/new-version', page: AppNewVersion, route: { params: { slug: 'my-app' } } },
  { name: 'dashboard', page: Dashboard },
  { name: 'my-apps', page: MyApps },
  { name: 'branding', page: Branding },
]

function pageCase(name: string): PageCase {
  return PAGES.find(p => p.name === name)!
}

async function mountAs(role: Role, c: PageCase): Promise<VueWrapper> {
  currentRole = role
  currentRoute = { params: c.route?.params ?? {}, query: c.route?.query ?? {} }
  localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles: [role], org_slug: 'acme' })))
  const Wrapper = defineComponent({
    render: () => h(Suspense, null, { default: () => h(c.page) }),
  })
  // Attached so isConnected tells the click driver which controls are still rendered.
  const w = mount(Wrapper, { global: { stubs: STUBS }, attachTo: document.body })
  await flushPromises()
  return w
}

async function fillInputs(w: VueWrapper) {
  for (const input of w.findAll('input')) {
    const el = input.element as HTMLInputElement
    if (!el.isConnected || el.readOnly || el.type === 'checkbox' || el.type === 'file') continue
    await input.setValue(el.type === 'number' ? '5' : 'test-value')
  }
  for (const select of w.findAll('select')) {
    const el = select.element as HTMLSelectElement
    const option = Array.from(el.options).find(o => o.value && !o.disabled)
    if (option) await select.setValue(option.value)
  }
}

// Clicks every rendered button (and submits every form), including the ones
// that only appear after an earlier click, such as a create form's submit.
// Cancel/close buttons are skipped so opened forms stay open.
async function exerciseControls(w: VueWrapper) {
  phase = 'click'
  const clicked = new WeakSet<Element>()
  for (let pass = 0; pass < 5; pass++) {
    await fillInputs(w)
    const fresh = w.findAll('button').filter(b => !clicked.has(b.element))
    if (!fresh.length) break
    for (const button of fresh) {
      clicked.add(button.element)
      if (!button.element.isConnected) continue
      if (/Annuleren|Sluiten/.test(button.text())) continue
      await button.trigger('click')
      await flushPromises()
    }
    for (const form of w.findAll('form')) {
      await form.trigger('submit')
      await flushPromises()
    }
  }
}

function buttonTexts(w: VueWrapper): string[] {
  return w.findAll('button').map(b => b.text().trim())
}

// ─── No rendered control produces a 403 ──────────────────────────────────────

describe('no rendered control produces a 403 for any single-role user', () => {
  for (const c of PAGES) {
    it.each(ROLES)(`${c.name} as %s`, async (role) => {
      const w = await mountAs(role, c)
      await exerciseControls(w)
      expect(forbidden.filter(f => f.phase === 'click')).toEqual([])
      w.unmount()
    })
  }
})

// Guards the check above against a driver that clicks nothing: for a role the
// API allows, exercising the page must reach its write endpoints.
describe('the click driver reaches write endpoints for permitted roles', () => {
  const CASES: { name: string; role: Role; expected: string[] }[] = [
    { name: 'clusters/index',          role: 'cluster_admin',  expected: ['POST /api/v1/clusters', 'DELETE /api/v1/clusters/c1'] },
    { name: 'clusters/[id]',           role: 'platform_admin', expected: ['PATCH /api/v1/clusters/c1', 'POST /api/v1/clusters/c1/access', 'DELETE /api/v1/clusters/c1/access/o2', 'POST /api/v1/clusters/c1/rotate-credential'] },
    { name: 'deployments/[id]',        role: 'deployer',       expected: ['POST /api/v1/deployments/d1/upgrade', 'POST /api/v1/deployments/d1/rollback', 'DELETE /api/v1/deployments/d1'] },
    { name: 'deployments/new',         role: 'deployer',       expected: ['POST /api/v1/deployments'] },
    { name: 'org/[slug]',              role: 'org_admin',      expected: ['POST /api/v1/orgs/acme/members/u2/roles'] },
    { name: 'apps/new',                role: 'developer',      expected: ['POST /api/v1/apps'] },
    { name: 'apps/[slug]',             role: 'developer',      expected: ['POST /api/v1/apps/my-app/submit'] },
    { name: 'apps/[slug]/new-version', role: 'developer',      expected: ['POST /api/v1/apps/my-app/versions'] },
  ]
  it.each(CASES)('$name as $role', async ({ name, role, expected }) => {
    const w = await mountAs(role, pageCase(name))
    await exerciseControls(w)
    const made = calls.map(c => `${c.method} ${c.url}`)
    for (const call of expected) expect(made).toContain(call)
    w.unmount()
  })
})

// The dashboard's audit queue is loaded for every role and is out of scope
// here, so the load-phase check covers only the pages this change guards.
describe('guarded pages issue no request that 403s while loading', () => {
  for (const c of PAGES.filter(p => p.name !== 'dashboard')) {
    it.each(ROLES)(`${c.name} as %s`, async (role) => {
      const w = await mountAs(role, c)
      expect(forbidden.filter(f => f.phase === 'load')).toEqual([])
      w.unmount()
    })
  }
})

// ─── Redirects for pages reached directly by URL ─────────────────────────────

describe('pages redirect a user who lacks the role', () => {
  const CASES: { name: string; allowed: Role[]; marker: string }[] = [
    { name: 'apps/new',                allowed: ['developer'],                   marker: '#installation-id' },
    { name: 'apps/[slug]/new-version', allowed: ['developer'],                   marker: '#git-ref' },
    { name: 'deployments/new',         allowed: ['deployer'],                    marker: '#namespace' },
    { name: 'org/[slug]',              allowed: ['org_admin', 'platform_admin'], marker: 'table' },
    { name: 'branding',                allowed: ['platform_admin'],              marker: '#brand-cta' },
  ]
  for (const c of CASES) {
    it.each(ROLES)(`${c.name} as %s`, async (role) => {
      const w = await mountAs(role, pageCase(c.name))
      if (c.allowed.includes(role)) {
        expect(mockNavigate).not.toHaveBeenCalledWith('/dashboard')
        expect(w.find(c.marker).exists()).toBe(true)
      } else {
        expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
        expect(w.find(c.marker).exists()).toBe(false)
      }
      w.unmount()
    })
  }

  it('org/[slug] does not request members for a role that cannot list them', async () => {
    const w = await mountAs('developer', pageCase('org/[slug]'))
    expect(calls.some(c => c.url.endsWith('/members'))).toBe(false)
    w.unmount()
  })
})

// ─── Acceptance: org_admin only ──────────────────────────────────────────────

describe('a user holding only org_admin', () => {
  it('sees no cluster create or delete control', async () => {
    const w = await mountAs('org_admin', pageCase('clusters/index'))
    expect(buttonTexts(w)).not.toContain('Cluster registreren')
    expect(buttonTexts(w)).not.toContain('Verwijderen')
    expect(w.text()).toContain('prod-01')
    w.unmount()
  })

  it('sees no deployment write actions', async () => {
    const w = await mountAs('org_admin', pageCase('deployments/[id]'))
    const texts = buttonTexts(w)
    for (const label of ['Herstarten', 'Configureren', 'Terugdraaien', 'Verwijderen']) {
      expect(texts).not.toContain(label)
    }
    expect(w.text()).toContain('d1')
    w.unmount()
  })

  it('sees no app-create form', async () => {
    const w = await mountAs('org_admin', pageCase('apps/new'))
    expect(w.find('form').exists()).toBe(false)
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
    w.unmount()
  })

  // The link to manage the organisation lives in the dashboard sidebar now;
  // DashboardSidebar.test.ts covers it for org_admin.
  it('sees no app-create entry point on the dashboard but sees the org deployments', async () => {
    const w = await mountAs('org_admin', pageCase('dashboard'))
    expect(w.text()).not.toContain('App toevoegen')
    expect(w.text()).not.toContain('Custom cluster Configuratie')
    expect(w.text()).toContain('Deployments (laatste 10)')
    w.unmount()
  })
})

// ─── Acceptance: platform_admin only ─────────────────────────────────────────

describe('a user holding only platform_admin', () => {
  it('sees cluster create and delete', async () => {
    const w = await mountAs('platform_admin', pageCase('clusters/index'))
    expect(buttonTexts(w)).toContain('Cluster registreren')
    expect(buttonTexts(w).some(t => t.includes('Verwijderen'))).toBe(true)
    w.unmount()
  })

  it('sees cluster access management and ingress editing', async () => {
    const w = await mountAs('platform_admin', pageCase('clusters/[id]'))
    expect(w.text()).toContain('Toegang — organisaties')
    expect(buttonTexts(w).some(t => t.includes('Verlenen'))).toBe(true)
    expect(buttonTexts(w).some(t => t.includes('Intrekken'))).toBe(true)
    expect(buttonTexts(w)).toContain('Opslaan')
    w.unmount()
  })

  it('does not see app creation', async () => {
    const form = await mountAs('platform_admin', pageCase('apps/new'))
    expect(form.find('form').exists()).toBe(false)
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
    form.unmount()

    const dash = await mountAs('platform_admin', pageCase('dashboard'))
    expect(dash.text()).not.toContain('App toevoegen')
    dash.unmount()

    const app = await mountAs('platform_admin', pageCase('apps/[slug]'))
    expect(buttonTexts(app)).not.toContain('Indienen voor beoordeling')
    app.unmount()
  })

  it('does not see audit actions', async () => {
    const dash = await mountAs('platform_admin', pageCase('dashboard'))
    expect(dash.text()).not.toContain('Beoordelingswachtrij')
    dash.unmount()

    mockNavigate.mockReset()
    const queue = await mountAs('platform_admin', { name: 'audit/index', page: AuditQueue })
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
    queue.unmount()
  })
})

// ─── Cluster scope/access stays platform_admin only ──────────────────────────

describe('cluster scope and access management', () => {
  it('is hidden from cluster_admin, who can still edit ingress defaults', async () => {
    const w = await mountAs('cluster_admin', pageCase('clusters/[id]'))
    expect(w.text()).not.toContain('Toegang — organisaties')
    expect(calls.some(c => c.url.endsWith('/access'))).toBe(false)
    expect(buttonTexts(w)).toContain('Opslaan')
    w.unmount()
  })

  it('hides the scope picker from cluster_admin when registering', async () => {
    const w = await mountAs('cluster_admin', pageCase('clusters/index'))
    await w.findAll('button').find(b => b.text().includes('Cluster registreren'))!.trigger('click')
    expect(w.find('#cluster-name').exists()).toBe(true)
    expect(w.find('#cluster-scope').exists()).toBe(false)
    w.unmount()
  })
})

// ─── Deployment actions keep the settled-state rule ──────────────────────────

describe('deployment write actions for a deployer', () => {
  it('shows every action on a settled deployment', async () => {
    const w = await mountAs('deployer', pageCase('deployments/[id]'))
    const texts = buttonTexts(w)
    for (const label of ['Herstarten', 'Configureren', 'Terugdraaien', 'Verwijderen']) {
      expect(texts).toContain(label)
    }
    w.unmount()
  })

  it('keeps hiding version actions while the deployment is not settled', async () => {
    const settled = DEPLOYMENT.state
    DEPLOYMENT.state = 'applying'
    try {
      const w = await mountAs('deployer', pageCase('deployments/[id]'))
      const texts = buttonTexts(w)
      expect(texts).not.toContain('Herstarten')
      expect(texts).not.toContain('Terugdraaien')
      expect(texts).toContain('Verwijderen')
      w.unmount()
    } finally {
      DEPLOYMENT.state = settled
    }
  })
})

// ─── Auditor is granted and revoked by platform admins only ──────────────────

describe('member editor auditor checkbox', () => {
  const TOOLTIP = 'Alleen een platformbeheerder kan de rol auditor toekennen of intrekken.'
  const CASES: { role: Role; locked: boolean }[] = [
    { role: 'org_admin',      locked: true },
    { role: 'platform_admin', locked: false },
  ]

  async function openEditor(role: Role, memberRoles: string[]) {
    MEMBER.roles = memberRoles
    const w = await mountAs(role, pageCase('org/[slug]'))
    await w.findAll('button').find(b => b.text().includes('Bewerken'))!.trigger('click')
    const checkbox = (value: string) => w.find<HTMLInputElement>(`input[type="checkbox"][value="${value}"]`).element
    return { w, checkbox }
  }

  afterEach(() => {
    MEMBER.roles = ['developer']
  })

  it.each(CASES)('as $role is disabled: $locked', async ({ role, locked }) => {
    const { w, checkbox } = await openEditor(role, ['developer'])
    expect(checkbox('auditor').disabled).toBe(locked)
    expect(checkbox('auditor').closest('label')!.getAttribute('title')).toBe(locked ? TOOLTIP : null)
    for (const other of ['developer', 'deployer', 'cluster_admin', 'org_admin']) {
      expect(checkbox(other).disabled).toBe(false)
    }
    w.unmount()
  })

  it.each(CASES)('as $role shows the member\'s current auditor state', async ({ role }) => {
    const holder = await openEditor(role, ['developer', 'auditor'])
    expect(holder.checkbox('auditor').checked).toBe(true)
    holder.w.unmount()

    const other = await openEditor(role, ['developer'])
    expect(other.checkbox('auditor').checked).toBe(false)
    other.w.unmount()
  })

  it('keeps an existing auditor role when an org_admin saves other changes', async () => {
    const { w } = await openEditor('org_admin', ['developer', 'auditor'])
    await w.find('input[type="checkbox"][value="deployer"]').setValue(true)
    await w.findAll('button').find(b => b.text().includes('Opslaan'))!.trigger('click')
    await flushPromises()
    const save = calls.find(c => c.method === 'POST' && c.url === '/api/v1/orgs/acme/members/u2/roles')
    expect(save?.body).toEqual({ roles: ['developer', 'auditor', 'deployer'] })
    w.unmount()
  })
})

// ─── Another organisation's page ─────────────────────────────────────────────

describe('org/[slug] for another organisation', () => {
  const other: PageCase = { name: 'org/[slug] (other)', page: OrgMembers, route: { params: { slug: 'other' } } }

  it('shows an org_admin a no-access state instead of an empty roster', async () => {
    const w = await mountAs('org_admin', other)
    expect(w.text()).toContain('Geen toegang tot deze organisatie')
    expect(w.text()).not.toContain('Geen leden gevonden')
    expect(calls.some(c => c.url.endsWith('/members'))).toBe(false)
    w.unmount()
  })

  it('still lets a platform admin manage any organisation', async () => {
    const w = await mountAs('platform_admin', other)
    expect(w.text()).not.toContain('Geen toegang tot deze organisatie')
    expect(calls.some(c => c.url === '/api/v1/orgs/other/members')).toBe(true)
    w.unmount()
  })
})

// ─── app_user ────────────────────────────────────────────────────────────────

describe('member editor Gebruiker checkbox', () => {
  afterEach(() => {
    MEMBER.roles = ['developer']
  })

  it.each(['org_admin', 'platform_admin'] as Role[])('lets %s grant app_user', async (role) => {
    MEMBER.roles = ['developer']
    const w = await mountAs(role, pageCase('org/[slug]'))
    await w.findAll('button').find(b => b.text().includes('Bewerken'))!.trigger('click')
    const box = w.find<HTMLInputElement>('input[type="checkbox"][value="app_user"]')
    expect(box.element.disabled).toBe(false)
    expect(box.element.closest('label')!.textContent).toContain('Gebruiker')
    await box.setValue(true)
    await w.findAll('button').find(b => b.text().includes('Opslaan'))!.trigger('click')
    await flushPromises()
    const save = calls.find(c => c.method === 'POST' && c.url === '/api/v1/orgs/acme/members/u2/roles')
    expect(save?.body).toEqual({ roles: ['developer', 'app_user'] })
    w.unmount()
  })

  it('shows Gebruiker for a member who holds app_user', async () => {
    MEMBER.roles = ['app_user']
    const w = await mountAs('org_admin', pageCase('org/[slug]'))
    expect(w.text()).toContain('Gebruiker')
    w.unmount()
  })
})

// ─── Deploy wizard reached without a chosen app ──────────────────────────────

describe('deployments/new without a chosen app', () => {
  it('prompts to pick an app instead of failing, and shows no form', async () => {
    const w = await mountAs('deployer', { name: 'deployments/new (empty)', page: DeploymentNew })
    expect(w.text()).toContain('Kies eerst een applicatie in de catalogus')
    expect(w.find('#namespace').exists()).toBe(false)
    expect(w.find('select').exists()).toBe(false)
    await exerciseControls(w)
    expect(calls.some(c => c.method === 'POST')).toBe(false)
    w.unmount()
  })
})

describe('dashboard landing for app_user', () => {
  it('sends a user holding only app_user to the launcher', async () => {
    const w = await mountAs('app_user', pageCase('dashboard'))
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard/my-apps')
    w.unmount()
  })

  it.each(ROLES.filter(r => r !== 'app_user'))('keeps %s on the dashboard', async (role) => {
    const w = await mountAs(role, pageCase('dashboard'))
    expect(mockNavigate).not.toHaveBeenCalledWith('/dashboard/my-apps')
    expect(w.text()).toContain('Welkom terug')
    w.unmount()
  })
})

describe('launcher page', () => {
  it.each(ROLES)('is open to %s without a redirect', async (role) => {
    const w = await mountAs(role, pageCase('my-apps'))
    expect(mockNavigate).not.toHaveBeenCalled()
    expect(calls.some(c => c.url === '/api/v1/my-apps')).toBe(true)
    expect(w.text()).toContain('GovChat')
    w.unmount()
  })

  it('opens a web app in a new tab', async () => {
    const w = await mountAs('app_user', pageCase('my-apps'))
    const open = w.findAll('a').find(a => a.text() === 'Openen')!
    expect(open.attributes('href')).toBe('https://chat.acme.example.nl')
    expect(open.attributes('target')).toBe('_blank')
    expect(open.attributes('rel')).toBe('noopener')
    expect(w.findAll('a').filter(a => a.text() === 'Openen')).toHaveLength(1)
    w.unmount()
  })

  it('names the maintainer of an app without a web interface', async () => {
    const w = await mountAs('app_user', pageCase('my-apps'))
    expect(w.text()).toContain('geen webinterface')
    const contact = w.findAll('a').find(a => a.text() === 'Vendor')!
    expect(contact.attributes('href')).toBe('mailto:help@vendor.example.nl?subject=Toegang%20tot%20Indexer')
    w.unmount()
  })

  it('says when a web app is not up yet', async () => {
    const w = await mountAs('app_user', pageCase('my-apps'))
    expect(w.text()).toContain('Nog niet beschikbaar om te openen.')
    w.unmount()
  })

  it('shows an empty state', async () => {
    const items = MY_APPS.items
    MY_APPS.items = []
    try {
      const w = await mountAs('app_user', pageCase('my-apps'))
      expect(w.text()).toContain('Nog geen applicaties')
      w.unmount()
    } finally {
      MY_APPS.items = items
    }
  })

  it('shows the API error message', async () => {
    fakeFetch.mockImplementationOnce(async () => { throw { status: 500, data: { message: 'database down' } } })
    const w = await mountAs('app_user', pageCase('my-apps'))
    expect(w.find('[role="alert"]').text()).toBe('database down')
    w.unmount()
  })
})
