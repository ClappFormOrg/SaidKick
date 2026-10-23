import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, ref, Suspense } from 'vue'
import { useAuth } from '~/composables/useAuth'
import { useAsyncAction } from '~/composables/useAsyncAction'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockMeResponse } from '../../utils/mock'

import ClusterDetail from '~/pages/dashboard/clusters/[id].vue'

// The "Inloggegevens vernieuwen" control on the cluster detail page follows
// the API rule for POST /clusters/:id/rotate-credential: a platform admin, or
// a cluster admin of the organisation that owns the cluster.

const OWNER_ORG = 'org-owner'
const OTHER_ORG = 'org-other'
const ROTATE_URL = '/api/v1/clusters/c1/rotate-credential'

const baseCluster = {
  id: 'c1', name: 'prod-01', description: '', scope: 'org', state: 'ready',
  agent_version: '1.0.0', last_heartbeat_at: '2026-01-01T00:00:00Z', registered_at: '2026-01-01T00:00:00Z',
  orchestrator_cluster_name: 'prod-01', owning_organization_id: OWNER_ORG,
  agent_cert_not_after: '2027-03-15T12:00:00Z', agent_cert_rotation_requested_at: null as string | null,
}

let cluster = { ...baseCluster }
let calls: { method: string; url: string }[] = []
let rotateFails = false

const fakeFetch = vi.fn(async (url: string, opts: { method?: string } = {}) => {
  const method = (opts.method ?? 'GET').toUpperCase()
  calls.push({ method, url })
  if (method === 'POST' && url === ROTATE_URL) {
    if (rotateFails) throw { status: 503, data: { message: 'Vault PKI onbereikbaar' } }
    return { rotation_requested_at: '2026-10-11T10:00:00Z' }
  }
  if (method === 'GET' && url === '/api/v1/clusters/c1') return { ...cluster }
  return { items: [] }
})

beforeEach(() => {
  localStorage.clear()
  calls = []
  rotateFails = false
  cluster = { ...baseCluster }
  vi.stubGlobal('$fetch', fakeFetch)
  vi.stubGlobal('useAuth', useAuth)
  vi.stubGlobal('useAsyncAction', useAsyncAction)
  vi.stubGlobal('definePageMeta', () => {})
  vi.stubGlobal('useHead', () => {})
  vi.stubGlobal('useRoute', () => ({ params: { id: 'c1' }, query: {} }))
  vi.stubGlobal('useAsyncData', async <T>(_key: string, fn: () => Promise<T>) => {
    const data = ref<T | null>(null)
    const error = ref<unknown>(null)
    try {
      data.value = await fn()
    } catch (err) {
      error.value = err
    }
    return { data, error, refresh: async () => {}, pending: ref(false) }
  })
  window.confirm = () => true
})

const slotStub = (tag: string) => defineComponent({
  setup(_, { slots }) {
    return () => h(tag, slots.default?.())
  },
})

async function mountAs(roles: string[], orgId: string): Promise<VueWrapper> {
  localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles, org_id: orgId })))
  const Wrapper = defineComponent({
    render: () => h(Suspense, null, { default: () => h(ClusterDetail) }),
  })
  const w = mount(Wrapper, { global: { stubs: { NuxtLink: slotStub('a') } }, attachTo: document.body })
  await flushPromises()
  return w
}

const rotateButton = (w: VueWrapper) => w.find('[data-testid="rotate-credential"]')

describe('cluster credential rotation control', () => {
  const CASES: { name: string; roles: string[]; org: string; visible: boolean }[] = [
    { name: 'platform_admin of another org',   roles: ['platform_admin'], org: OTHER_ORG, visible: true },
    { name: 'cluster_admin of the owning org', roles: ['cluster_admin'],  org: OWNER_ORG, visible: true },
    { name: 'cluster_admin of another org',    roles: ['cluster_admin'],  org: OTHER_ORG, visible: false },
    { name: 'developer of the owning org',     roles: ['developer'],      org: OWNER_ORG, visible: false },
    { name: 'deployer of the owning org',      roles: ['deployer'],       org: OWNER_ORG, visible: false },
    { name: 'auditor of the owning org',       roles: ['auditor'],        org: OWNER_ORG, visible: false },
    { name: 'org_admin of the owning org',     roles: ['org_admin'],      org: OWNER_ORG, visible: false },
  ]
  it.each(CASES)('$name: visible=$visible', async ({ roles, org, visible }) => {
    const w = await mountAs(roles, org)
    expect(rotateButton(w).exists()).toBe(visible)
    if (visible) expect(rotateButton(w).text()).toBe('Inloggegevens vernieuwen')
    w.unmount()
  })

  it('is hidden from a cluster_admin when the cluster has no owning org', async () => {
    cluster = { ...baseCluster, scope: 'public', owning_organization_id: undefined as unknown as string }
    const w = await mountAs(['cluster_admin'], OWNER_ORG)
    expect(rotateButton(w).exists()).toBe(false)
    w.unmount()
  })

  it('shows the certificate expiry to every role', async () => {
    const w = await mountAs(['developer'], OTHER_ORG)
    expect(w.text()).toContain('Certificaat geldig tot')
    expect(w.find('[data-testid="cert-not-after"]').text()).toContain('2027')
    expect(w.find('[data-testid="rotation-pending"]').exists()).toBe(false)
    w.unmount()
  })

  it('falls back to agent_cert_expires_at while the expiry is not recorded yet', async () => {
    cluster = { ...baseCluster, agent_cert_not_after: null as unknown as string, agent_cert_expires_at: '2026-12-01T00:00:00Z' } as typeof baseCluster
    const w = await mountAs(['developer'], OTHER_ORG)
    expect(w.find('[data-testid="cert-not-after"]').text()).toContain('2026')
    w.unmount()
  })

  it('shows a pending rotation', async () => {
    cluster = { ...baseCluster, agent_cert_rotation_requested_at: '2026-10-11T09:00:00Z' }
    const w = await mountAs(['developer'], OTHER_ORG)
    expect(w.find('[data-testid="rotation-pending"]').text()).toBe('Vernieuwing aangevraagd')
    w.unmount()
  })

  it('asks for confirmation and requests the rotation', async () => {
    const confirm = vi.fn(() => true)
    window.confirm = confirm
    const w = await mountAs(['cluster_admin'], OWNER_ORG)
    await rotateButton(w).trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledOnce()
    expect(calls).toContainEqual({ method: 'POST', url: ROTATE_URL })
    expect(w.find('[data-testid="rotation-pending"]').text()).toBe('Vernieuwing aangevraagd')
    w.unmount()
  })

  it('sends nothing when the confirmation is declined', async () => {
    window.confirm = () => false
    const w = await mountAs(['platform_admin'], OTHER_ORG)
    await rotateButton(w).trigger('click')
    await flushPromises()
    expect(calls.some(c => c.url === ROTATE_URL)).toBe(false)
    expect(w.find('[data-testid="rotation-pending"]').exists()).toBe(false)
    w.unmount()
  })

  it('shows the error when the request fails', async () => {
    rotateFails = true
    const w = await mountAs(['platform_admin'], OTHER_ORG)
    await rotateButton(w).trigger('click')
    await flushPromises()
    expect(w.find('[role="alert"]').exists()).toBe(true)
    expect(w.find('[data-testid="rotation-pending"]').exists()).toBe(false)
    w.unmount()
  })
})
