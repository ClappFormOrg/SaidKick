import { describe, it, expect, vi, beforeEach } from 'vitest'

// The /branding/logo route proxies the API so the logo is same-origin and
// the CSP's img-src 'self' covers it (#158 Q5).

// In-cluster API Service origin the route proxies to.
const INTERNAL_ORIGIN = 'http://saidkick-hub-backend.test:8080'

const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13])

let headers: Record<string, string>
let config: { apiBaseInternal: string; public: { apiBase: string } }

beforeEach(() => {
  headers = {}
  config = { apiBaseInternal: INTERNAL_ORIGIN, public: { apiBase: 'https://api.example.org' } }
  vi.stubGlobal('defineEventHandler', (fn: unknown) => fn)
  vi.stubGlobal('useRuntimeConfig', () => config)
  vi.stubGlobal('setResponseHeaders', (_e: unknown, h: Record<string, string>) => { Object.assign(headers, h) })
  vi.stubGlobal('createError', (e: { statusCode: number }) => Object.assign(new Error(String(e.statusCode)), e))
})

async function handler() {
  return (await import('~/server/routes/branding/logo.get')).default as unknown as (event: unknown) => Promise<Uint8Array>
}

function upstream(body: BodyInit | null, init: ResponseInit & { contentType?: string } = {}) {
  const fetchMock = vi.fn().mockResolvedValue(new Response(body, {
    status: init.status ?? 200,
    headers: { 'content-type': init.contentType ?? 'image/png' },
  }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('GET /branding/logo', () => {
  it('serves the API logo as image/png from the internal API origin', async () => {
    const fetchMock = upstream(PNG, { contentType: 'text/html' })
    const body = await (await handler())({})
    expect(fetchMock.mock.calls[0][0]).toBe(`${INTERNAL_ORIGIN}/api/v1/branding/logo`)
    expect(Array.from(body)).toEqual(Array.from(PNG))
    expect(headers['content-type']).toBe('image/png')
    expect(headers['x-content-type-options']).toBe('nosniff')
    expect(headers['cache-control']).toBe('public, max-age=300')
  })

  it('passes a missing logo through as 404', async () => {
    upstream(null, { status: 404 })
    await expect((await handler())({})).rejects.toMatchObject({ statusCode: 404 })
  })

  it.each([
    ['an upstream error', () => upstream(null, { status: 500 })],
    ['a non-PNG body', () => upstream('<svg onload="alert(1)"/>')],
    ['an oversize body', () => upstream(new Uint8Array([...PNG, ...new Uint8Array(512 * 1024)]))],
    ['an unreachable API', () => vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('ECONNREFUSED')))],
  ])('answers 502 for %s', async (_name, setup) => {
    setup()
    await expect((await handler())({})).rejects.toMatchObject({ statusCode: 502 })
    expect(headers['content-type']).toBeUndefined()
  })

  it('answers 502 without an absolute API origin', async () => {
    config = { apiBaseInternal: '', public: { apiBase: '' } }
    const fetchMock = upstream(PNG)
    await expect((await handler())({})).rejects.toMatchObject({ statusCode: 502 })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
