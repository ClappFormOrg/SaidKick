import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import GovnlRijkslint from '~/components/GovnlRijkslint.vue'
import GovnlLockup from '~/components/GovnlLockup.vue'

describe('GovnlRijkslint', () => {
  it('renders at the default size', () => {
    const w = mount(GovnlRijkslint)
    const style = w.get('div').attributes('style') ?? ''
    expect(style).toContain('width: 36px')
    expect(style).toContain('height: 64px')
  })

  it('honours custom width/height props', () => {
    const w = mount(GovnlRijkslint, { props: { width: 20, height: 40 } })
    const style = w.get('div').attributes('style') ?? ''
    expect(style).toContain('width: 20px')
    expect(style).toContain('height: 40px')
  })

  it('flips the base colour when reverse=true (branch coverage)', () => {
    const normal = mount(GovnlRijkslint).get('div').attributes('style') ?? ''
    const reversed = mount(GovnlRijkslint, { props: { reverse: true } }).get('div').attributes('style') ?? ''
    expect(normal).not.toBe(reversed)
    expect(reversed).toContain('var(--govnl-color-wit)')
  })
})

describe('GovnlLockup', () => {
  it('renders the default title and subtitle', () => {
    const w = mount(GovnlLockup)
    expect(w.text()).toContain('AI-Appstore')
    expect(w.text()).toContain('AI-applicaties voor de overheid')
  })

  it('renders custom title/subtitle props', () => {
    const w = mount(GovnlLockup, { props: { title: 'Testportaal', subtitle: 'Onze ondertitel' } })
    expect(w.text()).toContain('Testportaal')
    expect(w.text()).toContain('Onze ondertitel')
  })

  it('renders the embedded Rijkslint mark', () => {
    const w = mount(GovnlLockup)
    // Rijkslint renders at least one styled div in addition to the lockup wrapper
    expect(w.findAll('div').length).toBeGreaterThan(1)
  })

  it('uses white title text in reverse mode', () => {
    const w = mount(GovnlLockup, { props: { reverse: true } })
    const hasWhiteTitle = w.findAll('span').some(s => (s.attributes('style') ?? '').includes('var(--govnl-color-wit)'))
    expect(hasWhiteTitle).toBe(true)
  })
})
