import { describe, it, expect, vi, beforeEach } from 'vitest'
import authMiddleware, { handleAuthCheck } from '~/middleware/auth'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockMeResponse } from '../../utils/mock'

const mockNavigateTo = vi.fn()
vi.stubGlobal('navigateTo', mockNavigateTo)
const mockFetch = vi.fn()
vi.stubGlobal('$fetch', mockFetch)

beforeEach(() => {
  localStorage.clear()
})

describe('handleAuthCheck', () => {
  it('redirects to /inloggen when /me returns 401', async () => {
    mockFetch.mockRejectedValueOnce({ status: 401 })
    await handleAuthCheck()
    expect(mockNavigateTo).toHaveBeenCalledWith('/inloggen')
  })

  it('allows navigation when /me returns a profile', async () => {
    mockFetch.mockResolvedValueOnce(createMockMeResponse())
    await handleAuthCheck()
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/me', expect.objectContaining({
      credentials: 'include',
    }))
    expect(mockNavigateTo).not.toHaveBeenCalled()
  })

  it('redirects on an expired session even with a stale cached profile', async () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse()))
    mockFetch.mockRejectedValueOnce({ status: 401 })
    await handleAuthCheck()
    expect(mockNavigateTo).toHaveBeenCalledWith('/inloggen')
    expect(localStorage.getItem(STORAGE_KEY_ME)).toBeNull()
  })

  it('ignores the legacy aia_user entry', async () => {
    localStorage.setItem('aia_user', JSON.stringify({ id: 'u1' }))
    mockFetch.mockRejectedValueOnce({ status: 401 })
    await handleAuthCheck()
    expect(mockNavigateTo).toHaveBeenCalledWith('/inloggen')
  })
})

describe('auth route middleware (default export)', () => {
  it('runs the client guard and redirects an unauthenticated visitor', async () => {
    mockFetch.mockRejectedValueOnce({ status: 401 })
    await (authMiddleware as unknown as () => Promise<void>)()
    expect(mockNavigateTo).toHaveBeenCalledWith('/inloggen')
  })

  it('lets an authenticated visitor through', async () => {
    mockFetch.mockResolvedValueOnce(createMockMeResponse())
    await (authMiddleware as unknown as () => Promise<void>)()
    expect(mockNavigateTo).not.toHaveBeenCalled()
  })
})
