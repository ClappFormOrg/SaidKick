import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RijksMasthead from '~/components/RijksMasthead.vue'
import GovnlRijkslint from '~/components/GovnlRijkslint.vue'
import { useBranding } from '~/composables/useBranding'

const stubs = { GovnlRijkslint }

// useState is stubbed per call in tests/setup.ts, so share one brand ref
// between the test and the component.
let brand: ReturnType<typeof useBranding>

beforeEach(() => {
  brand = useBranding()
  vi.stubGlobal('useBranding', () => brand)
})

describe('RijksMasthead', () => {
  it('shows the Rijkslint lockup with the organisation name by default', () => {
    const w = mount(RijksMasthead, { global: { stubs } })
    expect(w.find('[data-testid="rijks-lockup"]').exists()).toBe(true)
    expect(w.text()).toContain('Rijksorganisatie voor')
    expect(w.text()).toContain('Koninkrijksrelaties')
    expect(w.find('img').exists()).toBe(false)
  })

  it('replaces the lockup with an uploaded logo', () => {
    brand.apply({
      is_default: true,
      updated_at: '2026-10-11T12:00:00Z',
      logo: { width: 320, height: 80, alt: 'Gemeente Voorbeeld' },
    })
    const w = mount(RijksMasthead, { global: { stubs } })
    const img = w.get('img')
    expect(img.attributes('src')).toBe('/branding/logo?v=2026-10-11T12%3A00%3A00Z')
    expect(img.attributes('alt')).toBe('Gemeente Voorbeeld')
    expect(img.attributes('width')).toBe('320')
    expect(img.attributes('height')).toBe('80')
    expect(w.find('[data-testid="rijks-lockup"]').exists()).toBe(false)
    expect(w.text()).not.toContain('Rijksorganisatie')
  })

  it('takes custom organisation lines', () => {
    const w = mount(RijksMasthead, {
      props: { organisation: ['Provincie', 'Voorbeeld'], ministry: ['Afdeling AI'] },
      global: { stubs },
    })
    expect(w.text()).toContain('Provincie')
    expect(w.text()).toContain('Afdeling AI')
    expect(w.findAll('br')).toHaveLength(1)
  })
})
