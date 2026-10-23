import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UiSegmented from '~/components/UiSegmented.vue'

describe('UiSegmented', () => {
  it('renders a labelled group with the default grid and list options', () => {
    const w = mount(UiSegmented, { props: { modelValue: 'raster', label: 'Weergave' } })
    const group = w.get('[role="group"]')
    expect(group.attributes('aria-label')).toBe('Weergave')
    expect(w.findAll('button').map(b => b.text())).toEqual(['Raster', 'Lijst'])
    expect(w.findAll('svg')).toHaveLength(2)
  })

  it('marks only the active option as pressed', () => {
    const w = mount(UiSegmented, { props: { modelValue: 'lijst', label: 'Weergave' } })
    expect(w.findAll('button').map(b => b.attributes('aria-pressed'))).toEqual(['false', 'true'])
  })

  it('emits the chosen option', async () => {
    const w = mount(UiSegmented, { props: { modelValue: 'raster', label: 'Weergave' } })
    await w.findAll('button')[1]!.trigger('click')
    expect(w.emitted('update:modelValue')).toEqual([['lijst']])
  })

  it('does not emit when the active option is clicked again', async () => {
    const w = mount(UiSegmented, { props: { modelValue: 'raster', label: 'Weergave' } })
    await w.findAll('button')[0]!.trigger('click')
    expect(w.emitted('update:modelValue')).toBeUndefined()
  })

  it('takes custom options, with or without an icon', () => {
    const w = mount(UiSegmented, {
      props: {
        modelValue: 'b',
        label: 'Periode',
        options: [{ id: 'a', label: 'Week' }, { id: 'b', label: 'Maand' }, { id: 'c', label: 'Jaar' }],
      },
    })
    expect(w.findAll('button').map(b => b.text())).toEqual(['Week', 'Maand', 'Jaar'])
    expect(w.findAll('svg')).toHaveLength(0)
    expect(w.findAll('button')[1]!.attributes('aria-pressed')).toBe('true')
  })

  it('presses nothing when the value matches no option', () => {
    const w = mount(UiSegmented, { props: { modelValue: 'onbekend', label: 'Weergave' } })
    expect(w.findAll('button[aria-pressed="true"]')).toHaveLength(0)
  })
})
