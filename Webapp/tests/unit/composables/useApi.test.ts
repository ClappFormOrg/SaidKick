import { describe, it, expect, vi, beforeEach } from 'vitest'
import { errorMessage, isFetchError, useApi } from '~/composables/useApi'

const mockCreate = vi.fn((defaults: Record<string, unknown>) => ({ defaults }))

beforeEach(() => {
  vi.stubGlobal('$fetch', Object.assign(vi.fn(), { create: mockCreate }))
})

describe('useApi', () => {
  it('returns the runtime-configured API base URL', () => {
    expect(useApi().baseURL).toBe('http://localhost:8080')
  })

  it('api is a $fetch client that always sends the session cookie to the API', () => {
    const { api } = useApi()
    expect(api).toEqual({ defaults: { baseURL: 'http://localhost:8080', credentials: 'include' } })
  })

  it('publicApi targets the API without forcing credentials', () => {
    const { publicApi } = useApi()
    expect(publicApi).toEqual({ defaults: { baseURL: 'http://localhost:8080' } })
  })
})

describe('errorMessage', () => {
  it('returns the API message from a fetch error (happy)', () => {
    expect(errorMessage({ status: 409, data: { message: 'Naam bestaat al' } }, 'Mislukt.')).toBe('Naam bestaat al')
  })

  it('falls back when the fetch error carries no message', () => {
    expect(errorMessage({ status: 500, data: {} }, 'Mislukt.')).toBe('Mislukt.')
  })

  it('falls back for non-fetch errors (unhappy)', () => {
    expect(errorMessage(new Error('network down'), 'Mislukt.')).toBe('Mislukt.')
    expect(errorMessage(undefined, 'Mislukt.')).toBe('Mislukt.')
  })
})

describe('isFetchError', () => {
  it('returns true when the object has a data property', () => {
    expect(isFetchError({ data: { message: 'Not Found' } })).toBe(true)
  })

  it('returns true when the object has a status property', () => {
    expect(isFetchError({ status: 404 })).toBe(true)
  })

  it('returns true when the object has both data and status', () => {
    expect(isFetchError({ data: { message: 'Unauthorized' }, status: 401 })).toBe(true)
  })

  it('returns true for a fetch error with error array in data', () => {
    expect(
      isFetchError({
        status: 422,
        data: { errors: [{ path: 'email', message: 'invalid' }] },
      }),
    ).toBe(true)
  })

  it('returns false for null', () => {
    expect(isFetchError(null)).toBe(false)
  })

  it('returns false for undefined', () => {
    expect(isFetchError(undefined)).toBe(false)
  })

  it('returns false for a string', () => {
    expect(isFetchError('error occurred')).toBe(false)
  })

  it('returns false for a number', () => {
    expect(isFetchError(404)).toBe(false)
  })

  it('returns false for an empty object', () => {
    expect(isFetchError({})).toBe(false)
  })

  it('returns false for an object without data or status', () => {
    expect(isFetchError({ message: 'something went wrong' })).toBe(false)
    expect(isFetchError({ code: 'ERR_TIMEOUT' })).toBe(false)
  })
})
