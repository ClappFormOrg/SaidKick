import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UiScoreDots from '~/components/UiScoreDots.vue'
import { CRITERIA } from '~/composables/useData'

const ALL_PASS = { dpia: 'pass', bio: 'pass', a11y: 'pass', risk: 'pass', oss: 'pass', local: 'pass' }

describe('UiScoreDots', () => {
  it('renders one dot per criterion', () => {
    const w = mount(UiScoreDots, { props: { scores: ALL_PASS } })
    // wrapper div (role=img) + one child dot per criterion
    const dots = w.findAll('[aria-hidden="true"]')
    expect(dots).toHaveLength(CRITERIA.length)
  })

  it('exposes an accessible summary via role=img + aria-label (happy: all pass)', () => {
    const w = mount(UiScoreDots, { props: { scores: ALL_PASS } })
    const root = w.get('[role="img"]')
    const label = root.attributes('aria-label') ?? ''
    expect(label.startsWith('Criteria:')).toBe(true)
    expect(label).toContain('DPIA voldoet')
    expect(label).toContain('Lokaal te draaien voldoet')
  })

  it('labels missing scores as "in beoordeling" (unhappy: incomplete data)', () => {
    const w = mount(UiScoreDots, { props: { scores: {} } })
    const label = w.get('[role="img"]').attributes('aria-label') ?? ''
    expect(label).toContain('DPIA in beoordeling')
  })

  it('labels an unrecognised score value as "in beoordeling" (unhappy: bad value)', () => {
    const w = mount(UiScoreDots, { props: { scores: { dpia: 'garbage' } } })
    const label = w.get('[role="img"]').attributes('aria-label') ?? ''
    expect(label).toContain('DPIA in beoordeling')
  })

  it('reflects a failing criterion in the aria-label', () => {
    const w = mount(UiScoreDots, { props: { scores: { ...ALL_PASS, bio: 'fail' } } })
    const label = w.get('[role="img"]').attributes('aria-label') ?? ''
    expect(label).toContain('BIO-conform voldoet niet')
  })

  it('colours a failing dot red and a passing dot green', () => {
    const w = mount(UiScoreDots, { props: { scores: { ...ALL_PASS, bio: 'fail' } } })
    const styles = w.findAll('[aria-hidden="true"]').map(d => d.attributes('style') ?? '')
    expect(styles.some(s => s.includes('var(--govnl-color-rood-500)'))).toBe(true)
    expect(styles.some(s => s.includes('var(--govnl-color-groen-500)'))).toBe(true)
  })
})
