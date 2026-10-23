<script setup lang="ts">
import {
  IconCircleCheck, IconX, IconAlertCircle,
  IconBrandGithub, IconDownload, IconExternalLink, IconRocket,
} from '~/lib/icons'
import { useApp, mapDetail, maintainerLink, CATEGORIES, CRITERIA, SCORE, type AppDetailAPI } from '~/composables/useData'
import type { MyDeploymentRef } from '~/types'

const route = useRoute()
const slug = route.params.slug as string

const { data: app, error } = await useApp(slug)
const { me, fetchMe, isDeployer } = useAuth()
const { api } = useApi()

// The server renders this page without the session, so the caller's own
// deployments of this app are fetched in the browser once the user is known.
const myDeployments = ref<MyDeploymentRef[]>([])
onMounted(async () => {
  if (!(await fetchMe())) return
  try {
    myDeployments.value = mapDetail(await api<AppDetailAPI>(`/api/v1/apps/${slug}`)).myDeployments ?? []
  } catch {
    // Without the list the page offers install or contact, as for anyone else.
  }
})

// The description lives outside the API (raw file from the app's repo), so
// this stays a plain $fetch without the session cookie.
const { data: description } = await useAsyncData(
  `app-desc:${slug}`,
  async (): Promise<{ text: string | null; failed: boolean }> => {
    const url = app.value?.descriptionUrl
    if (!url) return { text: null, failed: false }
    try { return { text: await $fetch<string>(url), failed: false } } catch { return { text: null, failed: true } }
  },
)
const descriptionText = computed(() => description.value?.text ?? null)

if (error.value || !app.value) {
  throw createError({ statusCode: 404, statusMessage: 'Applicatie niet gevonden' })
}
useHead({ title: computed(() => app.value?.name ?? 'Applicatie') })

const category = computed(() => CATEGORIES.find(c => c.slug === app.value?.category))

// The primary action, in order: open a running deployment in the viewer's
// organisation, install it as a deployer, or ask the vendor for access.
// When no contact resolves the page names the organisation administrator.
const openUrl = computed(() => myDeployments.value.find(d => d.url)?.url ?? '')
const accessLink = computed(() =>
  maintainerLink({ vendor_contact: app.value?.vendorContact }, app.value?.name ?? ''),
)

const activeTab = ref<'overzicht' | 'toelatingseisen' | 'documentatie' | 'versies'>('overzicht')

const passCount = computed(() =>
  app.value ? Object.values(app.value.scores).filter(v => v === 'pass').length : 0
)

const scoreIcon = (val: string) => {
  switch (val) {
    case 'pass': return IconCircleCheck
    case 'partial': return IconAlertCircle
    case 'fail': return IconX
    default: return IconAlertCircle
  }
}

const scoreColor = (val: string) => SCORE[val]?.color ?? 'var(--govnl-color-grijs-500)'

const tabs = [
  { key: 'overzicht',       label: 'Overzicht' },
  { key: 'toelatingseisen', label: 'Toelatingseisen' },
  { key: 'documentatie',    label: 'Documentatie' },
  { key: 'versies',         label: 'Versies' },
]

const metaRows = computed(() => app.value ? [
  { label: 'Eigenaar',         value: app.value.eigenaar },
  { label: 'Medeontwikkelaars', value: app.value.medeontwikkelaars.join(', ') },
  { label: 'Versie',           value: app.value.versie },
  { label: 'Bijgewerkt',       value: app.value.bijgewerkt },
  { label: 'Licentie',         value: app.value.licentie },
  { label: 'Repository',       value: app.value.repo },
] : [])

const lightbox = ref<string | null>(null)
const lightboxDialog = ref<HTMLElement | null>(null)
const lightboxTrigger = ref<HTMLElement | null>(null)

function openLightbox(src: string, trigger: HTMLElement) {
  lightboxTrigger.value = trigger
  lightbox.value = src
  nextTick(() => lightboxDialog.value?.focus())
}

