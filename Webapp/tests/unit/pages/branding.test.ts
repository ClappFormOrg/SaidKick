import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, Suspense } from 'vue'
import { useAuth } from '~/composables/useAuth'
import { useAsyncAction } from '~/composables/useAsyncAction'
import { useRoleGuard } from '~/composables/useRoleGuard'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockMeResponse } from '../../utils/mock'
import Branding from '~/pages/dashboard/branding/index.vue'
import GovnlButton from '~/components/GovnlButton.vue'

const DEFAULT = {
  colors: { cta: '#ca005d', primary: '#154273', secondary: '#e1f0fa', surface: '#f1f5f9', body_text: '#0f172a', border: '#cbd5e1' },
  tokens: {
    cta: '#ca005d', cta_hover: '#a30049', cta_active: '#7d0038', cta_subtle: '#f6dde7',
    primary: '#154273', primary_hover: '#11355c', primary_active: '#0d2845', primary_subtle: '#dce3ea',
    secondary: '#e1f0fa', surface: '#f1f5f9', body_text: '#0f172a', border: '#cbd5e1',
    wedge_start: '#0a2750', wedge_mid: '#154273', wedge_end: '#1b5391',
  },
  is_default: true,
  logo: null as null | { url: string; width: number; height: number; alt: string },
  contrast_failures: [],
  updated_at: '2026-10-11T10:00:00Z',
}
const WITH_LOGO = { ...DEFAULT, logo: { url: '/api/v1/branding/logo', width: 240, height: 80, alt: 'Gemeente Voorbeeld' }, updated_at: '2026-10-11T11:00:00Z' }

const CONTRAST_422 = {
  status: 422,
  data: {
    error: {
      code: 'validation_failed',
      message: 'the palette fails WCAG AA contrast',
      details: {
        contrast_failures: [
          { pair: 'white_on_cta', foreground: '#ffffff', background: '#009dff', ratio: 2.88, required: 4.5 },
          { pair: 'white_on_cta_hover', foreground: '#ffffff', background: '#007ecc', ratio: 4.32, required: 4.5 },
        ],
      },
    },
  },
}

let calls: { method: string; url: string; body?: unknown }[]
let respond: (method: string, url: string, body?: unknown) => unknown

beforeEach(() => {
  calls = []
  respond = (method) => (method === 'POST' ? WITH_LOGO : DEFAULT)
  localStorage.clear()
  vi.stubGlobal('$fetch', vi.fn(async (url: string, opts: { method?: string; body?: unknown } = {}) => {
    const method = (opts.method ?? 'GET').toUpperCase()
    calls.push({ method, url, body: opts.body })
    const r = respond(method, url, opts.body)
    if (r instanceof Error || (typeof r === 'object' && r !== null && 'status' in r)) throw r
    return r
  }))
  vi.stubGlobal('useAuth', useAuth)
  vi.stubGlobal('useAsyncAction', useAsyncAction)
  vi.stubGlobal('useRoleGuard', useRoleGuard)
  vi.stubGlobal('definePageMeta', () => {})
  vi.stubGlobal('useHead', () => {})
})

async function mountPage(role = 'platform_admin'): Promise<VueWrapper> {
  localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles: [role] })))
  const Wrapper = defineComponent({ render: () => h(Suspense, null, { default: () => h(Branding) }) })
  const w = mount(Wrapper, {
    global: { components: { GovnlButton }, stubs: { NuxtLink: defineComponent({ setup: (_, { slots }) => () => h('a', slots.default?.()) }) } },
    attachTo: document.body,
  })
  await flushPromises()
  return w
}

function button(w: VueWrapper, label: string) {
  const b = w.findAll('button').find(x => x.text().includes(label))
  if (!b) throw new Error(`no button "${label}"`)
  return b
}

