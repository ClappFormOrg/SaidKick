import { brandLogoSrc, internalApiBase, parseBranding, type BrandState } from '~/utils/branding'

// Server-side fetches give up quickly: a slow API must not hold up the page,
// and the stylesheet defaults are a complete brand on their own.
const SERVER_TIMEOUT_MS = 1500

// The instance brand, shared by the whole app. The server fetches it while
// rendering public pages and hands it over in the payload; client-only pages
// (/dashboard/**) fetch it in the browser. A failed fetch leaves it null, so
// the stylesheet defaults apply.
export function useBranding() {
  const branding = useState<BrandState | null>('branding', () => null)

  // Replaces the brand with a GET /api/v1/branding response, as the settings
  // page does after each save.
  function apply(raw: unknown) {
    branding.value = parseBranding(raw)
  }

  async function load(): Promise<void> {
    try {
      if (import.meta.server) {
        const base = internalApiBase(useRuntimeConfig(), process.env)
        if (!base) return
        apply(await $fetch(`${base}/api/v1/branding`, { timeout: SERVER_TIMEOUT_MS }))
      } else {
        const { publicApi } = useApi()
        apply(await publicApi('/api/v1/branding'))
      }
    } catch {
      // Keep the defaults.
    }
  }

  const style = computed(() => branding.value?.style ?? null)
  const logo = computed(() => branding.value?.logo ?? null)
  const logoSrc = computed(() => (logo.value ? brandLogoSrc(logo.value) : ''))

  return { branding, style, logo, logoSrc, apply, load }
}
