import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UiTag from '~/components/UiTag.vue'

describe('UiTag', () => {
  it('renders its default slot content', () => {
    const w = mount(UiTag, { slots: { default: 'Chatbot' } })
    expect(w.text()).toBe('Chatbot')
  })

  it('defaults to the neutral tone', () => {
    const w = mount(UiTag, { slots: { default: 'x' } })
    expect(w.get('span').classes()).toEqual(expect.arrayContaining(['bg-brand-white', 'border-brand-border']))
  })

  it('tints the brand tone with the primary colour', () => {
    const w = mount(UiTag, { props: { tone: 'brand' }, slots: { default: 'x' } })
    expect(w.get('span').classes()).toEqual(expect.arrayContaining(['bg-brand-primary-subtle', 'text-brand-primary']))
  })
})
