<script setup lang="ts">
import type { NuxtError } from '#app'
import { IconArrowLeft, IconSearch } from '~/lib/icons'

// Branded error page. Without it Nuxt renders its own fallback: a bare dark
// screen with an English "Go back home" button and no site chrome.
//
// It renders inside <NuxtLayout> so the header, navigation and footer stay.
// It never prints error.stack or error.message in production: a page that
// echoed the raw message could disclose server paths or upstream detail.
const props = defineProps<{ error: NuxtError }>()

const status = computed(() => Number(props.error?.statusCode) || 500)

// Copy per status. Anything unrecognised is treated as a server fault, which
// says nothing about the request.
const COPY: Record<number, { title: string; lead: string }> = {
  400: {
    title: 'Ongeldig verzoek',
    lead: 'Deze pagina is opgevraagd zonder de gegevens die zij nodig heeft. Begin opnieuw vanaf de catalogus.',
  },
  401: {
    title: 'Niet ingelogd',
    lead: 'Log in om deze pagina te bekijken.',
  },
  403: {
    title: 'Geen toegang',
    lead: 'U heeft geen rechten voor deze pagina. Neem contact op met uw organisatiebeheerder als dit onjuist lijkt.',
  },
  404: {
    title: 'Pagina niet gevonden',
    lead: 'Deze pagina bestaat niet of is verplaatst. Controleer de link, of zoek de applicatie op in de catalogus.',
  },
  500: {
    title: 'Er ging iets mis',
    lead: 'Er is een onverwachte fout opgetreden. Probeer het later opnieuw of neem contact op met de servicedesk.',
  },
  503: {
    title: 'Tijdelijk niet beschikbaar',
    lead: 'Het platform is even niet bereikbaar, waarschijnlijk door onderhoud. Probeer het over enkele minuten opnieuw.',
  },
}

const copy = computed(() => COPY[status.value] ?? COPY[500]!)

useHead({ title: computed(() => copy.value.title) })

// Technical detail for development only. import.meta.dev is compiled out of
// a production bundle, so this cannot render on a deployed instance.
const devDetail = computed(() => {
  if (!import.meta.dev) return ''
  return props.error?.message || props.error?.statusMessage || ''
})

// Clears the error state before navigating, so the error page does not stick.
async function goTo(path: string) {
  await clearError({ redirect: path })
}
</script>

<template>
  <NuxtLayout>
    <div class="mx-auto max-w-page px-6 py-16">
      <div class="max-w-[42rem] rounded-tl-cut border border-brand-border bg-brand-white p-8 sm:p-11">
        <p class="m-0 text-body-lg font-bold text-brand-cta">{{ status }}</p>

        <h1 class="brand-h1 mb-4 mt-2">{{ copy.title }}</h1>

        <p class="brand-lead m-0">{{ copy.lead }}</p>

        <div class="mt-8 flex flex-wrap gap-3">
          <GovnlButton :icon="IconSearch" @click="goTo('/catalogus')">
            Naar de catalogus
          </GovnlButton>
          <GovnlButton variant="secondary" :icon="IconArrowLeft" @click="goTo('/')">
            Terug naar home
          </GovnlButton>
        </div>

        <details v-if="devDetail" class="mt-8 border-t border-brand-border pt-5">
          <summary class="cursor-pointer text-base font-semibold text-govnl-grijs-600">
            Technische details (alleen in ontwikkeling)
          </summary>
          <pre class="mt-3 overflow-x-auto rounded-card bg-brand-surface p-4 text-sm text-brand-body">{{ devDetail }}</pre>
        </details>
      </div>
    </div>
  </NuxtLayout>
</template>
