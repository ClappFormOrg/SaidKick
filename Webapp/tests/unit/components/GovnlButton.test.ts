import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { IconArrowRight, IconRocket } from '@tabler/icons-vue'
import GovnlButton from '~/components/GovnlButton.vue'

describe('GovnlButton', () => {
  it('renders a <button type="button"> by default and shows slot content', () => {
    const w = mount(GovnlButton, { slots: { default: 'Installeren' } })
    const btn = w.get('button')
    expect(btn.attributes('type')).toBe('button')
    expect(w.text()).toContain('Installeren')
  })

  it('emits click when enabled (happy path)', async () => {
    const w = mount(GovnlButton, { slots: { default: 'Go' } })
    await w.get('button').trigger('click')
    expect(w.emitted('click')).toHaveLength(1)
  })

  it('does NOT emit click when disabled (unhappy path)', async () => {
    const w = mount(GovnlButton, { props: { disabled: true }, slots: { default: 'Go' } })
    const btn = w.get('button')
    expect(btn.attributes('disabled')).toBeDefined()
    await btn.trigger('click')
    expect(w.emitted('click')).toBeUndefined()
  })

  it('renders an <a> with href when href is provided', () => {
    const w = mount(GovnlButton, { props: { href: 'https://example.org' }, slots: { default: 'Docs' } })
    const a = w.get('a')
    expect(a.attributes('href')).toBe('https://example.org')
    expect(w.find('button').exists()).toBe(false)
  })

  it('a disabled link drops href and marks aria-disabled + tabindex=-1', () => {
    const w = mount(GovnlButton, {
      props: { href: 'https://example.org', disabled: true },
      slots: { default: 'Docs' },
    })
    const a = w.get('a')
    expect(a.attributes('href')).toBeUndefined()
    expect(a.attributes('aria-disabled')).toBe('true')
    expect(a.attributes('tabindex')).toBe('-1')
  })

  it('renders the icon component passed as icon before the label', () => {
    const w = mount(GovnlButton, { props: { icon: IconRocket }, slots: { default: 'Deploy' } })
    // @tabler icon renders an <svg>
    const svg = w.get('svg')
    expect(svg.attributes('aria-hidden')).toBe('true')
    expect(w.get('button').element.firstElementChild?.tagName.toLowerCase()).toBe('svg')
  })

  it('renders the iconAfter component after the label', () => {
    const w = mount(GovnlButton, { props: { iconAfter: IconArrowRight }, slots: { default: 'Verder' } })
    expect(w.get('button').element.lastElementChild?.tagName.toLowerCase()).toBe('svg')
  })

  it('renders no icon svg when no icon is passed', () => {
    const w = mount(GovnlButton, { slots: { default: 'X' } })
    expect(w.find('svg').exists()).toBe(false)
  })

  it.each([
    ['primary', 'brand-btn-primary'],
    ['navy', 'brand-btn-navy'],
    ['secondary', 'brand-btn-secondary'],
    ['tertiary', 'brand-btn-link'],
    ['ghost', 'brand-btn-ghost'],
    ['danger', 'brand-btn-danger'],
  ] as const)('gives the %s variant the %s class', (variant, cls) => {
    const w = mount(GovnlButton, { props: { variant }, slots: { default: 'x' } })
    expect(w.get('button').classes()).toEqual(expect.arrayContaining(['brand-btn', cls]))
  })

  it('defaults to the CTA-coloured primary variant', () => {
    const w = mount(GovnlButton, { slots: { default: 'x' } })
    expect(w.get('button').classes()).toContain('brand-btn-primary')
  })

  it('applies the size classes, except to the link-style variant', () => {
    expect(mount(GovnlButton, { props: { size: 'sm' } }).get('button').classes()).toContain('brand-btn-sm')
    expect(mount(GovnlButton, { props: { size: 'lg' } }).get('button').classes()).toContain('brand-btn-lg')
    expect(mount(GovnlButton, { props: { size: 'sm', variant: 'tertiary' } }).get('button').classes())
      .not.toContain('brand-btn-sm')
  })

  it('renders an internal route through NuxtLink', () => {
    const w = mount(GovnlButton, {
      props: { to: '/dashboard/clusters' },
      slots: { default: 'Clusters' },
      global: { stubs: { NuxtLink: { props: ['to'], template: '<a class="nuxt-link" :href="to"><slot /></a>' } } },
    })
    const a = w.get('a.nuxt-link')
    expect(a.attributes('href')).toBe('/dashboard/clusters')
  })

  it('renders a disabled internal route as an anchor without a destination', () => {
    const w = mount(GovnlButton, { props: { to: '/dashboard', disabled: true }, slots: { default: 'x' } })
    const a = w.get('a')
    expect(a.attributes('href')).toBeUndefined()
    expect(a.attributes('aria-disabled')).toBe('true')
  })

  it('sets no inline colours', () => {
    const w = mount(GovnlButton, { props: { variant: 'danger' }, slots: { default: 'x' } })
    expect(w.get('button').attributes('style')).toBeUndefined()
  })
})
