import { describe, it, expect } from 'vitest'
import {
  stateToStatus,
  riskClassToLabel,
  deriveScores,
  mapCard,
  buildDocs,
  mapDetail,
  statusDotColor,
  autoValColor,
  autoValLabel,
  riskLabel,
  riskColor,
  CATEGORIES,
  CRITERIA,
  SCORE,
} from '~/composables/useData'
import { createMockAppCardAPI, createMockAppDetailAPI } from '../../utils/mock'

// ─── stateToStatus ────────────────────────────────────────────────────────────

describe('stateToStatus', () => {
  it.each([
    ['published',     'Productie'],
    ['submitted',     'Pilot'],
    ['needs_changes', 'Testfase'],
    ['draft',         'Concept'],
    ['archived',      'Uitfaserend'],
    ['unknown',       'Concept'],
    ['',              'Concept'],
  ])('maps %s → %s', (state, expected) => {
    expect(stateToStatus(state)).toBe(expected)
  })
})

// ─── riskClassToLabel ─────────────────────────────────────────────────────────

describe('riskClassToLabel', () => {
  it.each([
    ['minimal',    'Minimaal risico'],
    ['limited',    'Beperkt risico'],
    ['high',       'Hoog risico'],
    ['',           'Onbekend'],
    ['prohibited', 'prohibited'],
  ])('maps %s → %s', (rc, expected) => {
    expect(riskClassToLabel(rc)).toBe(expected)
  })
})

// ─── deriveScores ─────────────────────────────────────────────────────────────

describe('deriveScores', () => {
  it('returns all pass when audit_approved is true', () => {
    const scores = deriveScores(undefined, true)
    const values = Object.values(scores)
    expect(values).toHaveLength(6)
    expect(values.every(v => v === 'pass')).toBe(true)
  })

  it('returns bio/risk=pass and others=pending when auto_validation passed', () => {
    const scores = deriveScores('passed', false)
    expect(scores.bio).toBe('pass')
    expect(scores.risk).toBe('pass')
    expect(scores.dpia).toBe('pending')
    expect(scores.a11y).toBe('pending')
    expect(scores.oss).toBe('pending')
    expect(scores.local).toBe('pending')
  })

  it('returns bio/risk=fail and others=pending when auto_validation failed', () => {
    const scores = deriveScores('failed', false)
    expect(scores.bio).toBe('fail')
    expect(scores.risk).toBe('fail')
    expect(scores.dpia).toBe('pending')
  })

  it('returns empty object when no state or approval', () => {
    expect(deriveScores()).toEqual({})
    expect(deriveScores('pending')).toEqual({})
  })

  it('approved takes precedence over state', () => {
    const scores = deriveScores('failed', true)
    expect(Object.values(scores).every(v => v === 'pass')).toBe(true)
  })
})

// ─── buildDocs ────────────────────────────────────────────────────────────────

describe('buildDocs', () => {
  it('returns empty array for empty spec', () => {
    expect(buildDocs('org/repo', {})).toEqual([])
  })

  it('builds a DPIA doc URL from audit.dpia', () => {
    const docs = buildDocs('org/repo', { audit: { dpia: './docs/dpia.pdf' } })
    expect(docs).toHaveLength(1)
    expect(docs[0].label).toBe('DPIA-rapport')
    expect(docs[0].url).toBe('https://github.com/org/repo/blob/main/docs/dpia.pdf')
  })

  it('strips leading ./ from document paths', () => {
    const docs = buildDocs('org/repo', { audit: { security_scan: './sec/report.pdf' } })
    expect(docs[0].url).toContain('sec/report.pdf')
    expect(docs[0].url).not.toContain('./')
  })

  it('builds an API doc URL from an openapi interface', () => {
    const docs = buildDocs('org/repo', {
      interfaces: [{ type: 'api', openapi: './openapi.yaml' }],
    })
    expect(docs).toHaveLength(1)
    expect(docs[0].label).toBe('API-documentatie')
    expect(docs[0].url).toContain('openapi.yaml')
  })

  it('ignores non-api interface types', () => {
    const docs = buildDocs('org/repo', {
      interfaces: [{ type: 'grpc' }],
    })
    expect(docs).toHaveLength(0)
  })

  it('collects all audit and interface docs together', () => {
    const docs = buildDocs('org/repo', {
      audit: { dpia: './dpia.pdf', ai_risk: './ai-risk.pdf' },
      interfaces: [{ type: 'api', openapi: './api.yaml' }],
    })
    expect(docs).toHaveLength(3)
  })
})

// ─── mapCard ──────────────────────────────────────────────────────────────────