function closeLightbox() {
  lightbox.value = null
  nextTick(() => lightboxTrigger.value?.focus())
}

function onLightboxKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    closeLightbox()
    return
  }
  if (e.key === 'Tab') {
    const focusable = lightboxDialog.value?.querySelectorAll<HTMLElement>(
      'button, [href], input, [tabindex]:not([tabindex="-1"])',
    ) ?? []
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last?.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first?.focus()
    }
  }
}

function handleTabKeydown(e: KeyboardEvent, currentKey: string) {
  const keys = tabs.map(t => t.key)
  const idx = keys.indexOf(currentKey)
  if (e.key === 'ArrowRight') {
    e.preventDefault()
    const next = tabs[(idx + 1) % tabs.length]
    activeTab.value = next.key as typeof activeTab.value
    nextTick(() => document.getElementById(`tab-${next.key}`)?.focus())
  } else if (e.key === 'ArrowLeft') {
    e.preventDefault()
    const prev = tabs[(idx - 1 + tabs.length) % tabs.length]
    activeTab.value = prev.key as typeof activeTab.value
    nextTick(() => document.getElementById(`tab-${prev.key}`)?.focus())
  } else if (e.key === 'Home') {
    e.preventDefault()
    activeTab.value = tabs[0].key as typeof activeTab.value
    nextTick(() => document.getElementById(`tab-${tabs[0].key}`)?.focus())
  } else if (e.key === 'End') {
    e.preventDefault()
    const last = tabs[tabs.length - 1]
    activeTab.value = last.key as typeof activeTab.value
    nextTick(() => document.getElementById(`tab-${last.key}`)?.focus())
  }
}
</script>


