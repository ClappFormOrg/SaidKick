<script setup lang="ts">
// Landing page after a successful OIDC callback.
// The API has already set the saidkick_session cookie; fetchMe() calls
// /api/v1/me with credentials to get the user profile and caches it for the
// UI, then we redirect to the dashboard.

const { fetchMe } = useAuth()

const errorMsg = ref('')

onMounted(async () => {
  const meResp = await fetchMe()
  if (meResp) {
    await navigateTo('/dashboard')
  } else {
    errorMsg.value = 'Inloggen mislukt. Probeer het opnieuw.'
  }
})
</script>

<template>
  <div class="mx-auto flex min-h-[60vh] max-w-page items-center justify-center px-6 py-10">
    <div class="w-full max-w-[25rem] text-center">
      <p
        v-if="!errorMsg"
        role="status"
        class="brand-prose m-0 text-govnl-grijs-600"
      >
        Bezig met inloggen&hellip;
      </p>
      <UiAlert v-else tone="negative" :title="errorMsg" class="text-left">
        <NuxtLink to="/inloggen" class="brand-link font-semibold">Terug naar inloggen</NuxtLink>
      </UiAlert>
    </div>
  </div>
</template>
