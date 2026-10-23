<script setup lang="ts">
import {
  IconSearch, IconShieldCheck, IconLock, IconEye, IconScale,
  IconCloud, IconApps, IconArrowRight, IconRocket,
} from '~/lib/icons'
import { CATEGORIES, CRITERIA, useApps } from '~/composables/useData'

useHead({ title: 'AI-Appstore voor publieke organisaties' })

const router = useRouter()
const heroSearch = ref('')

function submitHeroSearch() {
  if (heroSearch.value.trim()) {
    router.push(`/catalogus?q=${encodeURIComponent(heroSearch.value.trim())}`)
  } else {
    router.push('/catalogus')
  }
}

const { data: apps } = await useApps()
const featuredApps = computed(() => (apps.value ?? []).filter(a => a.status === 'Productie').slice(0, 4))

const categoryCounts = computed(() =>
  Object.fromEntries(
    CATEGORIES.map(c => [c.slug, (apps.value ?? []).filter(a => a.category === c.slug).length]),
  ),
)

// No partner count here: the provinces have not confirmed taking part yet.
const stats = computed(() => [
  { value: apps.value?.length ?? 0, label: 'getoetste applicaties' },
  { value: CATEGORIES.length, label: 'onderwerpen' },
  { value: CRITERIA.length, label: 'toelatingscriteria per applicatie' },
])

const trustItems = [
  { icon: IconShieldCheck, label: 'DPIA beoordeeld' },
  { icon: IconLock,        label: 'BIO-conform' },
  { icon: IconEye,         label: 'WCAG 2.2 AA' },
  { icon: IconScale,       label: 'AI-Verordening getoetst' },
  { icon: IconApps,        label: 'Open-source' },
  { icon: IconCloud,       label: 'Cloud-agnostisch' },
]

const howSteps = [
  {
    n: '1',
    title: 'Selecteer',
    body: 'Doorzoek de catalogus op onderwerp, status of toelatingsscore en vergelijk applicaties.',
  },
  {
    n: '2',
    title: 'Configureer',
    body: 'Download de Helm-chart of container-image en pas de configuratie aan uw omgeving aan.',
  },
  {
    n: '3',
    title: 'Deploy',
    body: 'Installeer op uw eigen cluster, SURF of provincie-cloud. Geen externe afhankelijkheid.',
  },
  {
    n: '4',
    title: 'Monitor',
    body: 'Ontvang security-advisories en updates via het dashboard of uw eigen CI/CD-pipeline.',
  },
]

const quickChips = ['Chatbots', 'RAG', 'Agents', 'Documenten', 'Nederlands']
</script>

