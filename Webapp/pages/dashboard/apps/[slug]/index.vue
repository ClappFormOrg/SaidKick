<script setup lang="ts">
import { IconBrandGithub, IconExternalLink, IconRefresh } from '~/lib/icons'
import { useApp } from '~/composables/useData'
import { formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })

const route  = useRoute()
const slug   = route.params.slug as string
const { api } = useApi()
const { isDeveloper, isAuditor } = useAuth()

// App detail — uses existing useApp() composable (works for non-published apps when authenticated)
const { data: app, error, refresh: refreshApp } = await useApp(slug)
if (error.value || !app.value) throw createError({ statusCode: 404, statusMessage: 'Applicatie niet gevonden' })
useHead({ title: computed(() => app.value?.name ?? 'Applicatie') })

// Versions list — requires developer or auditor; skipped for other roles so
// the page does not issue a request that 403s.
interface AppVersionItem {
  id: string
  git_ref: string
  git_sha: string
  state: string
  created_at: string
}
interface VersionsResponse { items: AppVersionItem[]; total: number }

const { data: versions, refresh: refreshVersions } = await useAsyncData<VersionsResponse>(
  `versions:${slug}`,
  () => (isDeveloper.value || isAuditor.value)
    ? api<VersionsResponse>(`/api/v1/apps/${slug}/versions`)
    : Promise.resolve({ items: [], total: 0 }),
  { server: false, default: (): VersionsResponse => ({ items: [], total: 0 }) },
)

// Submit for review
const submitDone = ref(false)

const { run: submit, pending: isSubmitting, error: submitError } = useAsyncAction(async () => {
  await api(`/api/v1/apps/${slug}/submit`, { method: 'POST' })
  submitDone.value = true
  await Promise.all([refreshApp(), refreshVersions()])
}, 'Indienen mislukt. Probeer het opnieuw.')

async function submitForReview() {
  submitDone.value = false
  await submit()
}

function versionStateColor(state: string) {
  switch (state) {
    case 'published':     return { bg: 'var(--govnl-color-groen-50)',          fg: 'var(--govnl-color-groen-700)' }
    case 'submitted':     return { bg: 'var(--govnl-color-hemelblauw-50)',     fg: 'var(--govnl-color-hemelblauw-700)' }
    case 'needs_changes': return { bg: 'var(--govnl-color-oranje-50)',         fg: 'var(--govnl-color-oranje-700)' }
    case 'draft':         return { bg: 'var(--govnl-color-grijs-100)',         fg: 'var(--govnl-color-grijs-600)' }
    case 'rejected':      return { bg: 'var(--govnl-color-rood-50)',           fg: 'var(--govnl-color-rood-700)' }
    default:              return { bg: 'var(--govnl-color-grijs-100)',         fg: 'var(--govnl-color-grijs-600)' }
  }
}

function versionStateLabel(state: string) {
  const map: Record<string, string> = {
    draft:         'Concept',
    submitted:     'Ingediend',
    needs_changes: 'Wijzigingen vereist',
    published:     'Gepubliceerd',
    rejected:      'Afgewezen',
    archived:      'Gearchiveerd',
  }
  return map[state] ?? state
}

// Submitting requires the developer role as well as a submittable state.
const canSubmit = computed(() =>
  isDeveloper.value && (app.value?.status === 'Concept' || app.value?.status === 'Testfase'),
)
</script>

