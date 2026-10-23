import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref, type Ref } from 'vue'
import { useRoleGuard } from '~/composables/useRoleGuard'
import { useAuth } from '~/composables/useAuth'
import { STORAGE_KEY_ME } from '~/utils/constants'
import { createMockMeResponse } from '../../utils/mock'

const mockNavigate = vi.fn()
vi.stubGlobal('navigateTo', mockNavigate)

beforeEach(() => {
  localStorage.clear()
  mockNavigate.mockReset()
})

function mountGuard(allowed: Ref<boolean>) {
  return mount(defineComponent({
    setup() {
      useRoleGuard(allowed)
      return () => h('div')
    },
  }))
}

describe('useRoleGuard', () => {
  it('redirects to the dashboard when the profile lacks the role', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles: ['org_admin'] })))
    mountGuard(ref(false))
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
  })

  it('stays on the page when the profile has the role', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles: ['developer'] })))
    mountGuard(ref(true))
    expect(mockNavigate).not.toHaveBeenCalled()
  })

  it('waits for the profile before deciding', () => {
    mountGuard(ref(false))
    expect(mockNavigate).not.toHaveBeenCalled()
  })

  it('redirects with a real helper once a single-role user is known', () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles: ['platform_admin'] })))
    const { isAuditor } = useAuth()
    mountGuard(isAuditor)
    expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
  })

  it('stops watching once the page unmounts', async () => {
    localStorage.setItem(STORAGE_KEY_ME, JSON.stringify(createMockMeResponse({ roles: ['developer'] })))
    const allowed = ref(true)
    const w = mountGuard(allowed)
    w.unmount()
    allowed.value = false
    await nextTick()
    expect(mockNavigate).not.toHaveBeenCalled()
  })
})