<template>
  <!-- HERO -->
  <UiHeroWedge :eyebrow="'Voor provincies & publieke organisaties'">
    <h1 id="hero-heading" class="brand-display mb-5">
      Betrouwbare AI-applicaties, getoetst door provincies.
    </h1>

    <p class="brand-lead mb-8 mt-0">
      De sAId-Kick AI-Appstore brengt provincies en publieke organisaties samen rondom veilige, open-source AI-oplossingen. Iedere applicatie is beoordeeld op DPIA, BIO, toegankelijkheid en de EU AI Act.
    </p>

    <form
      role="search"
      class="flex w-full max-w-[32.5rem] gap-2"
      @submit.prevent="submitHeroSearch"
    >
      <input
        v-model="heroSearch"
        type="search"
        placeholder="Zoek een applicatie..."
        aria-label="Zoek een applicatie"
        class="brand-input"
      />
      <!-- The search is the main action of this page, so it carries the CTA colour. -->
      <button type="submit" class="brand-btn brand-btn-primary">
        <IconSearch size="18" stroke="2" aria-hidden="true" />
        Zoek
      </button>
    </form>

    <ul class="mb-0 mt-5 flex list-none flex-wrap gap-2 p-0">
      <li v-for="chip in quickChips" :key="chip">
        <NuxtLink
          :to="`/catalogus?q=${encodeURIComponent(chip)}`"
          class="inline-flex rounded-full border border-brand-border bg-brand-white px-3.5 py-1 text-sm font-medium text-brand-body no-underline hover:bg-brand-surface"
        >{{ chip }}</NuxtLink>
      </li>
    </ul>
  </UiHeroWedge>

  <UiStatsBar :stats="stats" />

  <!-- TRUST STRIP -->
  <section aria-label="Vertrouwenskenmerken" class="mx-auto max-w-page px-6 pt-14">
    <div class="brand-card flex flex-wrap items-center justify-between gap-4 p-5">
      <ul class="m-0 flex list-none flex-wrap items-center gap-x-5 gap-y-2 p-0">
        <li
          v-for="item in trustItems"
          :key="item.label"
          class="flex items-center gap-2 text-brand-body"
        >
          <component :is="item.icon" size="18" stroke="2" class="text-brand-primary" aria-hidden="true" />
          {{ item.label }}
        </li>
      </ul>
      <NuxtLink to="/toelating" class="brand-link whitespace-nowrap font-semibold">
        Bekijk het volledige beoordelingskader &rarr;
      </NuxtLink>
    </div>
  </section>

  <!-- CATEGORY GRID: told apart by icon, not by a colour per category -->
  <section aria-labelledby="onderwerpen-heading" class="mx-auto max-w-page px-6 py-14">
    <UiSectionHead id="onderwerpen-heading" title="Onderwerpen" />

    <ul class="m-0 grid list-none grid-cols-2 gap-4 p-0 sm:grid-cols-3 lg:grid-cols-6">
      <li v-for="cat in CATEGORIES" :key="cat.slug">
        <NuxtLink
          :to="`/catalogus?category=${cat.slug}`"
          class="brand-card brand-card-interactive flex h-full min-h-[150px] flex-col justify-between gap-6 p-4 no-underline"
        >
          <span class="text-brand-primary">
            <UiCategoryIcon :category="cat.slug" :size="26" :labelled="false" />
          </span>
          <span>
            <span class="block font-bold leading-[1.2] text-brand-primary">{{ cat.title }}</span>
            <span class="mt-1 block text-sm text-govnl-grijs-600">{{ categoryCounts[cat.slug] ?? 0 }} apps</span>
          </span>
        </NuxtLink>
      </li>
    </ul>
  </section>

  <!-- FEATURED APPS -->
  <section aria-labelledby="uitgelicht-heading" class="bg-brand-secondary py-12">
    <div class="mx-auto max-w-page px-6">
      <UiSectionHead id="uitgelicht-heading" title="Uitgelicht door provincies">
        <template #action>
          <NuxtLink to="/catalogus" class="brand-link inline-flex items-center gap-1.5 font-semibold">
            Bekijk alle apps
            <IconArrowRight size="16" stroke="2" aria-hidden="true" />
          </NuxtLink>
        </template>
      </UiSectionHead>

      <div v-if="featuredApps.length" class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <AppCard
          v-for="app in featuredApps"
          :key="app.slug"
          :app="app"
        />
      </div>
      <p v-else class="brand-card m-0 p-6 text-center text-govnl-grijs-600">
        Er staan nog geen applicaties in productie.
      </p>
    </div>
  </section>

  <!-- HOW IT WORKS -->
  <section aria-labelledby="hoe-het-werkt-heading" class="bg-brand-surface py-16">
    <div class="mx-auto max-w-page px-6">
      <UiSectionHead id="hoe-het-werkt-heading" title="Hoe het werkt" center />

      <ol class="m-0 flex list-none flex-wrap justify-center gap-x-14 gap-y-10 p-0">
        <UiPillar
          v-for="step in howSteps"
          :key="step.n"
          :step="step.n"
          :title="step.title"
          :body="step.body"
        />
      </ol>
    </div>
  </section>

  <!-- CTA: one primary-coloured promo, two white link panels -->
  <section aria-label="Aan de slag" class="mx-auto max-w-page px-6 py-14">
    <div class="grid grid-cols-1 items-stretch gap-6 lg:grid-cols-2">
      <UiPromoPanel
        title="Klaar om met een applicatie te starten?"
        body="Bekijk het volledige aanbod of dien uw eigen applicatie in voor opname in de catalogus."
      >
        <GovnlButton variant="ghost" :icon="IconSearch" to="/catalogus">Bekijk catalogus</GovnlButton>
        <GovnlButton variant="ghost" :icon="IconRocket" to="/dashboard/apps/new">App aanmelden</GovnlButton>
      </UiPromoPanel>

      <div class="grid grid-rows-2 gap-6">
        <UiLinkPanel
          title="Toelatingseisen"
          body="Bekijk het volledige beoordelingskader: DPIA, BIO, toegankelijkheid en de AI-Verordening."
          to="/toelating"
        />
        <UiLinkPanel
          title="Community"
          body="Stel vragen, deel ervaringen en leer van andere organisaties die deze applicaties gebruiken."
          to="/community"
        />
      </div>
    </div>
  </section>
</template>
