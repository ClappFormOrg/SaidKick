export function useApi() {
  const config = useRuntimeConfig()
  const baseURL = config.public.apiBase

  // credentials: 'include' is required for cross-origin requests so the
  // saidkick_session cookie is sent to the API (localhost:8080 from localhost:3000).
  const api = $fetch.create({ baseURL, credentials: 'include' })

  // Public endpoints (community discussions, federated org list) never needed
  // the session cookie, so they keep the browser's default credentials mode.
  const publicApi = $fetch.create({ baseURL })

  // baseURL stays exported for URLs the browser opens itself (login redirect,
  // evidence downloads, the deployment event stream).
  return { api, publicApi, baseURL }
}

// Type guard for $fetch error responses. Narrows `unknown` catch values to
// the shape Nuxt's ofetch throws so callers avoid `err: any`.
export interface FieldError { path: string; message: string }

export interface FetchError {
  data?: { message?: string; code?: string; errors?: FieldError[] }
  status?: number
}

export function isFetchError(e: unknown): e is FetchError {
  return typeof e === 'object' && e !== null && ('data' in e || 'status' in e)
}

// The API's error message when it sent one, otherwise the caller's fallback.
export function errorMessage(err: unknown, fallback: string): string {
  return isFetchError(err) ? (err.data?.message ?? fallback) : fallback
}
