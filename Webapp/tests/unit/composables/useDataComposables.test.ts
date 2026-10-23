import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref } from 'vue'
import {
  useApps, useApp, useDeployments, useClusters, useAuditQueue, useMyApps, mapDetail,
} from '~/composables/useData'

// ─── useAsyncData harness ─────────────────────────────────────────────────────
// The real Nuxt useAsyncData runs the handler, catches errors, and exposes
// { data, error }. We reproduce just enough of that contract so we can drive
// both the happy path (handler resolves → mapped data) and the unhappy path
// (handler rejects → data stays at the declared default, error is set).

interface Harness<T> { data: { value: T }; error: { value: unknown }; ready: Promise<void> }

function installAsyncDataHarness() {
  vi.stubGlobal(
    'useAsyncData',
    (_key: string, handler: () => Promise<unknown>, opts?: { default?: () => unknown }) => {
      const data = ref(opts?.default ? opts.default() : null)
      const error = ref<unknown>(null)
      const ready = Promise.resolve()
        .then(handler)
        .then(v => { data.value = v })
        .catch(e => { error.value = e })
      return { data, error, ready }
    },
  )
}

const mockFetch = vi.fn()

beforeEach(() => {
  installAsyncDataHarness()
  vi.stubGlobal('$fetch', mockFetch)
  mockFetch.mockReset()
})

// ─── useApps ──────────────────────────────────────────────────────────────────

describe('useApps', () => {
  it('fetches the catalogue and maps API cards to App view models (happy)', async () => {
    mockFetch.mockResolvedValueOnce({
      items: [{ id: '1', slug: 'govchat', vendor: 'Provincie Voorbeeld', state: 'published', github_repo: 'provincie-voorbeeld/govchat', display_name: 'GovChat' }],
      total: 1,
    })
    const r = useApps() as unknown as Harness<any[]>
    await r.ready
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/apps', expect.objectContaining({
      baseURL: 'http://localhost:8080',
      query: { page_size: 100 },
      credentials: 'include',
    }))
    expect(r.data.value).toHaveLength(1)
    expect(r.data.value[0]).toMatchObject({ slug: 'govchat', name: 'GovChat', status: 'Productie' })
  })

  it('tolerates a response with no items array (unhappy: empty payload)', async () => {
    mockFetch.mockResolvedValueOnce({ total: 0 })
    const r = useApps() as unknown as Harness<any[]>
    await r.ready
    expect(r.data.value).toEqual([])
  })

  it('keeps the empty default and records the error when the fetch fails (unhappy)', async () => {
    mockFetch.mockRejectedValueOnce(new Error('network down'))
    const r = useApps() as unknown as Harness<any[]>
    await r.ready
    expect(r.data.value).toEqual([])
    expect(r.error.value).toBeInstanceOf(Error)
  })
})

// ─── useApp ─────────────────────────────────────────────────────────────────

describe('useApp', () => {
  it('fetches one app by slug and maps its detail (happy)', async () => {
    mockFetch.mockResolvedValueOnce({
      id: '1', slug: 'govchat', vendor: 'Provincie Voorbeeld', state: 'published', github_repo: 'provincie-voorbeeld/govchat',
      owning_organization_id: 'o1', owning_user_id: 'u1', github_installation_id: 1,
      github_default_branch: 'main', repo_link_state: 'linked',
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-06-18T00:00:00Z',
    })
    const r = useApp('govchat') as unknown as Harness<any>
    await r.ready
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/apps/govchat', expect.objectContaining({
      baseURL: 'http://localhost:8080', credentials: 'include',
    }))
    expect(r.data.value).toMatchObject({ slug: 'govchat', status: 'Productie' })
  })

  it('records the error when the detail fetch fails (unhappy)', async () => {
    mockFetch.mockRejectedValueOnce({ status: 404 })
    const r = useApp('ontbreekt') as unknown as Harness<any>
    await r.ready
    expect(r.error.value).toEqual({ status: 404 })
  })
})

// ─── useDeployments / useClusters ─────────────────────────────────────────────

describe('useDeployments', () => {
  it('returns the deployment list (happy)', async () => {
    mockFetch.mockResolvedValueOnce({ items: [{ id: 'd1', state: 'ready' }], total: 1 })
    const r = useDeployments() as unknown as Harness<any>
    await r.ready
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/deployments', expect.objectContaining({ baseURL: 'http://localhost:8080' }))
    expect(r.data.value.total).toBe(1)
  })

  it('falls back to the empty default on failure (unhappy)', async () => {
    mockFetch.mockRejectedValueOnce(new Error('500'))
    const r = useDeployments() as unknown as Harness<any>
    await r.ready
    expect(r.data.value).toEqual({ items: [], total: 0 })
  })
})

