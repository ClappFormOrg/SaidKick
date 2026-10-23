import { useAuth } from '~/composables/useAuth'

export async function handleAuthCheck() {
  // Ask the API rather than trusting localStorage: a cached profile can
  // outlive an expired or cleared session cookie.
  const { fetchMe } = useAuth()
  const me = await fetchMe()
  if (!me) return navigateTo('/inloggen')
}

export default defineNuxtRouteMiddleware(() => {
  if (import.meta.client) return handleAuthCheck()
})
