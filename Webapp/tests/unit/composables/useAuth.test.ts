import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useAuth } from '~/composables/useAuth'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockMeResponse } from '../../utils/mock'

// Own mock for $fetch so we can control resolved/rejected values per-test.
const mockFetch = vi.fn()
vi.stubGlobal('$fetch', mockFetch)

// Own mock for navigateTo so we can assert routing.
const mockNavigateTo = vi.fn()
vi.stubGlobal('navigateTo', mockNavigateTo)

beforeEach(() => {
  localStorage.clear()
})

// ─── initials ─────────────────────────────────────────────────────────────────

describe('initials', () => {
  it('returns ? when me is null', () => {
    const { initials } = useAuth()
    expect(initials.value).toBe('?')
  })

  it('returns first letter of a single-word name', () => {
    const { me, initials } = useAuth()
    me.value = createMockMeResponse({ name: 'Alice' })
    expect(initials.value).toBe('A')
  })

  it('returns initials of first two name parts', () => {
    const { me, initials } = useAuth()
    me.value = createMockMeResponse({ name: 'John Doe' })
    expect(initials.value).toBe('JD')
  })

  it('splits on dots, underscores, and hyphens', () => {
    const { me, initials } = useAuth()
    me.value = createMockMeResponse({ name: 'j.doe' })
    expect(initials.value).toBe('JD')
  })

  it('uses email-like name when no display name is set', () => {
    const { me, initials } = useAuth()
    me.value = createMockMeResponse({ name: 'user@example.com' })
    expect(initials.value).toBe('UE')
  })
})

// ─── hasRole ─────────────────────────────────────────────────────────────────

describe('hasRole', () => {
  it('returns true when the exact role is present', () => {
    const { me, hasRole } = useAuth()
    me.value = createMockMeResponse({ roles: ['developer'] })
    expect(hasRole('developer')).toBe(true)
  })

  it('returns false when the role is absent', () => {
    const { me, hasRole } = useAuth()
    me.value = createMockMeResponse({ roles: ['developer'] })
    expect(hasRole('auditor')).toBe(false)
  })

  it('does not treat platform_admin as holding other roles', () => {
    const { me, hasRole } = useAuth()
    me.value = createMockMeResponse({ roles: ['platform_admin'] })
    expect(hasRole('platform_admin')).toBe(true)
    expect(hasRole('developer')).toBe(false)
    expect(hasRole('auditor')).toBe(false)
    expect(hasRole('cluster_admin')).toBe(false)
  })

  it('returns false when me is null', () => {
    const { hasRole } = useAuth()
    expect(hasRole('developer')).toBe(false)
  })
})

// ─── role helpers per single role ─────────────────────────────────────────────
// Each helper mirrors the API route table (handlers.go Register):
//   isDeveloper     POST /apps, /apps/:slug/versions, /apps/:slug/submit
//   isDeployer      POST /deployments, upgrade, rollback, DELETE /deployments/:id
//   isClusterMgr    POST /clusters, PATCH/DELETE /clusters/:id
//   isAuditor       /audit-submissions/*
//   isOrgAdmin      GET /orgs/:slug/members, POST .../roles
//   isPlatformAdmin cluster scope/access, POST /orgs, /advisories, GET /audit-log
//   isAppUser       holds app_user (GET /my-apps itself needs only a session)
//   isAppUserOnly   holds app_user and nothing else; /dashboard redirects them

const ROLES = ['developer', 'deployer', 'auditor', 'cluster_admin', 'org_admin', 'platform_admin', 'app_user'] as const
type Role = typeof ROLES[number]

interface HelperOutputs {
  isDeveloper: boolean
  isDeployer: boolean
  isClusterMgr: boolean
  isAuditor: boolean
  isOrgAdmin: boolean
  isPlatformAdmin: boolean
  isAppUser: boolean
  isAppUserOnly: boolean
}

const EXPECTED: Record<Role, HelperOutputs> = {
  developer:      { isDeveloper: true,  isDeployer: false, isClusterMgr: false, isAuditor: false, isOrgAdmin: false, isPlatformAdmin: false, isAppUser: false, isAppUserOnly: false },
  deployer:       { isDeveloper: false, isDeployer: true,  isClusterMgr: false, isAuditor: false, isOrgAdmin: false, isPlatformAdmin: false, isAppUser: false, isAppUserOnly: false },
  auditor:        { isDeveloper: false, isDeployer: false, isClusterMgr: false, isAuditor: true,  isOrgAdmin: false, isPlatformAdmin: false, isAppUser: false, isAppUserOnly: false },
  cluster_admin:  { isDeveloper: false, isDeployer: false, isClusterMgr: true,  isAuditor: false, isOrgAdmin: false, isPlatformAdmin: false, isAppUser: false, isAppUserOnly: false },
  org_admin:      { isDeveloper: false, isDeployer: false, isClusterMgr: false, isAuditor: false, isOrgAdmin: true,  isPlatformAdmin: false, isAppUser: false, isAppUserOnly: false },
  platform_admin: { isDeveloper: false, isDeployer: false, isClusterMgr: true,  isAuditor: false, isOrgAdmin: true,  isPlatformAdmin: true, isAppUser: false, isAppUserOnly: false },
  app_user:       { isDeveloper: false, isDeployer: false, isClusterMgr: false, isAuditor: false, isOrgAdmin: false, isPlatformAdmin: false, isAppUser: true, isAppUserOnly: true },
}

