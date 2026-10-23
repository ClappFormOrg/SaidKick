import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import ErrorPage from '~/error.vue'

const slotStub = (tag: string) => defineComponent({
  setup(_, { slots }) {
    return () => h(tag, slots.default?.())
  },
})

const clearError = vi.fn()

beforeEach(() => {
  vi.stubGlobal('useHead', () => {})
  vi.stubGlobal('clearError', clearError)
})

function render(error: Record<string, unknown>) {
  return mount(ErrorPage, {
    props: { error: error as never },
    global: { stubs: { NuxtLayout: slotStub('div'), GovnlButton: slotStub('button') } },
  })
}

describe('error page', () => {
  it('renders Dutch copy for a 404', () => {
    const w = render({ statusCode: 404 })
    expect(w.text()).toContain('404')
    expect(w.text()).toContain('Pagina niet gevonden')
    expect(w.text()).toContain('Naar de catalogus')
  })

  it('treats an unknown status as a server fault', () => {
    const w = render({ statusCode: 418 })
    expect(w.text()).toContain('Er ging iets mis')
  })

  it('never prints the error message or stack outside development', () => {
    const w = render({
      statusCode: 500,
      message: 'open C:/Users/someone/secret/file.ts',
      stack: 'Error: boom\n    at C:/Users/someone/secret/file.ts:1:1',
    })
    expect(w.text()).not.toContain('C:/Users')
    expect(w.find('details').exists()).toBe(false)
  })

  it('clears the error when navigating away', async () => {
    const w = render({ statusCode: 404 })
    await w.findAll('button')[0]!.trigger('click')
    expect(clearError).toHaveBeenCalledWith({ redirect: '/catalogus' })
  })
})
