import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, ref, Suspense } from 'vue'
import { useAuth } from '~/composables/useAuth'
import type { AppDetailAPI } from '~/composables/useData'
import { createMockAppDetailAPI, createMockMeResponse } from '../../utils/mock'

import AppDetailPage from '~/pages/catalogus/[slug].vue'

// The public detail page presents vetted catalogue content, so everything it
// shows about versions and evidence must come from the API response.

let detail: AppDetailAPI

beforeEach(() => {
  localStorage.clear()
  vi.stubGlobal('$fetch', vi.fn(async (url: string) => {
    if (url === '/api/v1/apps/test-app') return detail
    throw { status: 404 }
  }))
  vi.stubGlobal('useAuth', useAuth)
  vi.stubGlobal('useHead', () => {})
  vi.stubGlobal('useRoute', () => ({ params: { slug: 'test-app' }, query: {} }))
  vi.stubGlobal('createError', (e: Record<string, unknown>) => Object.assign(new Error(String(e.statusMessage ?? '')), e))
  vi.stubGlobal('useAsyncData', async <T>(_key: string, fn: () => Promise<T>) => {
    const data = ref<T | null>(null)
    const error = ref<unknown>(null)
    try { data.value = await fn() } catch (err) { error.value = err }
    return { data, error }
  })
})

const slotStub = (tag: string) => defineComponent({
  setup(_, { slots }) {
    return () => h(tag, slots.default?.())
  },
})

async function mountPage(): Promise<VueWrapper> {
  const Wrapper = defineComponent({
    render: () => h(Suspense, null, { default: () => h(AppDetailPage) }),
  })
  const w = mount(Wrapper, {
    global: { stubs: { NuxtLink: slotStub('a'), GovnlButton: slotStub('a'), Teleport: true } },
  })
  await flushPromises()
  return w
}

async function openTab(w: VueWrapper, label: string) {
  const tab = w.findAll('button[role="tab"]').find(b => b.text() === label)!
  await tab.trigger('click')
}

describe('catalogue detail page without API-provided version or evidence data', () => {
  beforeEach(() => {
    detail = createMockAppDetailAPI({
      versie: undefined,
      audit_approved: true,
      manifest: { ...createMockAppDetailAPI().manifest!, spec: { categories: ['chatbot'] } },
    })
  })

  it('shows no invented version history', async () => {
    const w = await mountPage()
    await openTab(w, 'Versies')
    expect(w.text()).not.toContain('1.0.0')
    expect(w.text()).not.toContain('Initiële release')
    expect(w.text()).toContain('Geen versiegegevens beschikbaar')
  })

  it('links no evidence for criteria the manifest gives no document for', async () => {
    const w = await mountPage()
    await openTab(w, 'Toelatingseisen')
    expect(w.findAll('a[href="#"]').map(a => a.text())).not.toContain('Bekijken')
    expect(w.text()).not.toContain('Bekijken')
  })

  it('offers no DPIA download when the manifest names no DPIA', async () => {
    const w = await mountPage()
    expect(w.text()).not.toContain('DPIA downloaden')
  })
})

describe('catalogue detail primary action', () => {
  let roles: string[] | null

  beforeEach(() => {
    roles = null
    detail = createMockAppDetailAPI({ current_published_version_id: 'v1' })
    vi.stubGlobal('$fetch', vi.fn(async (url: string) => {
      if (url === '/api/v1/apps/test-app') return detail
      if (url === '/api/v1/me' && roles) return createMockMeResponse({ roles })
      throw { status: 401 }
    }))
  })

  function primary(w: VueWrapper, label: string) {
    return w.findAll('a').find(a => a.text() === label)
  }

  it('opens a running deployment of the viewer\'s organisation', async () => {
    roles = ['app_user']
    detail.my_deployments = [
      { deployment_id: 'd0', availability: 'preparing' },
      { deployment_id: 'd1', availability: 'available', url: 'https://chat.example.nl' },
    ]
    const w = await mountPage()
    const open = primary(w, 'Openen')!
    expect(open.attributes('href')).toBe('https://chat.example.nl')
    expect(open.attributes('target')).toBe('_blank')
    expect(open.attributes('rel')).toBe('noopener')
    expect(primary(w, 'Toegang aanvragen')).toBeUndefined()
  })

  it('offers install to a deployer when nothing runs', async () => {
    roles = ['deployer']
    detail.my_deployments = []
    const w = await mountPage()
    expect(primary(w, 'Installeren')).toBeDefined()
    expect(primary(w, 'Openen')).toBeUndefined()
  })

  it('offers install when a deployment exists but cannot be opened', async () => {
    roles = ['deployer']
    detail.my_deployments = [{ deployment_id: 'd0', availability: 'unavailable' }]
    const w = await mountPage()
    expect(primary(w, 'Openen')).toBeUndefined()
    expect(primary(w, 'Installeren')).toBeDefined()
  })

  it('links access requests to the vendor contact for anyone else', async () => {
    const w = await mountPage()
    expect(primary(w, 'Toegang aanvragen')!.attributes('href'))
      .toBe('mailto:contact@example.com?subject=Toegang%20tot%20Test%20App%20(Manifest)')
    expect(w.findAll('a[href="#"]').map(a => a.text())).not.toContain('Toegang aanvragen')
  })

  it('names the organisation administrator when no contact resolves', async () => {
    detail.manifest = { ...detail.manifest!, metadata: { ...detail.manifest!.metadata, contact: '' } }
    const w = await mountPage()
    expect(primary(w, 'Toegang aanvragen')).toBeUndefined()
    expect(w.text()).toContain('Toegang nodig? Vraag het uw organisatiebeheerder.')
  })
})

describe('catalogue detail page with API-provided data', () => {
  beforeEach(() => {
    detail = createMockAppDetailAPI({ versie: undefined, chart_version: '2.4.0', audit_approved: true })
  })

  it('shows the published version from the API', async () => {
    const w = await mountPage()
    await openTab(w, 'Versies')
    expect(w.text()).toContain('v2.4.0')
  })

  it('links DPIA evidence to the document the manifest names', async () => {
    const dpia = 'https://github.com/org/test-app/blob/main/docs/dpia.pdf'
    const w = await mountPage()
    expect(w.find(`a[href="${dpia}"]`).text()).toBe('DPIA downloaden')
    await openTab(w, 'Toelatingseisen')
    const evidence = w.findAll('a').filter(a => a.text() === 'Bekijken')
    expect(evidence.map(a => a.attributes('href'))).toEqual([dpia])
  })
})
