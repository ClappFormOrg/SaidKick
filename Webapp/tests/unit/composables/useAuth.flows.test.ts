import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useAuth, type MeResponse } from '~/composables/useAuth'
import { STORAGE_KEY_ME } from '~/utils/constants'

const mockNavigate = vi.fn()
vi.stubGlobal('navigateTo', mockNavigate)
const mockFetch = vi.fn()

const ME: MeResponse = { id: 'u1', email: 'ada@example.nl', name: 'Ada Auditor', roles: ['auditor'] }

beforeEach(() => {
  localStorage.clear()
  mockNavigate.mockReset()
  vi.stubGlobal('$fetch', mockFetch)
  mockFetch.mockReset()
})

// ─── cache hydration (readCache, called during useAuth setup) ─────────────────

describe('useAuth — cache hydration', () => {
  it('hydrates the profile from a valid cached payload (happy)', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(ME))
    expect(useAuth().me.value).toEqual(ME)
  })

  it('stays null when there is no cache', () => {
    expect(useAuth().me.value).toBeNull()
  })

  it('stays null when the cached JSON is corrupt (unhappy)', () => {
    localStorage.setItem(STORAGE_KEY_ME, '{ this is not json')
    expect(useAuth().me.value).toBeNull()
  })

  it('waits for hydration to finish before applying the cache, so the first client render matches the server', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(ME))
    const hooks: Record<string, () => void> = {}
    vi.stubGlobal('useNuxtApp', () => ({
      isHydrating: true,
      hooks: { hookOnce: (name: string, fn: () => void) => { hooks[name] = fn } },
    }))
    try {
      const { me } = useAuth()
      expect(me.value).toBeNull()
      hooks['app:suspense:resolve']()
      expect(me.value).toEqual(ME)
    } finally {
      vi.stubGlobal('useNuxtApp', () => ({ isHydrating: false, hooks: { hookOnce: vi.fn() } }))
    }
  })
})

// ─── fetchMe ───────────────────────────────────────────────────────────────────

describe('useAuth — fetchMe', () => {
  it('stores the profile in state and localStorage on success (happy)', async () => {
    mockFetch.mockResolvedValueOnce(ME)
    const auth = useAuth()
    const res = await auth.fetchMe()
    expect(res).toEqual(ME)
    expect(auth.me.value).toEqual(ME)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY_ME)!)).toEqual(ME)
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/me', expect.objectContaining({
      baseURL: 'http://localhost:8080',
      credentials: 'include',
    }))
  })

  it('clears state and cache and returns null when the session is invalid (unhappy)', async () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(ME))
    mockFetch.mockRejectedValueOnce({ status: 401 })
    const auth = useAuth()
    const res = await auth.fetchMe()
    expect(res).toBeNull()
    expect(auth.me.value).toBeNull()
    expect(localStorage.getItem(STORAGE_KEY_ME)).toBeNull()
  })
})

// ─── role helpers driven by the fetched profile ────────────────────────────────

describe('useAuth — role helpers', () => {
  it('auditor profile: isAuditor true, deployer/admin false', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(ME))
    const a = useAuth()
    expect(a.isAuditor.value).toBe(true)
    expect(a.isDeployer.value).toBe(false)
    expect(a.isPlatformAdmin.value).toBe(false)
    expect(a.initials.value).toBe('AA')
  })

  it('platform_admin holds only the routes that name it, not every role', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify({ ...ME, roles: ['platform_admin'] }))
    const a = useAuth()
    expect(a.hasRole('deployer')).toBe(false)
    expect(a.isDeployer.value).toBe(false)
    expect(a.isDeveloper.value).toBe(false)
    expect(a.isAuditor.value).toBe(false)
    expect(a.isClusterMgr.value).toBe(true)
    expect(a.isOrgAdmin.value).toBe(true)
    expect(a.isPlatformAdmin.value).toBe(true)
  })

  it('initials fall back to "?" when there is no profile', () => {
    expect(useAuth().initials.value).toBe('?')
  })
})

// ─── logout ────────────────────────────────────────────────────────────────────

describe('useAuth — logout', () => {
  it('ends the API session, clears cache + state and redirects to /inloggen', async () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(ME))
    mockFetch.mockResolvedValueOnce(undefined)
    const auth = useAuth()
    await auth.logout()
    expect(mockFetch).toHaveBeenCalledWith('/auth/logout', expect.objectContaining({
      method: 'POST',
      baseURL: 'http://localhost:8080',
      credentials: 'include',
    }))
    expect(auth.me.value).toBeNull()
    expect(localStorage.getItem(STORAGE_KEY_ME)).toBeNull()
    expect(mockNavigate).toHaveBeenCalledWith('/inloggen')
  })

  it('still clears client state when the logout request fails (unhappy)', async () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(ME))
    mockFetch.mockRejectedValueOnce(new Error('Network error'))
    const auth = useAuth()
    await auth.logout()
    expect(auth.me.value).toBeNull()
    expect(localStorage.getItem(STORAGE_KEY_ME)).toBeNull()
    expect(mockNavigate).toHaveBeenCalledWith('/inloggen')
  })
})
