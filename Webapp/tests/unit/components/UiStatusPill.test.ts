import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UiStatusPill from '~/components/UiStatusPill.vue'

function dot(w: ReturnType<typeof mount>) {
  return w.get('span[aria-hidden="true"]')
}

describe('UiStatusPill', () => {
  it('always prints the status as text', () => {
    for (const status of ['Productie', 'Pilot', 'Testfase', 'Concept', 'Uitfaserend', 'Beschikbaar']) {
      expect(mount(UiStatusPill, { props: { status } }).text()).toBe(status)
    }
  })

  it('shows a decorative green dot for Productie', () => {
    const w = mount(UiStatusPill, { props: { status: 'Productie' } })
    expect(dot(w).attributes('style')).toContain('var(--govnl-color-groen-500)')
  })

  it('keeps the pill itself neutral', () => {
    const w = mount(UiStatusPill, { props: { status: 'Uitfaserend' } })
    const pill = w.get('span')
    expect(pill.attributes('style')).toBeUndefined()
    expect(pill.classes()).toEqual(expect.arrayContaining(['bg-brand-surface', 'text-brand-body']))
  })

  it('falls back to a grey dot for an unknown status and still prints it', () => {
    const w = mount(UiStatusPill, { props: { status: 'ietsonbekends' } })
    expect(dot(w).attributes('style')).toContain('var(--govnl-color-grijs-500)')
    expect(w.text()).toBe('ietsonbekends')
  })

  it('renders an empty label without crashing', () => {
    const w = mount(UiStatusPill, { props: { status: '' } })
    expect(w.find('span').exists()).toBe(true)
  })
})