describe('useClusters', () => {
  it('returns the cluster list (happy)', async () => {
    mockFetch.mockResolvedValueOnce({ items: [{ id: 'c1', name: 'prod', state: 'ready' }], total: 1 })
    const r = useClusters() as unknown as Harness<any>
    await r.ready
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/clusters', expect.objectContaining({ credentials: 'include' }))
    expect(r.data.value.items[0].name).toBe('prod')
  })

  it('falls back to the empty default on failure (unhappy)', async () => {
    mockFetch.mockRejectedValueOnce(new Error('boom'))
    const r = useClusters() as unknown as Harness<any>
    await r.ready
    expect(r.data.value).toEqual({ items: [], total: 0 })
  })
})

// ─── useAuditQueue ─────────────────────────────────────────────────────────────

describe('useAuditQueue', () => {
  it('requests submitted submissions with paging (happy)', async () => {
    mockFetch.mockResolvedValueOnce({ items: [{ id: 's1' }], total: 1, page: 1, page_size: 50 })
    const r = useAuditQueue() as unknown as Harness<any>
    await r.ready
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/audit-submissions', expect.objectContaining({
      query: { state: 'submitted', page_size: 50 },
      credentials: 'include',
    }))
    expect(r.data.value.total).toBe(1)
  })

  it('falls back to the empty default on failure (unhappy)', async () => {
    mockFetch.mockRejectedValueOnce(new Error('403'))
    const r = useAuditQueue() as unknown as Harness<any>
    await r.ready
    expect(r.data.value).toEqual({ items: [], total: 0, page: 1, page_size: 50 })
  })
})

// ─── useMyApps ─────────────────────────────────────────────────────────────────

describe('useMyApps', () => {
  it('fans out across all six lifecycle states and flattens the results (happy)', async () => {
    // Six calls, one per state; give the first a card, the rest empty.
    mockFetch
      .mockResolvedValueOnce({ items: [{ id: '1', slug: 'draft-app', vendor: 'Provincie Voorbeeld', state: 'draft', github_repo: 'provincie-voorbeeld/d' }], total: 1 })
      .mockResolvedValue({ items: [], total: 0 })
    const r = useMyApps() as unknown as Harness<any[]>
    await r.ready
    expect(mockFetch).toHaveBeenCalledTimes(6)
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/apps', expect.objectContaining({
      query: { status: 'draft', page_size: 100 },
    }))
    expect(r.data.value).toHaveLength(1)
    expect(r.data.value[0].slug).toBe('draft-app')
  })

  it('falls back to the empty default if any state request fails (unhappy)', async () => {
    mockFetch.mockRejectedValue(new Error('network'))
    const r = useMyApps() as unknown as Harness<any[]>
    await r.ready
    expect(r.data.value).toEqual([])
    expect(r.error.value).toBeInstanceOf(Error)
  })
})

// ─── mapDetail edge cases (partial / incomplete detail payloads) ───────────────

describe('mapDetail — incomplete payloads', () => {
  const bare = {
    id: '1', slug: 'x', vendor: 'V', state: 'published', github_repo: 'v/x',
    owning_organization_id: 'o', owning_user_id: 'u', github_installation_id: 1,
    github_default_branch: 'main', repo_link_state: 'linked',
    created_at: '', updated_at: '',
  }

  it('maps a detail with no manifest without throwing', () => {
    const app = mapDetail({ ...bare } as any)
    expect(app.slug).toBe('x')
    expect(app.docs).toBeUndefined()
  })

  it('leaves descriptionUrl undefined when the manifest omits demo.description', () => {
    const app = mapDetail({
      ...bare,
      manifest: { metadata: { name: 'x', display_name: 'X', vendor: 'V', contact: 'c', license: 'MIT' }, spec: {} },
    } as any)
    expect(app.descriptionUrl).toBeUndefined()
  })

  it('keeps the base bijgewerkt when updated_at is empty', () => {
    const app = mapDetail({
      ...bare,
      manifest: { metadata: { name: 'x', display_name: 'X', vendor: 'V', contact: 'c', license: 'MIT' }, spec: {} },
    } as any)
    // no updated_at → falls back to the (empty) base value rather than formatting
    expect(app.bijgewerkt).toBe('')
  })
})
