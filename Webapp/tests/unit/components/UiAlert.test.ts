import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UiAlert from '~/components/UiAlert.vue'

describe('UiAlert', () => {
  it.each([
    ['informative', 'status', 'Informatie'],
    ['positive', 'status', 'Gelukt'],
    ['warning', 'status', 'Let op'],
    ['negative', 'alert', 'Fout'],
  ] as const)('announces a %s message as %s with an icon and the label %s', (tone, role, label) => {
    const w = mount(UiAlert, { props: { tone } })
    expect(w.get(`[role="${role}"]`).text()).toBe(label)
    expect(w.find('svg[aria-hidden="true"]').exists()).toBe(true)
  })

  it('defaults to informative', () => {
    const w = mount(UiAlert)
    expect(w.attributes('role')).toBe('status')
    expect(w.text()).toBe('Informatie')
  })

  it('prefixes a custom title with the tone for screen readers', () => {
    const w = mount(UiAlert, { props: { tone: 'negative', title: 'Opslaan mislukt' } })
    expect(w.get('.sr-only').text()).toBe('Fout:')
    expect(w.text()).toContain('Opslaan mislukt')
  })

  it('renders the body slot', () => {
    const w = mount(UiAlert, { slots: { default: 'Probeer het later opnieuw.' } })
    expect(w.text()).toContain('Probeer het later opnieuw.')
  })
})