<template>
  <div v-if="app">
    <!-- Page header -->
    <div class="border-b border-brand-border bg-brand-white">
      <div class="mx-auto max-w-page px-6 pt-6">
        <UiBreadcrumb
          class="mb-5"
          :items="[
            { label: 'App-store', to: '/' },
            { label: 'Catalogus', to: '/catalogus' },
            ...(category ? [{ label: category.title, to: `/catalogus?category=${category.slug}` }] : []),
            { label: app.name },
          ]"
        />

        <div class="flex flex-wrap items-center gap-2.5">
          <span v-if="category" class="flex text-brand-primary">
            <UiCategoryIcon :category="category.slug" :size="20" :labelled="false" />
          </span>
          <UiTypeBadge v-if="category">{{ category.title }}</UiTypeBadge>
          <UiStatusPill :status="app.status" />
          <span class="text-sm text-govnl-grijs-600">
            In gebruik bij {{ app.installs }} organisatie{{ app.installs !== 1 ? 's' : '' }}
          </span>
        </div>

        <h1 class="brand-h1 mb-2.5 mt-3">{{ app.name }}</h1>

        <p v-if="app.tagline" class="brand-lead mb-6 max-w-[48rem]">{{ app.tagline }}</p>

        <!-- Tab bar -->
        <div
          role="tablist"
          :aria-label="app.name + ' secties'"
          class="-mb-px flex flex-wrap"
        >
          <button
            v-for="tab in tabs"
            :id="`tab-${tab.key}`"
            :key="tab.key"
            role="tab"
            type="button"
            :aria-selected="activeTab === tab.key"
            :aria-controls="`panel-${tab.key}`"
            :tabindex="activeTab === tab.key ? 0 : -1"
            class="cursor-pointer border-0 border-b-[3px] bg-transparent px-5 py-3 text-body transition-[color,border-color] duration-150 ease-out"
            :class="activeTab === tab.key
              ? 'border-b-brand-cta font-bold text-brand-primary'
              : 'border-b-transparent font-normal text-govnl-grijs-700 hover:text-brand-primary'"
            @click="activeTab = tab.key as typeof activeTab"
            @keydown="handleTabKeydown($event, tab.key)"
          >{{ tab.label }}</button>
        </div>
      </div>
    </div>

    <!-- Tab content -->
    <div class="mx-auto max-w-page px-6 py-8">
      <div class="grid grid-cols-1 items-start gap-10 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div class="min-w-0">
          <!-- OVERZICHT -->
          <div
            v-if="activeTab === 'overzicht'"
            id="panel-overzicht"
            role="tabpanel"
            aria-labelledby="tab-overzicht"
            tabindex="-1"
          >
            <article>
              <p v-if="description?.failed" role="status" class="brand-help mb-4 mt-0">
                De volledige beschrijving kon niet worden geladen.
              </p>
              <template v-if="descriptionText">
                <p
                  v-for="(para, i) in descriptionText.split(/\n\n+/).map(p => p.trim()).filter(Boolean)"
                  :key="i"
                  class="brand-prose mb-4 leading-[1.7]"
                >{{ para }}</p>
              </template>
              <p v-else-if="app.longDescription?.length" class="brand-prose mb-4 leading-[1.7]">
                {{ app.longDescription.join(' ') }}
              </p>

              <template v-if="app.screenshots?.length">
                <h2 class="brand-h3 mb-3 mt-8">Schermafbeeldingen</h2>
                <ul class="-mx-1 m-0 flex list-none gap-3 overflow-x-auto px-1 pb-2">
                  <li v-for="(src, i) in app.screenshots" :key="i" class="shrink-0">
                    <button
                      type="button"
                      :aria-label="`Schermafbeelding ${i + 1} van ${app.name} vergroten`"
                      class="cursor-zoom-in rounded-card border-0 bg-transparent p-0"
                      @click="openLightbox(src, $event.currentTarget as HTMLElement)"
                    >
                      <img
                        :src="src"
                        :alt="`Schermafbeelding ${i + 1} van ${app.name}`"
                        class="h-[200px] w-auto rounded-card border border-brand-border object-cover"
                      />
                    </button>
                  </li>
                </ul>
              </template>

              <h2 class="brand-h3 mb-3 mt-8">Wat u krijgt</h2>
              <ul class="brand-prose m-0 flex list-disc flex-col gap-1.5 pl-5">
                <li>Volledig geconfigureerde Helm-chart voor Kubernetes</li>
                <li>Nederlandstalige documentatie en installatiehandleiding</li>
                <li>Beveiligingsupdates via de platformkanalen</li>
                <li>Community-ondersteuning via het platform-forum</li>
              </ul>

              <h2 class="brand-h3 mb-3 mt-8">Eigenschappen</h2>
              <table class="brand-table">
                <tbody>
                  <tr v-for="row in metaRows" :key="row.label">
                    <th scope="row" class="w-[13.75rem] !pr-4">{{ row.label }}</th>
                    <td class="break-all">{{ row.value }}</td>
                  </tr>
                  <tr>
                    <th scope="row" class="!pr-4">Onderwerpen</th>
                    <td>
                      <span class="flex flex-wrap gap-1.5">
                        <UiTag v-for="tag in app.tags" :key="tag">{{ tag }}</UiTag>
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </article>
          </div>

          <!-- TOELATINGSEISEN -->
          <div
            v-else-if="activeTab === 'toelatingseisen'"
            id="panel-toelatingseisen"
            role="tabpanel"
            aria-labelledby="tab-toelatingseisen"
            tabindex="-1"
          >
            <div
              class="mb-6 flex items-center gap-4 rounded-card border px-5 py-4"
              :style="{
                backgroundColor: passCount === CRITERIA.length ? 'var(--govnl-color-groen-50)' : 'var(--govnl-color-geel-100)',
                borderColor: passCount === CRITERIA.length ? 'var(--govnl-color-groen-300)' : 'var(--govnl-color-bruin-500)',
              }"
            >
              <span
                class="text-h2 font-bold"
                :style="{ color: passCount === CRITERIA.length ? 'var(--govnl-color-groen-700)' : 'var(--govnl-color-bruin-700)' }"
              >{{ passCount }}/{{ CRITERIA.length }}</span>
              <span>
                <span class="block font-semibold text-brand-body">criteria voldoet</span>
                <span class="block text-sm text-govnl-grijs-700">{{ app.riskCategory }}</span>
              </span>
            </div>

            <div class="overflow-x-auto">
              <table class="brand-table min-w-[40rem]">
                <thead>
                  <tr>
                    <th
                      v-for="col in ['Criterium', 'Status', 'Onderbouwing', 'Bewijsstuk']"
                      :key="col"
                      scope="col"
                      class="!pr-4 !font-bold !text-brand-primary"
                    >{{ col }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="c in CRITERIA" :key="c.key">
                    <td class="!pr-4 font-semibold">{{ c.label }}</td>
                    <td class="!pr-4">
                      <!-- The icon carries the score colour; the label stays body
                           colour because orange and green text miss 4.5:1. -->
                      <span class="inline-flex items-center gap-1.5 whitespace-nowrap font-semibold">
                        <component
                          :is="scoreIcon(app.scores[c.key] ?? 'pending')"
                          size="16"
                          stroke="2"
                          aria-hidden="true"
                          :style="{ color: scoreColor(app.scores[c.key] ?? 'pending') }"
                        />
                        {{ SCORE[app.scores[c.key] ?? 'pending']?.label ?? 'In beoordeling' }}
                      </span>
                    </td>
                    <td class="!pr-4 !text-govnl-grijs-700">{{ c.desc }}</td>
                    <td>
                      <a
                        v-if="app.evidence?.[c.key]"
                        :href="app.evidence[c.key]"
                        target="_blank"
                        rel="noopener noreferrer"
                        class="brand-link inline-flex items-center gap-1"
                      >
                        <IconExternalLink size="14" stroke="2" aria-hidden="true" />
                        Bekijken
                      </a>
                      <span v-else class="text-govnl-grijs-600">—</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <!-- DOCUMENTATIE -->
          <div
            v-else-if="activeTab === 'documentatie'"
            id="panel-documentatie"
            role="tabpanel"
            aria-labelledby="tab-documentatie"
            tabindex="-1"
          >
            <h2 class="brand-h3 mb-4 mt-0">Documentatie</h2>
            <ul v-if="app.docs?.length" class="m-0 flex list-none flex-col gap-2 p-0">
              <li v-for="doc in app.docs" :key="doc.url">
                <a
                  :href="doc.url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="brand-card brand-card-interactive flex items-center gap-2.5 px-[18px] py-3.5 font-semibold text-brand-primary no-underline"
                >
                  <IconExternalLink size="16" stroke="2" aria-hidden="true" />
                  {{ doc.label }}
                </a>
              </li>
            </ul>
            <p v-else class="brand-prose text-govnl-grijs-600">
              Geen documentatie beschikbaar voor deze applicatie.
            </p>
          </div>

          <!-- VERSIES -->
          <div
            v-else-if="activeTab === 'versies'"
            id="panel-versies"
            role="tabpanel"
            aria-labelledby="tab-versies"
            tabindex="-1"
          >
            <h2 class="brand-h3 mb-4 mt-0">Versiehistorie</h2>
            <table v-if="app.versie" class="brand-table">
              <tbody>
                <tr class="bg-brand-surface">
                  <td class="w-28 !px-3 font-mono font-semibold !text-brand-primary">v{{ app.versie }}</td>
                  <td class="w-48 !pr-3 !text-govnl-grijs-600">{{ app.bijgewerkt }}</td>
                  <td class="!pr-3">Huidige versie</td>
                  <td class="!pr-3 text-right">
                    <UiTag tone="brand">Huidig</UiTag>
                  </td>
                </tr>
              </tbody>
            </table>
            <p v-else class="brand-prose text-govnl-grijs-600">
              Geen versiegegevens beschikbaar voor deze applicatie.
            </p>
          </div>
        </div>

        <!-- Aan de slag -->
        <aside class="order-first flex flex-col gap-4 lg:sticky lg:top-6 lg:order-none">
          <div class="brand-card flex flex-col gap-2.5 p-5">
            <h2 class="brand-h3 mb-1 mt-0">Aan de slag</h2>
            <GovnlButton
              v-if="openUrl"
              variant="primary"
              :href="openUrl"
              target="_blank"
              rel="noopener"
              :icon-after="IconExternalLink"
              class="w-full justify-center"
            >Openen</GovnlButton>
            <GovnlButton
              v-else-if="isDeployer && app.id && app.currentVersionId"
              variant="primary"
              :to="`/dashboard/deployments/new?app_id=${app.id}&app_version_id=${app.currentVersionId}&app_name=${encodeURIComponent(app.name)}&app_slug=${app.slug}`"
              :icon="IconRocket"
              class="w-full justify-center"
            >Installeren</GovnlButton>
            <GovnlButton
              v-else-if="accessLink"
              variant="primary"
              :href="accessLink"
              class="w-full justify-center"
            >Toegang aanvragen</GovnlButton>
            <!-- No contact resolves, so say who grants access instead of
                 rendering a button that goes nowhere. -->
            <p v-else class="m-0 text-govnl-grijs-700">
              Toegang nodig? Vraag het uw organisatiebeheerder.
            </p>
            <GovnlButton
              variant="secondary"
              :href="`https://github.com/${app.repo}`"
              target="_blank"
              rel="noopener noreferrer"
              :icon="IconBrandGithub"
              class="w-full justify-center"
            >Broncode bekijken</GovnlButton>
            <GovnlButton
              v-if="app.evidence?.dpia"
              variant="secondary"
              :href="app.evidence.dpia"
              target="_blank"
              rel="noopener noreferrer"
              :icon="IconDownload"
              class="w-full justify-center"
            >DPIA downloaden</GovnlButton>
          </div>

          <div class="rounded-card bg-brand-secondary p-5">
            <h2 class="brand-h3 mb-3 mt-0">Beoordeling in één oogopslag</h2>
            <ul class="m-0 flex list-none flex-col gap-2.5 p-0">
              <li v-for="c in CRITERIA" :key="c.key" class="flex items-start gap-2.5">
                <component
                  :is="scoreIcon(app.scores[c.key] ?? 'pending')"
                  size="18"
                  stroke="2"
                  class="mt-0.5 shrink-0"
                  aria-hidden="true"
                  :style="{ color: scoreColor(app.scores[c.key] ?? 'pending') }"
                />
                <span>
                  <span class="block font-semibold text-brand-primary">{{ c.label }}</span>
                  <span class="block text-sm leading-[1.4] text-govnl-grijs-700">{{ c.desc }}</span>
                </span>
              </li>
            </ul>
          </div>
        </aside>
      </div>
    </div>

    <!-- Lightbox -->
    <Teleport to="body">
      <div
        v-if="lightbox"
        ref="lightboxDialog"
        role="dialog"
        aria-modal="true"
        aria-label="Schermafbeelding vergroten"
        tabindex="-1"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 outline-none"
        @click.self="closeLightbox"
        @keydown="onLightboxKey"
      >
        <button
          type="button"
          aria-label="Sluiten"
          class="absolute right-4 top-4 cursor-pointer border-0 bg-transparent p-2 text-white/80 hover:text-white"
          @click="closeLightbox"
        >
          <IconX size="28" stroke="2" aria-hidden="true" />
        </button>
        <img
          :src="lightbox"
          alt="Schermafbeelding vergroot"
          class="max-h-[90vh] max-w-full rounded-card object-contain shadow-elev-3"
        />
      </div>
    </Teleport>
  </div>
</template>
