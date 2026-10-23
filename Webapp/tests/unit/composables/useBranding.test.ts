import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref, type Ref } from 'vue'
import { useBranding } from '~/composables/useBranding'
import { brandStyle } from '~/utils/branding'

const TOKENS = {
  cta: '#0b5cad', cta_hover: '#094a8a', cta_active: '#073768', cta_subtle: '#dae7f3',
  primary: '#1a1a6e', primary_hover: '#151558', primary_active: '#101042', primary_subtle: '#dddde9',
  secondary: '#eef3ff', surface: '#f7f7f7', body_text: '#111111', border: '#d0d0d0',
  wedge_start: '#101042', wedge_mid: '#1a1a6e', wedge_end: '#21218b',
}
const CUSTOM = {
  tokens: TOKENS, is_default: false, updated_at: '2026-10-11T10:00:00Z',
  logo: { url: '/api/v1/branding/logo', width: 240, height: 80, alt: 'Logo' },
}

// One shared state per key, as Nuxt's useState gives within an app.
let states: Map<string, Ref<unknown>>
beforeEach(() => {
  states = new Map()
  vi.stubGlobal('useState', (key: string, init?: () => unknown) => {
    if (!states.has(key)) states.set(key, ref(init?.() ?? null))
    return states.get(key)
  })
})

describe('useBranding', () => {
  it('fetches the brand anonymously in the browser and applies it', async () => {
    const fetchMock = vi.fn().mockResolvedValue(CUSTOM)
    vi.stubGlobal('$fetch', fetchMock)
    const { load, style, logo, logoSrc } = useBranding()
    await load()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/branding', { baseURL: 'http://localhost:8080' })
    expect(style.value).toBe(brandStyle(TOKENS))
    expect(logo.value).toEqual({ width: 240, height: 80, alt: 'Logo', version: CUSTOM.updated_at })
    expect(logoSrc.value).toBe('/branding/logo?v=2026-10-11T10%3A00%3A00Z')
  })

  it('keeps the stylesheet defaults when the fetch fails', async () => {
    vi.stubGlobal('$fetch', vi.fn().mockRejectedValue(new Error('down')))
    const { load, branding, style, logoSrc } = useBranding()
    await load()
    expect(branding.value).toBeNull()
    expect(style.value).toBeNull()
    expect(logoSrc.value).toBe('')
  })

  it('shares one brand across callers, so a save recolours the site', () => {
    const page = useBranding()
    const layout = useBranding()
    page.apply(CUSTOM)
    expect(layout.style.value).toBe(brandStyle(TOKENS))
    page.apply({ ...CUSTOM, is_default: true, logo: null })
    expect(layout.style.value).toBeNull()
    expect(layout.logo.value).toBeNull()
  })
})

describe('branding plugin', () => {
  async function runPlugin() {
    let head: { style: Ref<unknown[]> } | undefined
    vi.stubGlobal('defineNuxtPlugin', (p: unknown) => p)
    vi.stubGlobal('useHead', (h: { style: Ref<unknown[]> }) => { head = h })
    vi.stubGlobal('computed', (await import('vue')).computed)
    const plugin = (await import('~/plugins/branding')).default as unknown as { name: string; setup: () => Promise<void> }
    await plugin.setup()
    return { plugin, head: head! }
  }

  it('adds no style tag for the default palette', async () => {
    vi.stubGlobal('$fetch', vi.fn().mockResolvedValue({ tokens: TOKENS, is_default: true, logo: null, updated_at: 'x' }))
    const { head } = await runPlugin()
    await vi.waitFor(() => expect(states.get('branding')?.value).not.toBeNull())
    expect(head.style.value).toEqual([])
  })

  // On client-only pages (/dashboard/**) the palette arrives through useHead
  // in the browser; plugins/cspNonce.client.ts nonces the tag it inserts.
  it('adds one keyed style tag for a custom palette fetched in the browser', async () => {
    vi.stubGlobal('$fetch', vi.fn().mockResolvedValue(CUSTOM))
    const { plugin, head } = await runPlugin()
    expect(plugin.name).toBe('branding')
    await vi.waitFor(() => expect(head.style.value).toHaveLength(1))
    expect(head.style.value[0]).toEqual({ key: 'brand-palette', id: 'brand-palette', textContent: brandStyle(TOKENS) })
  })

  it('does not refetch a brand the server already handed over', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('$fetch', fetchMock)
    useBranding().apply(CUSTOM)
    await runPlugin()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
