// Shared constants — import explicitly where needed.
// Nuxt does not auto-import plain constants.

export const STORAGE_KEY_ME = 'aia_me'

export const COPY_FEEDBACK_MS = 2_000

export const REGEX_EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// Dutch short date, e.g. "15 jun 2024". withTime adds hours and minutes for
// timestamps where the time of day matters (cluster heartbeats).
export function formatDate(iso?: string, opts: { withTime?: boolean } = {}): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleDateString('nl-NL', {
    day: 'numeric', month: 'short', year: 'numeric',
    ...(opts.withTime ? { hour: '2-digit' as const, minute: '2-digit' as const } : {}),
  })
}