<template>
  <div class="flex flex-col gap-6">

    <UiBreadcrumb :items="[{ label: 'Dashboard', to: '/dashboard' }, { label: app!.name }]" />

    <!-- App header -->
    <div class="flex flex-wrap items-start justify-between gap-5">
      <div class="min-w-0 flex-1">
        <div class="mb-2 flex flex-wrap items-center gap-3">
          <h1 class="brand-h1">{{ app!.name }}</h1>
          <UiStatusPill :status="app!.status" />
          <span
            v-if="app!.versie"
            class="rounded-full border border-brand-border bg-brand-white px-2.5 py-0.5 font-mono text-xs text-govnl-grijs-700"
          >v{{ app!.versie }}</span>
        </div>

        <a
          :href="`https://github.com/${app!.repo}`"
          target="_blank"
          rel="noopener noreferrer"
          class="brand-link inline-flex items-center gap-1.5 text-sm"
        >
          <IconBrandGithub size="16" stroke="2" aria-hidden="true" />
          {{ app!.repo }}
          <IconExternalLink size="12" stroke="2" aria-hidden="true" />
          <span class="sr-only">(opent in nieuw tabblad)</span>
        </a>
      </div>

      <div class="flex flex-wrap gap-2">
        <!-- A plain button: the role-guard tests click only real <button> elements. -->
        <button
          v-if="canSubmit"
          type="button"
          :disabled="isSubmitting"
          class="brand-btn brand-btn-primary"
          @click="submitForReview"
        >
          <IconRefresh v-if="isSubmitting" size="18" stroke="2" aria-hidden="true" />
          {{ isSubmitting ? 'Bezig…' : 'Indienen voor beoordeling' }}
        </button>

        <GovnlButton
          v-if="app!.status === 'Productie'"
          variant="primary"
          :to="`/catalogus/${slug}`"
        >Bekijk in catalogus</GovnlButton>
      </div>
    </div>

    <!-- State banner -->
    <UiAlert v-if="app!.status === 'Concept'" tone="warning">
      Uw applicatie is nog niet ingediend. Gebruik de knop "Indienen voor beoordeling" als u klaar bent.
    </UiAlert>

    <UiAlert v-else-if="app!.status === 'Pilot'" tone="informative">
      Uw aanvraag is ingediend en wordt beoordeeld door een onafhankelijk auditor. U ontvangt bericht zodra de beoordeling is afgerond.
    </UiAlert>

    <UiAlert v-else-if="app!.status === 'Testfase'" tone="warning" title="Beoordeling: wijzigingen vereist.">
      Pas uw repository aan op basis van de auditfeedback, maak daarna een nieuwe versie aan en dien opnieuw in.
    </UiAlert>

    <UiAlert v-else-if="app!.status === 'Productie'" tone="positive">
      Uw applicatie is goedgekeurd en gepubliceerd in de catalogus.
    </UiAlert>

    <!-- Submit error -->
    <p
      v-if="submitError"
      role="alert"
      class="m-0 rounded-card border border-govnl-rood-500 bg-govnl-rood-50 px-5 py-3.5 text-sm text-govnl-rood-700"
    >{{ submitError }}</p>

    <!-- Manifest overview -->
    <section class="brand-card overflow-hidden" aria-labelledby="app-manifest-title">
      <h2 id="app-manifest-title" class="brand-h3 border-b border-brand-border px-6 py-3.5">
        Manifest &amp; metadata
      </h2>
      <dl class="m-0 grid grid-cols-1 sm:grid-cols-3">
        <div
          v-for="(item, i) in [
            { label: 'Eigenaar',       value: app!.eigenaar || '—' },
            { label: 'Licentie',       value: app!.licentie || '—' },
            { label: 'Risicocategorie', value: app!.riskCategory || '—' },
            { label: 'Categorie',      value: app!.category || '—' },
            { label: 'Installaties',   value: String(app!.installs) },
            { label: 'Bijgewerkt',     value: app!.bijgewerkt || '—' },
          ]"
          :key="item.label"
          class="border-brand-border px-6 py-3.5 max-sm:border-b max-sm:last:border-b-0"
          :class="{
            'sm:border-r': i % 3 !== 2,
            'sm:border-b': i < 3,
          }"
        >
          <dt class="mb-1 text-sm text-govnl-grijs-600">{{ item.label }}</dt>
          <dd class="m-0 font-semibold text-brand-body">{{ item.value }}</dd>
        </div>
      </dl>
    </section>

    <!-- Versions table -->
    <section class="brand-card overflow-hidden" aria-labelledby="app-versions-title">
      <h2 id="app-versions-title" class="brand-h3 border-b border-brand-border px-6 py-3.5">
        Versies
      </h2>

      <p
        v-if="!versions?.items?.length"
        class="m-0 px-6 py-8 text-center text-sm text-govnl-grijs-600"
      >Geen versies gevonden.</p>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse">
          <thead>
            <tr class="border-b border-brand-border bg-brand-surface">
              <th
                v-for="col in ['Ref', 'SHA', 'Status', 'Aangemaakt']"
                :key="col"
                class="px-6 py-2.5 text-left text-sm font-semibold text-govnl-grijs-700"
              >{{ col }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="ver in versions.items"
              :key="ver.id"
              class="border-b border-brand-border last:border-0"
            >
              <td class="px-6 py-3 text-sm font-semibold text-brand-body">
                {{ ver.git_ref || '—' }}
              </td>
              <td class="px-6 py-3 font-mono text-[13px] text-govnl-grijs-700">
                {{ ver.git_sha?.slice(0, 8) ?? '—' }}
              </td>
              <td class="px-6 py-3">
                <span
                  class="inline-block whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-semibold"
                  :style="{ backgroundColor: versionStateColor(ver.state).bg, color: versionStateColor(ver.state).fg }"
                >{{ versionStateLabel(ver.state) }}</span>
              </td>
              <td class="px-6 py-3 text-sm text-govnl-grijs-600">
                {{ formatDate(ver.created_at) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

  </div>
</template>