describe('mapCard', () => {
  it('maps required fields from the API response', () => {
    const app = createMockAppCardAPI()
    const result = mapCard(app)
    expect(result.id).toBe('app-1')
    expect(result.slug).toBe('test-app')
    expect(result.eigenaar).toBe('Test Vendor')
    expect(result.licentie).toBe('MIT')
    expect(result.repo).toBe('org/test-app')
  })

  it('prefers display_name over slug for name', () => {
    expect(mapCard(createMockAppCardAPI({ display_name: 'Nice Name' })).name).toBe('Nice Name')
    expect(mapCard(createMockAppCardAPI({ display_name: undefined })).name).toBe('test-app')
  })

  it('uses first category as category and all as tags', () => {
    const result = mapCard(createMockAppCardAPI({ categories: ['agent', 'rag'] }))
    expect(result.category).toBe('agent')
    expect(result.tags).toEqual(['agent', 'rag'])
  })

  it('defaults category and tags to empty when categories is absent', () => {
    const result = mapCard(createMockAppCardAPI({ categories: undefined }))
    expect(result.category).toBe('')
    expect(result.tags).toEqual([])
  })

  it('converts state to Dutch status label', () => {
    expect(mapCard(createMockAppCardAPI({ state: 'published' })).status).toBe('Productie')
    expect(mapCard(createMockAppCardAPI({ state: 'draft' })).status).toBe('Concept')
  })

  it('converts risk_class to Dutch label', () => {
    const result = mapCard(createMockAppCardAPI({ risk_class: 'high' }))
    expect(result.riskCategory).toBe('Hoog risico')
  })

  it('derives scores from auto_validation_state', () => {
    const passed = mapCard(createMockAppCardAPI({ auto_validation_state: 'passed', audit_approved: false }))
    expect(passed.scores.bio).toBe('pass')

    const approved = mapCard(createMockAppCardAPI({ audit_approved: true }))
    expect(Object.values(approved.scores).every(v => v === 'pass')).toBe(true)
  })

  it('initialises installs to 0', () => {
    expect(mapCard(createMockAppCardAPI()).installs).toBe(0)
  })

  it('takes the version from chart_version on the card', () => {
    expect(mapCard(createMockAppCardAPI({ chart_version: '1.4.2' })).versie).toBe('1.4.2')
    expect(mapCard(createMockAppCardAPI({ chart_version: undefined })).versie).toBe('')
  })
})

// ─── mapDetail ────────────────────────────────────────────────────────────────

describe('mapDetail', () => {
  it('overrides name from manifest metadata', () => {
    const result = mapDetail(createMockAppDetailAPI())
    expect(result.name).toBe('Test App (Manifest)')
  })

  it('overrides eigenaar from manifest metadata', () => {
    const result = mapDetail(createMockAppDetailAPI())
    expect(result.eigenaar).toBe('Manifest Vendor')
  })

  it('overrides licentie from manifest metadata', () => {
    const result = mapDetail(createMockAppDetailAPI())
    expect(result.licentie).toBe('Apache-2.0')
  })

  it('uses manifest spec categories for tags', () => {
    const result = mapDetail(createMockAppDetailAPI())
    expect(result.tags).toEqual(['chatbot', 'rag'])
    expect(result.category).toBe('chatbot')
  })

  it('formats bijgewerkt as Dutch locale date', () => {
    const result = mapDetail(createMockAppDetailAPI({ updated_at: '2024-06-15T00:00:00.000Z' }))
    expect(result.bijgewerkt).toContain('2024')
  })

  it('builds docs from manifest spec', () => {
    const result = mapDetail(createMockAppDetailAPI())
    expect(result.docs).toBeDefined()
    expect(result.docs!.length).toBeGreaterThan(0)
    const labels = result.docs!.map(d => d.label)
    expect(labels).toContain('DPIA-rapport')
    expect(labels).toContain('API-documentatie')
  })

  it('carries versie and installs from the API response', () => {
    const result = mapDetail(createMockAppDetailAPI({ versie: '2.0.0', installs: 99 }))
    expect(result.versie).toBe('2.0.0')
    expect(result.installs).toBe(99)
  })

  it('takes the version from chart_version, the field the API sends', () => {
    const result = mapDetail(createMockAppDetailAPI({ versie: undefined, chart_version: '3.1.0' }))
    expect(result.versie).toBe('3.1.0')
  })

  it('links criterion evidence only for audit documents the manifest names', () => {
    const result = mapDetail(createMockAppDetailAPI())
    expect(result.evidence).toEqual({ dpia: 'https://github.com/org/test-app/blob/main/docs/dpia.pdf' })
  })

  it('falls back gracefully when manifest is absent', () => {
    const detail = createMockAppDetailAPI({ manifest: undefined })
    const result = mapDetail(detail)
    expect(result.slug).toBe('test-app')
    expect(result.docs).toBeUndefined()
  })
})

// ─── statusDotColor ──────────────────────────────────────────────────────────

