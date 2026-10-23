import { errorMessage, isFetchError, type FieldError } from '~/composables/useApi'

// Wraps a user-triggered async action (form submit, delete, ...) with the
// pending flag and error state pages used to hand-roll. `run` resolves to the
// action's result, or undefined when it failed; it never throws.
export function useAsyncAction<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  fallbackMsg: string,
) {
  const pending = ref(false)
  const error = ref('')
  const fieldErrors = ref<FieldError[]>([])

  async function run(...args: A): Promise<R | undefined> {
    pending.value = true
    error.value = ''
    fieldErrors.value = []
    try {
      return await fn(...args)
    } catch (err: unknown) {
      error.value = errorMessage(err, fallbackMsg)
      fieldErrors.value = isFetchError(err) ? (err.data?.errors ?? []) : []
      return undefined
    } finally {
      pending.value = false
    }
  }

  return { run, pending, error, fieldErrors }
}
