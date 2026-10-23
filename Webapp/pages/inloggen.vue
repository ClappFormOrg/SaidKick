<script setup lang="ts">
import { IconArrowRight, IconSearch } from '~/lib/icons'

useHead({ title: 'Aanmelden' })

const { publicApi, baseURL } = useApi()

interface FederatedOrg { slug: string; display_name: string }

const orgs        = ref<FederatedOrg[]>([])
const isLoading   = ref(true)
const fetchFailed = ref(false)
const search      = ref('')

onMounted(async () => {
  try {
    const data = await publicApi<{ items: FederatedOrg[] }>('/api/v1/orgs')
    orgs.value = data.items ?? []
  } catch {
    fetchFailed.value = true
  } finally {
    isLoading.value = false
  }
})

const filteredOrgs = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return orgs.value
  return orgs.value.filter(o => o.display_name.toLowerCase().includes(q) || o.slug.includes(q))
})

function loginWithOrg(slug: string) {
  const redirect = encodeURIComponent(window.location.origin + '/auth/complete')
  window.location.href = `${baseURL}/auth/login?org=${encodeURIComponent(slug)}&redirect=${redirect}`
}
</script>

<template>
  <div class="bg-brand-secondary">
    <div class="mx-auto flex min-h-[70vh] max-w-page items-center justify-center px-6 py-12">
      <div class="w-full max-w-[30rem] overflow-hidden rounded-card shadow-elev-3">
        <!-- One 40px corner cut on the cap, never all four. -->
        <div class="flex items-center gap-4 rounded-tr-cut bg-brand-primary px-8 py-7">
          <GovnlRijkslint :width="32" :height="52" reverse />
          <div>
            <p class="m-0 text-body font-bold leading-tight text-brand-white">Aanmelden</p>
            <p class="m-0 mt-0.5 text-sm text-white/80">AI-Appstore Dashboard</p>
          </div>
        </div>

        <div class="brand-card rounded-t-none border-t-0 p-8">
          <h1 class="brand-h3 mb-2 mt-0">Kies uw organisatie</h1>
          <p class="brand-prose mb-6 mt-0 text-govnl-grijs-700">
            Selecteer uw organisatie om in te loggen via de Identity Provider van uw organisatie.
          </p>

          <div class="relative mb-4">
            <IconSearch
              size="18"
              stroke="2"
              class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-govnl-grijs-600"
              aria-hidden="true"
            />
            <input
              v-model="search"
              type="search"
              placeholder="Zoek organisatie…"
              aria-label="Zoek organisatie"
              class="brand-input pl-10"
            />
          </div>

          <div v-if="isLoading" class="flex flex-col gap-2.5" aria-busy="true">
            <span class="sr-only">Organisaties worden geladen</span>
            <div
              v-for="i in 3"
              :key="i"
              class="h-12 animate-pulse rounded-card bg-brand-surface"
              aria-hidden="true"
            />
          </div>

          <UiAlert v-else-if="fetchFailed" tone="negative" title="Kan organisaties niet ophalen.">
            <p class="m-0">Controleer uw verbinding en probeer opnieuw.</p>
          </UiAlert>

          <ul v-else class="m-0 flex list-none flex-col gap-2 p-0">
            <li v-for="org in filteredOrgs" :key="org.slug">
              <button
                type="button"
                class="brand-card brand-card-interactive flex w-full cursor-pointer items-center justify-between gap-3 px-4 py-3 text-left font-semibold text-brand-primary"
                @click="loginWithOrg(org.slug)"
              >
                {{ org.display_name }}
                <IconArrowRight size="18" stroke="2" class="shrink-0" aria-hidden="true" />
              </button>
            </li>

            <li v-if="filteredOrgs.length === 0 && search" class="py-4 text-center text-govnl-grijs-600">
              Geen organisaties gevonden voor "<strong>{{ search }}</strong>".
            </li>
            <li v-if="filteredOrgs.length === 0 && !search" class="py-4 text-center text-govnl-grijs-600">
              Nog geen organisaties geconfigureerd.
            </li>
          </ul>
        </div>
      </div>
    </div>
  </div>
</template>
