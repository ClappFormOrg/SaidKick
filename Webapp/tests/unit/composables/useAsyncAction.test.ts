import { describe, it, expect } from 'vitest'
import { useAsyncAction } from '~/composables/useAsyncAction'

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('useAsyncAction', () => {
  it('passes arguments through and returns the result (happy)', async () => {
    const { run, error, fieldErrors } = useAsyncAction(async (a: number, b: number) => a + b, 'Mislukt.')
    await expect(run(2, 3)).resolves.toBe(5)
    expect(error.value).toBe('')
    expect(fieldErrors.value).toEqual([])
  })

  it('is pending only while the action runs', async () => {
    const d = deferred<void>()
    const { run, pending } = useAsyncAction(() => d.promise, 'Mislukt.')
    expect(pending.value).toBe(false)
    const p = run()
    expect(pending.value).toBe(true)
    d.resolve()
    await p
    expect(pending.value).toBe(false)
  })

  it('stores the API message and field errors when the action fails (unhappy)', async () => {
    const { run, pending, error, fieldErrors } = useAsyncAction(async () => {
      throw { status: 422, data: { message: 'Ongeldige invoer', errors: [{ path: 'name', message: 'verplicht' }] } }
    }, 'Mislukt.')
    await expect(run()).resolves.toBeUndefined()
    expect(pending.value).toBe(false)
    expect(error.value).toBe('Ongeldige invoer')
    expect(fieldErrors.value).toEqual([{ path: 'name', message: 'verplicht' }])
  })

  it('uses the fallback message for errors without an API message', async () => {
    const { run, error, fieldErrors } = useAsyncAction(async () => { throw new Error('network down') }, 'Mislukt.')
    await run()
    expect(error.value).toBe('Mislukt.')
    expect(fieldErrors.value).toEqual([])
  })

  it('clears the previous error when it runs again', async () => {
    let fail = true
    const { run, error } = useAsyncAction(async () => { if (fail) throw new Error('x') }, 'Mislukt.')
    await run()
    expect(error.value).toBe('Mislukt.')
    fail = false
    await run()
    expect(error.value).toBe('')
  })
})
