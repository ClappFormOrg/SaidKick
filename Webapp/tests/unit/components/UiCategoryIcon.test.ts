import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UiCategoryIcon from '~/components/UiCategoryIcon.vue'
import { CATEGORIES } from '~/composables/useData'

describe('UiCategoryIcon', () => {
  it('renders a glyph for a known category', () => {
    const w = mount(UiCategoryIcon, { props: { category: 'chatbot' } })
    expect(w.find('svg').exists()).toBe(true)
  })

  it('names the glyph with the category title', () => {
    const w = mount(UiCategoryIcon, { props: { category: 'rag' } })
    expect(w.get('[role="img"]').attributes('aria-label')).toBe('RAG & zoeken')
  })

  it('falls back to a generic glyph named by the slug for an unknown category', () => {
    const w = mount(UiCategoryIcon, { props: { category: 'niet-bestaand' } })
    expect(w.find('svg').exists()).toBe(true)
    expect(w.get('[role="img"]').attributes('aria-label')).toBe('niet-bestaand')
  })

  it('renders for an empty category', () => {
    const w = mount(UiCategoryIcon, { props: { category: '' } })
    expect(w.find('svg').exists()).toBe(true)
  })

  it('hides itself from assistive tech when labelled is false', () => {
    const svg = mount(UiCategoryIcon, { props: { category: 'chatbot', labelled: false } }).get('svg')
    expect(svg.attributes('aria-hidden')).toBe('true')
    expect(svg.attributes('role')).toBeUndefined()
    expect(svg.attributes('aria-label')).toBeUndefined()
  })

  it('gives every configured category its own glyph', () => {
    // A category without a mapping would silently get the generic glyph.
    const glyph = (category: string) =>
      mount(UiCategoryIcon, { props: { category, labelled: false } }).get('svg').html()
    const rendered = CATEGORIES.map(c => glyph(c.slug))
    expect(new Set(rendered).size).toBe(CATEGORIES.length)
    expect(rendered).not.toContain(glyph('niet-bestaand'))
  })

  it('honours the size prop', () => {
    const w = mount(UiCategoryIcon, { props: { category: 'chatbot', size: 32 } })
    expect(w.get('svg').attributes('width')).toBe('32')
  })
})