describe('Huisstijl page', () => {
  it('loads the saved palette into the form', async () => {
    const w = await mountPage()
    expect(calls[0]).toMatchObject({ method: 'GET', url: '/api/v1/branding' })
    expect((w.get('#brand-cta').element as HTMLInputElement).value).toBe('#ca005d')
    expect(w.text()).toContain('Standaardpalet')
    expect(w.text()).toContain('#a30049')
    w.unmount()
  })

  it('saves the six colours lowercase', async () => {
    const w = await mountPage()
    await w.get('#brand-cta').setValue('#0B5CAD')
    await button(w, 'Kleuren opslaan').trigger('click')
    await flushPromises()
    const put = calls.find(c => c.method === 'PUT')!
    expect(put.url).toBe('/api/v1/branding')
    expect(put.body).toEqual({ ...DEFAULT.colors, cta: '#0b5cad' })
    expect(w.text()).toContain('Kleuren opgeslagen.')
    w.unmount()
  })

  it('does not save a malformed colour', async () => {
    const w = await mountPage()
    await w.get('#brand-primary').setValue('blue')
    expect(button(w, 'Kleuren opslaan').attributes('disabled')).toBeDefined()
    expect(w.get('#brand-primary').attributes('aria-invalid')).toBe('true')
    expect(w.text()).toContain('Gebruik de vorm #RRGGBB.')
    w.unmount()
  })

  // Q4: the API refuses a palette below WCAG AA; the page lists the pairs and
  // keeps them on screen until a save succeeds.
  it('lists the failing contrast pairs of a refused save until the next successful one', async () => {
    const w = await mountPage()
    respond = (method) => (method === 'PUT' ? CONTRAST_422 : DEFAULT)
    await w.get('#brand-cta').setValue('#009dff')
    await button(w, 'Kleuren opslaan').trigger('click')
    await flushPromises()
    const panel = w.findAll('[role="alert"]').find(a => a.text().includes('4,5:1'))!
    expect(panel.text()).toContain('Witte tekst op de actiekleur: 2,88:1')
    expect(panel.text()).toContain('Witte tekst op de actiekleur (hover): 4,32:1')
    expect(w.text()).toContain('Niet opgeslagen')

    respond = () => DEFAULT
    await w.get('#brand-cta').setValue('#ca005d')
    await button(w, 'Kleuren opslaan').trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('2,88:1')
    w.unmount()
  })

  it('resets the colours with DELETE /branding', async () => {
    const w = await mountPage()
    await button(w, 'Standaardkleuren herstellen').trigger('click')
    await flushPromises()
    expect(calls.some(c => c.method === 'DELETE' && c.url === '/api/v1/branding')).toBe(true)
    expect(w.text()).toContain('Het logo is ongewijzigd.')
    w.unmount()
  })

  // #47: the hidden file input has an accessible name and is no tab stop of
  // its own; the visible button opens it.
  it('keeps the hidden file input out of the tab order with an accessible name', async () => {
    const w = await mountPage()
    const input = w.get('input[type="file"]')
    expect(input.attributes('aria-label')).toBe('Logobestand (PNG)')
    expect(input.attributes('tabindex')).toBe('-1')
    expect(input.attributes('aria-hidden')).toBe('true')
    expect(input.attributes('accept')).toBe('image/png')

    const click = vi.spyOn(input.element as HTMLInputElement, 'click').mockImplementation(() => {})
    await button(w, 'Logobestand kiezen').trigger('click')
    expect(click).toHaveBeenCalled()
    w.unmount()
  })

  // The rule axe's "label" check applies: every form control has a name.
  it('gives every input an accessible name', async () => {
    const w = await mountPage()
    const inputs = w.findAll('input')
    expect(inputs.length).toBe(14)
    for (const input of inputs) {
      const id = input.attributes('id')
      const named = Boolean(input.attributes('aria-label')) || Boolean(id && w.find(`label[for="${id}"]`).exists())
      expect(named, input.html()).toBe(true)
    }
    w.unmount()
  })

  it('uploads a PNG with its alt text and shows the stored logo', async () => {
    const w = await mountPage()
    const input = w.get('input[type="file"]')
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'logo.png', { type: 'image/png' })
    Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
    await input.trigger('change')
    expect(button(w, 'Logo opslaan').attributes('disabled')).toBeDefined()
    await w.get('#brand-logo-alt').setValue('Gemeente Voorbeeld')
    await button(w, 'Logo opslaan').trigger('click')
    await flushPromises()

    const post = calls.find(c => c.method === 'POST')!
    expect(post.url).toBe('/api/v1/branding/logo')
    const body = post.body as FormData
    expect((body.get('file') as File).name).toBe('logo.png')
    expect(body.get('alt')).toBe('Gemeente Voorbeeld')

    const img = w.get('img')
    expect(img.attributes('src')).toBe('/branding/logo?v=2026-10-11T11%3A00%3A00Z')
    expect(img.attributes('alt')).toBe('Gemeente Voorbeeld')
    expect(img.attributes('width')).toBe('240')
    w.unmount()
  })

  it('refuses a non-PNG or oversize file before uploading', async () => {
    const w = await mountPage()
    const input = w.get('input[type="file"]')
    for (const [file, message] of [
      [new File(['<svg/>'], 'logo.svg', { type: 'image/svg+xml' }), 'Alleen PNG-bestanden'],
      [new File([new Uint8Array(512 * 1024 + 1)], 'big.png', { type: 'image/png' }), 'groter dan 512 KB'],
    ] as const) {
      Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
      await input.trigger('change')
      expect(w.text()).toContain(message)
    }
    expect(calls.some(c => c.method === 'POST')).toBe(false)
    w.unmount()
  })

  it('removes the logo with DELETE /branding/logo', async () => {
    respond = () => WITH_LOGO
    const w = await mountPage()
    respond = () => DEFAULT
    await button(w, 'Logo verwijderen').trigger('click')
    await flushPromises()
    expect(calls.some(c => c.method === 'DELETE' && c.url === '/api/v1/branding/logo')).toBe(true)
    expect(w.find('img').exists()).toBe(false)
    w.unmount()
  })

  it('shows no controls to a user without platform_admin', async () => {
    const w = await mountPage('org_admin')
    expect(w.find('form').exists()).toBe(false)
    expect(w.find('input[type="file"]').exists()).toBe(false)
    w.unmount()
  })
})
