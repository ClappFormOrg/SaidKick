// Applies the instance brand (#158) as one <style> rule in the head.
//
// Server-rendered pages: the server fetches the brand before rendering, and
// nuxt-security puts the response nonce on the rendered <style>. Client-only
// pages (/dashboard/**, #125): the browser fetches it after start-up without
// blocking it, and plugins/cspNonce.client.ts nonces the tag when useHead
// inserts it. The default palette adds no tag at all, so an untouched
// install renders exactly as the stylesheet alone does.
export default defineNuxtPlugin({
  name: 'branding',
  async setup() {
    const { branding, style, load } = useBranding()

    useHead({
      style: computed(() => (style.value
        ? [{ key: 'brand-palette', id: 'brand-palette', textContent: style.value }]
        : [])),
    })

    if (import.meta.server) await load()
    else if (!branding.value) void load()
  },
})