function helperOutputs(a: ReturnType<typeof useAuth>): HelperOutputs {
  return {
    isDeveloper:     a.isDeveloper.value,
    isDeployer:      a.isDeployer.value,
    isClusterMgr:    a.isClusterMgr.value,
    isAuditor:       a.isAuditor.value,
    isOrgAdmin:      a.isOrgAdmin.value,
    isPlatformAdmin: a.isPlatformAdmin.value,
    isAppUser:       a.isAppUser.value,
    isAppUserOnly:   a.isAppUserOnly.value,
  }
}

describe('role helpers per single role', () => {
  it.each(ROLES)('%s gets exactly the helpers the route table grants', (role) => {
    const a = useAuth()
    a.me.value = createMockMeResponse({ roles: [role] })
    expect(helperOutputs(a)).toEqual(EXPECTED[role])
  })

  it.each(ROLES)('hasRole is plain membership for %s', (role) => {
    const a = useAuth()
    a.me.value = createMockMeResponse({ roles: [role] })
    for (const other of ROLES) expect(a.hasRole(other)).toBe(other === role)
  })

  it('every helper is false when me is null', () => {
    expect(Object.values(helperOutputs(useAuth()))).not.toContain(true)
  })

  it('multiple roles combine additively', () => {
    const a = useAuth()
    a.me.value = createMockMeResponse({ roles: ['developer', 'deployer'] })
    expect(helperOutputs(a)).toEqual({
      ...EXPECTED.developer,
      isDeployer: true,
    })
  })

  it('app_user with any other role is not app_user only', () => {
    const a = useAuth()
    a.me.value = createMockMeResponse({ roles: ['app_user', 'developer'] })
    expect(a.isAppUser.value).toBe(true)
    expect(a.isAppUserOnly.value).toBe(false)
  })

  it('a user with no roles is not app_user only', () => {
    const a = useAuth()
    a.me.value = createMockMeResponse({ roles: [] })
    expect(a.isAppUserOnly.value).toBe(false)
  })
})

describe('effectiveOrgSlug', () => {
  it('returns the org_slug from me', () => {
    const { me, effectiveOrgSlug } = useAuth()
    me.value = createMockMeResponse({ org_slug: 'apps-dev' })
    expect(effectiveOrgSlug.value).toBe('apps-dev')
  })

  it('returns empty string when me is null', () => {
    const { effectiveOrgSlug } = useAuth()
    expect(effectiveOrgSlug.value).toBe('')
  })
})

// ─── readCache (via useAuth initialisation) ───────────────────────────────────
// Note: import.meta.client is not substituted in the Vitest transform pipeline,
// so the client-side cache-read guard inside readCache() evaluates to false.
// We test the fetchMe() path instead for populating state.

describe('cache initialisation', () => {
  it('starts with me as null when localStorage is empty', () => {
    const { me } = useAuth()
    expect(me.value).toBeNull()
  })
})

// ─── fetchMe ─────────────────────────────────────────────────────────────────

describe('fetchMe', () => {
  it('updates me and writes to localStorage on success', async () => {
    const user = createMockMeResponse()
    mockFetch.mockResolvedValueOnce(user)

    const { me, fetchMe } = useAuth()
    const result = await fetchMe()

    expect(result).toEqual(user)
    expect(me.value).toEqual(user)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY_ME)!)).toEqual(user)
  })

  it('clears me and localStorage on fetch failure', async () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse()))
    mockFetch.mockRejectedValueOnce(new Error('Network error'))

    const { me, fetchMe } = useAuth()
    me.value = createMockMeResponse()

    const result = await fetchMe()

    expect(result).toBeNull()
    expect(me.value).toBeNull()
    expect(localStorage.getItem(STORAGE_KEY_ME)).toBeNull()
  })
})

// ─── logout ───────────────────────────────────────────────────────────────────

describe('logout', () => {
  it('sets me to null and navigates to /inloggen', async () => {
    mockFetch.mockResolvedValueOnce(undefined)
    const { me, logout } = useAuth()
    me.value = createMockMeResponse()
    await logout()

    expect(mockFetch).toHaveBeenCalledWith('/auth/logout', expect.objectContaining({ method: 'POST' }))
    expect(me.value).toBeNull()
    expect(mockNavigateTo).toHaveBeenCalledWith('/inloggen')
  })
})