describe('statusDotColor', () => {
  it.each([
    ['Productie', 'var(--govnl-color-groen-500)'],
    ['Pilot', 'var(--govnl-color-hemelblauw-500)'],
    ['Testfase', 'var(--govnl-color-oranje-500)'],
    ['Concept', 'var(--govnl-color-grijs-500)'],
    ['Uitfaserend', 'var(--govnl-color-rood-500)'],
  ])('%s gets %s', (status, color) => {
    expect(statusDotColor(status)).toBe(color)
  })

  it('falls back to grey for an unknown status', () => {
    expect(statusDotColor('unknown-status')).toBe(statusDotColor('Concept'))
  })

  it('uses only feedback and grey colours', () => {
    for (const s of ['Productie', 'Pilot', 'Testfase', 'Concept', 'Uitfaserend']) {
      expect(statusDotColor(s)).toMatch(/^var\(--govnl-color-(groen|hemelblauw|oranje|grijs|rood)-\d+\)$/)
    }
  })
})

// ─── autoValColor ─────────────────────────────────────────────────────────────

describe('autoValColor', () => {
  it('returns green classes for passed', () => {
    const { bg, fg } = autoValColor('passed')
    expect(bg).toBe('bg-govnl-groen-50')
    expect(fg).toBe('text-govnl-groen-700')
  })

  it('returns red classes for failed', () => {
    const { bg, fg } = autoValColor('failed')
    expect(bg).toBe('bg-govnl-rood-50')
    expect(fg).toBe('text-govnl-rood-700')
  })

  it('returns grey classes for pending and unknown', () => {
    expect(autoValColor('pending')).toEqual({ bg: 'bg-govnl-grijs-100', fg: 'text-govnl-grijs-600' })
    expect(autoValColor('')).toEqual(autoValColor('pending'))
  })
})

// ─── autoValLabel ─────────────────────────────────────────────────────────────

describe('autoValLabel', () => {
  it.each([
    ['passed',  'Geslaagd'],
    ['failed',  'Mislukt'],
    ['pending', 'In behandeling'],
    ['',        'In behandeling'],
    ['other',   'In behandeling'],
  ])('maps %s → %s', (state, expected) => {
    expect(autoValLabel(state)).toBe(expected)
  })
})

// ─── riskLabel ───────────────────────────────────────────────────────────────

describe('riskLabel', () => {
  it.each([
    ['minimal',    'Minimaal'],
    ['limited',    'Beperkt'],
    ['high',       'Hoog'],
    ['',           '—'],
    ['prohibited', 'prohibited'],
  ])('maps %s → %s', (rc, expected) => {
    expect(riskLabel(rc)).toBe(expected)
  })
})

// ─── riskColor ───────────────────────────────────────────────────────────────

describe('riskColor', () => {
  it('returns red classes for high', () => {
    expect(riskColor('high')).toEqual({ bg: 'bg-govnl-rood-50', fg: 'text-govnl-rood-700' })
  })

  it('returns orange classes for limited', () => {
    expect(riskColor('limited')).toEqual({ bg: 'bg-govnl-oranje-50', fg: 'text-govnl-oranje-700' })
  })

  it('returns green classes for minimal and unknown', () => {
    expect(riskColor('minimal')).toEqual({ bg: 'bg-govnl-groen-50', fg: 'text-govnl-groen-700' })
    expect(riskColor('')).toEqual(riskColor('minimal'))
  })
})

// ─── CATEGORIES ───────────────────────────────────────────────────────────────

describe('CATEGORIES', () => {
  it('is an array with 6 entries', () => {
    expect(CATEGORIES).toHaveLength(6)
  })

  it('each entry has slug, title and icon, and no colour of its own', () => {
    for (const cat of CATEGORIES) {
      expect(typeof cat.slug).toBe('string')
      expect(typeof cat.title).toBe('string')
      expect(typeof cat.icon).toBe('string')
      expect(cat).not.toHaveProperty('color')
    }
  })

  it('all slugs are unique', () => {
    const slugs = CATEGORIES.map(c => c.slug)
    expect(new Set(slugs).size).toBe(slugs.length)
  })
})

// ─── CRITERIA ─────────────────────────────────────────────────────────────────

describe('CRITERIA', () => {
  it('is an array with 6 entries', () => {
    expect(CRITERIA).toHaveLength(6)
  })

  it('contains the six required audit criteria keys', () => {
    const keys = CRITERIA.map(c => c.key)
    for (const expected of ['dpia', 'bio', 'a11y', 'risk', 'oss', 'local']) {
      expect(keys).toContain(expected)
    }
  })
})

// ─── SCORE ────────────────────────────────────────────────────────────────────

describe('SCORE', () => {
  it('defines all four score levels', () => {
    expect(Object.keys(SCORE)).toEqual(
      expect.arrayContaining(['pass', 'partial', 'fail', 'pending']),
    )
  })

  it('each level has a label and color', () => {
    for (const [, value] of Object.entries(SCORE)) {
      expect(typeof value.label).toBe('string')
      expect(typeof value.color).toBe('string')
    }
  })

  it('pass and fail have distinct colors', () => {
    expect(SCORE.pass.color).not.toBe(SCORE.fail.color)
  })
})
