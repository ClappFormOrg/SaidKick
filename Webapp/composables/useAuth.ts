import { STORAGE_KEY_ME } from '~/utils/constants'

const CACHE_KEY = STORAGE_KEY_ME

export interface MeResponse {
  id: string
  email: string
  name: string
  roles: string[]
  org_id?: string
  org_slug?: string
  org_name?: string
}

function readCache(): MeResponse | null {
  if (!import.meta.client) return null
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    return raw ? (JSON.parse(raw) as MeResponse) : null
  } catch {
    return null
  }
}

export function useAuth() {
  const { api } = useApi()
  const me = useState<MeResponse | null>('auth:me', () => null)

  // Populate from cache so the template has the right state before the async
  // fetchMe() resolves. Only runs on the client. The server never knows the
  // user, so during hydration the cache is applied only once hydration has
  // finished: applying it earlier makes the client render a signed-in header
  // over server HTML that says "Inloggen", which Vue reports as a mismatch.
  if (import.meta.client && !me.value) {
    const nuxtApp = useNuxtApp()
    if (nuxtApp.isHydrating) {
      nuxtApp.hooks.hookOnce('app:suspense:resolve', () => {
        if (!me.value) me.value = readCache()
      })
    } else {
      me.value = readCache()
    }
  }

  async function fetchMe(): Promise<MeResponse | null> {
    if (import.meta.server) return null
    try {
      const data = await api<MeResponse>('/api/v1/me')
      me.value = data
      localStorage.setItem(CACHE_KEY, JSON.stringify(data))
      return data
    } catch {
      me.value = null
      localStorage.removeItem(CACHE_KEY)
      return null
    }
  }

  const initials = computed(() => {
    const name = me.value?.name
    if (!name) return '?'
    const parts = name.split(/[\s@_\-.]+/).filter(Boolean)
    return parts.slice(0, 2).map(p => p[0]!.toUpperCase()).join('')
  })

  const roles = computed(() => me.value?.roles ?? [])

  // Plain membership, mirroring Principal.Has in the API. There is no
  // superuser: platform_admin only holds the routes that name it.
  function hasRole(role: string): boolean {
    return roles.value.includes(role)
  }

  // Each helper matches the roles the API route table requires for the
  // actions it gates (API/internal/handlers/handlers.go).
  const isDeveloper     = computed(() => hasRole('developer'))
  const isDeployer      = computed(() => hasRole('deployer'))
  const isClusterMgr    = computed(() => hasRole('cluster_admin') || hasRole('platform_admin'))
  const isAuditor       = computed(() => roles.value.includes('auditor'))
  const isOrgAdmin      = computed(() => roles.value.includes('org_admin') || roles.value.includes('platform_admin'))
  const isPlatformAdmin = computed(() => roles.value.includes('platform_admin'))
  const isAppUser       = computed(() => hasRole('app_user'))
  // Holds app_user and no other role, so the launcher is the only dashboard
  // page with anything for them. GET /my-apps itself needs only a session.
  const isAppUserOnly   = computed(() => roles.value.length > 0 && roles.value.every(r => r === 'app_user'))
  const effectiveOrgSlug = computed(() => me.value?.org_slug ?? '')

  async function logout() {
    if (import.meta.client) {
      // Ask the API to clear the saidkick_session cookie. Client state is
      // cleared even if this fails so the UI never shows a stale session.
      try {
        await api('/auth/logout', { method: 'POST' })
      } catch {
        // ignore
      }
      localStorage.removeItem(CACHE_KEY)
    }
    me.value = null
    await navigateTo('/inloggen')
  }

  return {
    me, fetchMe, hasRole, initials,
    isDeveloper, isDeployer, isClusterMgr, isAuditor, isOrgAdmin, isPlatformAdmin,
    isAppUser, isAppUserOnly, effectiveOrgSlug, logout,
  }
}
