import type { Ref } from 'vue'
import { useAuth } from '~/composables/useAuth'

// Sends the user to the dashboard once their profile is known and lacks the
// role a page needs. Pages still hide their controls with v-if; this covers a
// route reached directly by URL. Call it before the first `await` in setup so
// the watcher and its cleanup bind to the component.
export function useRoleGuard(allowed: Ref<boolean>, redirectTo = '/dashboard') {
  const { me } = useAuth()
  const stop = watchEffect(() => {
    if (me.value && !allowed.value) navigateTo(redirectTo)
  })
  onUnmounted(stop)
}
