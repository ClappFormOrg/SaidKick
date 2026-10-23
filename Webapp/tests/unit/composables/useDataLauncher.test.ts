import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref } from 'vue'
import {
  useMyDeployedApps, availabilityLabel, maintainerLink, mapDetail, statusDotColor, type MyAppList,
} from '~/composables/useData'
import { createMockAppDetailAPI } from '../../utils/mock'

const mockFetch = vi.fn()

beforeEach(() => {
  mockFetch.mockReset()
  vi.stubGlobal('$fetch', mockFetch)
  vi.stubGlobal('useAsyncData', (_key: string, handler: () => Promise<unknown>, opts?: { default?: () => unknown }) => {
    const data = ref<unknown>(opts?.default?.() ?? null)
    const error = ref<unknown>(null)
    const ready = handler().then(v => { data.value = v }).catch(e => { error.value = e })
    return { data, error, ready }
  })
})

describe('useMyDeployedApps', () => {
  it('fetches /my-apps with the session cookie', async () => {
    const list: MyAppList = { items: [], total: 0 }
    mockFetch.mockResolvedValueOnce(list)
    const r = useMyDeployedApps() as unknown as { data: { value: MyAppList }; ready: Promise<void> }
    await r.ready
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/my-apps', expect.objectContaining({
      credentials: 'include', query: { page_size: 100 },
    }))
    expect(r.data.value).toEqual(list)
  })

  it('keeps the empty default when the request fails', async () => {
    mockFetch.mockRejectedValueOnce({ status: 500 })
    const r = useMyDeployedApps() as unknown as { data: { value: MyAppList }; error: { value: unknown }; ready: Promise<void> }
    await r.ready
    expect(r.data.value).toEqual({ items: [], total: 0 })
    expect(r.error.value).toEqual({ status: 500 })
  })
})

describe('availabilityLabel', () => {
  it.each([
    ['available', 'Beschikbaar'],
    ['degraded', 'Verminderde werking'],
    ['preparing', 'Wordt voorbereid'],
    ['unavailable', 'Niet beschikbaar'],
    ['something_new', 'something_new'],
  ])('%s -> %s', (value, label) => {
    expect(availabilityLabel(value)).toBe(label)
  })

  it('gives every label a dot colour of its own', () => {
    const fallback = statusDotColor('unknown')
    for (const a of ['available', 'degraded', 'preparing', 'unavailable']) {
      expect(statusDotColor(availabilityLabel(a))).not.toEqual(fallback)
    }
  })
})

describe('maintainerLink', () => {
  it('prefers the deployer address', () => {
    expect(maintainerLink({ email: 'dana@org.nl', vendor_contact: 'vendor@x.nl' }, 'GovChat'))
      .toBe('mailto:dana@org.nl?subject=Toegang%20tot%20GovChat')
  })

  it('falls back to the vendor contact', () => {
    expect(maintainerLink({ vendor_contact: 'vendor@x.nl' }, 'A')).toBe('mailto:vendor@x.nl?subject=Toegang%20tot%20A')
  })

  it('accepts an https helpdesk URL', () => {
    expect(maintainerLink({ vendor_contact: 'https://help.example.nl/ticket' }, 'A')).toBe('https://help.example.nl/ticket')
  })

  it.each([
    [undefined],
    [{}],
    [{ vendor_contact: 'call the helpdesk' }],
    [{ vendor_contact: 'javascript:alert(1)' }],
    [{ vendor_contact: 'http://insecure.example.nl' }],
    [{ email: 'a@b.nl?cc=evil@x.nl' }],
  ])('returns nothing usable for %j', (m) => {
    expect(maintainerLink(m, 'A')).toBe('')
  })

  it('skips an unusable deployer address for a usable vendor contact', () => {
    expect(maintainerLink({ email: 'not-an-address', vendor_contact: 'vendor@x.nl' }, 'A'))
      .toBe('mailto:vendor@x.nl?subject=Toegang%20tot%20A')
  })
})

describe('mapDetail launcher fields', () => {
  it('maps my_deployments and the vendor contact', () => {
    const app = mapDetail(createMockAppDetailAPI({
      my_deployments: [{ deployment_id: 'd1', availability: 'available', url: 'https://a.example.nl' }],
    }))
    expect(app.myDeployments).toEqual([{ deploymentId: 'd1', availability: 'available', url: 'https://a.example.nl' }])
    expect(app.vendorContact).toBe('contact@example.com')
  })

  it('defaults to no deployments, also without a manifest', () => {
    expect(mapDetail(createMockAppDetailAPI()).myDeployments).toEqual([])
    expect(mapDetail(createMockAppDetailAPI({ manifest: undefined })).myDeployments).toEqual([])
  })
})
