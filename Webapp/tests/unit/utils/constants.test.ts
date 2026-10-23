import { describe, it, expect } from 'vitest'
import {
  formatDate,
  REGEX_EMAIL,
  STORAGE_KEY_ME,
  COPY_FEEDBACK_MS,
} from '~/utils/constants'

describe('STORAGE_KEY_ME', () => {
  it('equals aia_me', () => {
    expect(STORAGE_KEY_ME).toBe('aia_me')
  })
})

describe('COPY_FEEDBACK_MS', () => {
  it('equals 2000', () => {
    expect(COPY_FEEDBACK_MS).toBe(2_000)
  })
})

describe('REGEX_EMAIL', () => {
  it('accepts standard email addresses', () => {
    expect(REGEX_EMAIL.test('user@example.com')).toBe(true)
    expect(REGEX_EMAIL.test('first.last@example.com')).toBe(true)
  })

  it('accepts emails with + tags and subdomains', () => {
    expect(REGEX_EMAIL.test('user+tag@mail.domain.org')).toBe(true)
    expect(REGEX_EMAIL.test('a@b.c')).toBe(true)
  })

  it('rejects missing @ symbol', () => {
    expect(REGEX_EMAIL.test('notanemail')).toBe(false)
    expect(REGEX_EMAIL.test('user.domain.com')).toBe(false)
  })

  it('rejects missing local part', () => {
    expect(REGEX_EMAIL.test('@domain.com')).toBe(false)
  })

  it('rejects missing domain', () => {
    expect(REGEX_EMAIL.test('user@')).toBe(false)
  })

  it('rejects empty string', () => {
    expect(REGEX_EMAIL.test('')).toBe(false)
  })

  it('rejects addresses with spaces', () => {
    expect(REGEX_EMAIL.test('user @domain.com')).toBe(false)
    expect(REGEX_EMAIL.test('user@ domain.com')).toBe(false)
  })
})

describe('formatDate', () => {
  it('returns em-dash for undefined', () => {
    expect(formatDate(undefined)).toBe('—')
  })

  it('returns em-dash for empty string', () => {
    expect(formatDate('')).toBe('—')
  })

  it('returns a non-empty string for a valid ISO date', () => {
    const result = formatDate('2024-06-15T00:00:00.000Z')
    expect(typeof result).toBe('string')
    expect(result.length).toBeGreaterThan(0)
  })

  it('includes the year in the output', () => {
    expect(formatDate('2024-06-15T00:00:00.000Z')).toContain('2024')
    expect(formatDate('2019-03-01T00:00:00.000Z')).toContain('2019')
  })

  it('matches the local toLocaleDateString format used before (date only by default)', () => {
    const iso = '2024-06-15T10:30:00.000Z'
    expect(formatDate(iso)).toBe(
      new Date(iso).toLocaleDateString('nl-NL', { day: 'numeric', month: 'short', year: 'numeric' }),
    )
  })

  it('adds hours and minutes with withTime', () => {
    const iso = '2024-06-15T10:30:00.000Z'
    expect(formatDate(iso, { withTime: true })).toBe(
      new Date(iso).toLocaleDateString('nl-NL', {
        day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit',
      }),
    )
    expect(formatDate(iso, { withTime: true })).not.toBe(formatDate(iso))
  })

  it('formats different months distinctly', () => {
    const jan = formatDate('2024-01-15T00:00:00.000Z')
    const dec = formatDate('2024-12-15T00:00:00.000Z')
    expect(jan).not.toBe(dec)
  })
})
