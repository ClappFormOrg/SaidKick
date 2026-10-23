<script setup lang="ts">
import {
  IconPlus, IconTrash, IconCheck, IconCopy,
} from '~/lib/icons'
import type { ClusterAPI } from '~/composables/useData'
import { errorMessage } from '~/composables/useApi'
import { COPY_FEEDBACK_MS, formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Clusters' })

const { api } = useApi()
const { isClusterMgr, isPlatformAdmin } = useAuth()

const { data: clusterData, refresh } = await useAsyncData<{ items: ClusterAPI[] }>(
  'clusters',
  () => api<{ items: ClusterAPI[] }>('/api/v1/clusters'),
  { server: false, default: (): { items: ClusterAPI[] } => ({ items: [] }) },
)

const showForm    = ref(false)
const form        = reactive({ name: '', description: '', scope: '', ingress_class_name: '', base_domain: '' })

interface InstallerResult {
  cluster: ClusterAPI
  installer: { helm_command: string; join_token_jwt: string }
}
const installer   = ref<InstallerResult | null>(null)
const copied      = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | undefined

const { run: handleCreate, pending: isCreating, error: createErrorMsg } = useAsyncAction(async () => {
  const result = await api<InstallerResult>('/api/v1/clusters', {
    method:  'POST',
    body: {
      name:               form.name.trim(),
      description:        form.description.trim() || undefined,
      scope:              form.scope.trim() || undefined,
      ingress_class_name: form.ingress_class_name.trim() || undefined,
      base_domain:        form.base_domain.trim() || undefined,
    },
  })
  installer.value = result
  showForm.value  = false
  form.name = form.description = form.scope = form.ingress_class_name = form.base_domain = ''
  await refresh()
}, 'Aanmaken mislukt.')

async function copyCommand() {
  if (!installer.value) return
  await navigator.clipboard.writeText(installer.value.installer.helm_command)
  copied.value = true
  clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { copied.value = false }, COPY_FEEDBACK_MS)
}
onUnmounted(() => clearTimeout(copyTimer))

const deletingId  = ref<string | null>(null)
const deleteError = ref('')

async function handleDelete(cl: ClusterAPI) {
  if (!window.confirm(`Cluster "${cl.name}" verwijderen? Alle actieve deployments moeten eerst worden verwijderd.`)) return
  deletingId.value  = cl.id
  deleteError.value = ''
  try {
    await api(`/api/v1/clusters/${cl.id}`, { method: 'DELETE' })
    await refresh()
    if (installer.value?.cluster.id === cl.id) installer.value = null
  } catch (err: unknown) {
    deleteError.value = errorMessage(err, 'Verwijderen mislukt.')
  } finally {
    deletingId.value = null
  }
}

function stateColor(state: string) {
  switch (state) {
    case 'ready':    return { bg: 'bg-govnl-groen-50',  fg: 'text-govnl-groen-700' }
    case 'pending_registration':
                     return { bg: 'bg-govnl-geel-50',   fg: 'text-govnl-bruin-700' }
    case 'decommissioned':
                     return { bg: 'bg-govnl-grijs-100', fg: 'text-govnl-grijs-600' }
    default:         return { bg: 'bg-govnl-grijs-100', fg: 'text-govnl-grijs-600' }
  }
}

function stateLabel(state: string) {
  switch (state) {
    case 'ready':                return 'Actief'
    case 'pending_registration': return 'In afwachting'
    case 'decommissioned':       return 'Buiten gebruik'
    default:                     return state
  }
}
</script>

<template>
  <div>
    <UiBreadcrumb
      class="mb-6"
      :items="[{ label: 'Dashboard', to: '/dashboard' }, { label: 'Clusters' }]"
    />

    <!-- Heading markup inline rather than UiSectionHead, and plain buttons with
         the brand-btn classes: the role-guard tests mount this page without
         the auto-imported components and click only real <button> elements. -->
    <div class="mb-6 flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
      <div>
        <h1 class="brand-h1">Clusters</h1>
        <p class="brand-prose mt-2">Kubernetes-clusters beheren</p>
      </div>
      <button
        v-if="isClusterMgr"
        type="button"
        class="brand-btn brand-btn-primary"
        :aria-expanded="showForm"
        aria-controls="cluster-create-form"
        @click="showForm = !showForm"
      >
        <IconPlus size="18" stroke="2" aria-hidden="true" />
        Cluster registreren
      </button>
    </div>

    <div
      v-if="installer"
      role="status"
      class="mb-6 rounded-card border border-govnl-groen-500 bg-govnl-groen-50 px-6 py-5"
    >
      <p class="m-0 mb-2 font-semibold text-govnl-groen-700">
        Cluster "{{ installer.cluster.name }}" aangemaakt
      </p>
      <p class="m-0 mb-3.5 text-sm text-govnl-groen-700">
        Voer het volgende Helm-commando uit op het doelcluster. Het join-token vervalt na <strong>1 uur</strong>.
      </p>
      <div class="relative whitespace-pre-wrap break-all rounded-card border border-brand-border bg-brand-white px-4 py-3.5 pr-28 font-mono text-[13px] text-brand-body">{{ installer.installer.helm_command }}<button
        type="button"
        class="brand-btn brand-btn-secondary brand-btn-sm absolute right-2.5 top-2.5"
        @click="copyCommand"
      >
        <IconCheck v-if="copied" size="14" stroke="2" aria-hidden="true" />
        <IconCopy v-else size="14" stroke="2" aria-hidden="true" />
        {{ copied ? 'Gekopieerd' : 'Kopiëren' }}
      </button></div>
      <button
        type="button"
        class="brand-btn brand-btn-link mt-3 text-sm"
        @click="installer = null"
      >Sluiten ×</button>
    </div>

    <section
      v-if="showForm && isClusterMgr"
      id="cluster-create-form"
      class="brand-card mb-6 p-6"
      aria-labelledby="cluster-create-title"
    >
      <h2 id="cluster-create-title" class="brand-h3 mb-5">Nieuw cluster registreren</h2>

      <div class="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <label for="cluster-name" class="brand-label">
            Naam <span class="text-govnl-rood-700" aria-hidden="true">*</span>
          </label>
          <input
            id="cluster-name"
            v-model="form.name"
            type="text"
            required
            placeholder="bijv. prod-cluster-01"
            class="brand-input"
            @keydown.enter="handleCreate"
          />
        </div>
        <div>
          <label for="cluster-description" class="brand-label">Beschrijving</label>
          <input
            id="cluster-description"
            v-model="form.description"
            type="text"
            placeholder="Optioneel"
            class="brand-input"
          />
        </div>
      </div>

      <div v-if="isPlatformAdmin" class="mb-4 max-w-[320px]">
        <label for="cluster-scope" class="brand-label">Scope</label>
        <select
          id="cluster-scope"
          v-model="form.scope"
          class="brand-input"
        >
          <option value="">Organisatie (standaard)</option>
          <option value="public">Publiek (zichtbaar voor alle organisaties)</option>
          <option value="restricted">Beperkt (eigenaar + expliciete toegangslijst)</option>
        </select>
      </div>

      <!-- Ingress defaults -->
      <div class="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <label for="cluster-ingress-class" class="brand-label">
            Ingress class <span class="font-normal text-govnl-grijs-600">(optioneel)</span>
          </label>
          <input
            id="cluster-ingress-class"
            v-model="form.ingress_class_name"
            type="text"
            placeholder="bijv. nginx, traefik"
            class="brand-input"
            aria-describedby="cluster-ingress-class-help"
          />
          <p id="cluster-ingress-class-help" class="brand-help">Wordt vooringevuld in het deploy-formulier.</p>
        </div>
        <div>
          <label for="cluster-base-domain" class="brand-label">
            Basis-domein <span class="font-normal text-govnl-grijs-600">(optioneel)</span>
          </label>
          <input
            id="cluster-base-domain"
            v-model="form.base_domain"
            type="text"
            placeholder="bijv. apps.gemeente.nl"
            class="brand-input"
            aria-describedby="cluster-base-domain-help"
          />
          <p id="cluster-base-domain-help" class="brand-help">Deployments krijgen <em>&lt;app&gt;.apps.gemeente.nl</em> als suggestie.</p>
        </div>
      </div>

      <p
        v-if="createErrorMsg"
        role="alert"
        class="mb-4 mt-0 rounded-card border border-govnl-rood-500 bg-govnl-rood-50 px-4 py-3 text-sm text-govnl-rood-700"
      >{{ createErrorMsg }}</p>

      <div class="flex flex-wrap items-center gap-3">
        <button
          type="button"
          :disabled="isCreating || !form.name.trim()"
          class="brand-btn brand-btn-navy"
          @click="handleCreate"
        >{{ isCreating ? 'Aanmaken…' : 'Registreren' }}</button>
        <button
          type="button"
          class="brand-btn brand-btn-secondary"
          @click="showForm = false; createErrorMsg = ''"
        >Annuleren</button>
      </div>
    </section>

    <p
      v-if="deleteError"
      role="alert"
      class="mb-4 mt-0 rounded-card border border-govnl-rood-500 bg-govnl-rood-50 px-4 py-3 text-sm text-govnl-rood-700"
    >{{ deleteError }}</p>

    <section class="brand-card overflow-hidden" aria-labelledby="clusters-list-title">
      <div class="flex items-center justify-between gap-4 border-b border-brand-border px-5 py-4">
        <h2 id="clusters-list-title" class="brand-h3">Geregistreerde clusters</h2>
        <span class="text-sm text-govnl-grijs-600">{{ clusterData?.items?.length ?? 0 }} clusters</span>
      </div>

      <p v-if="!clusterData?.items?.length" class="m-0 p-12 text-center text-sm text-govnl-grijs-600">
        Geen clusters gevonden.<template v-if="isClusterMgr"> Klik op "Cluster registreren" om te beginnen.</template>
      </p>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse">
          <thead>
            <tr class="border-b border-brand-border bg-brand-surface">
              <th class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Naam</th>
              <th class="hidden px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 sm:table-cell">Scope</th>
              <th class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Status</th>
              <th class="hidden px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 md:table-cell">Versie agent</th>
              <th class="hidden px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 lg:table-cell">Laatste heartbeat</th>
              <th class="px-4 py-3"><span class="sr-only">Acties</span></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="cl in clusterData.items"
              :key="cl.id"
              class="border-b border-brand-border last:border-0"
            >
              <td class="px-4 py-3.5">
                <NuxtLink
                  :to="`/dashboard/clusters/${cl.id}`"
                  class="brand-link font-semibold"
                >{{ cl.name }}</NuxtLink>
                <div v-if="cl.description" class="mt-0.5 text-sm text-govnl-grijs-600">{{ cl.description }}</div>
              </td>
              <td class="hidden px-4 py-3.5 text-sm text-govnl-grijs-700 sm:table-cell">
                <code class="rounded-card bg-brand-surface px-1.5 py-0.5 text-xs">{{ cl.scope }}</code>
              </td>
              <td class="px-4 py-3.5">
                <span
                  class="inline-block whitespace-nowrap rounded-card px-2.5 py-0.5 text-xs font-semibold"
                  :class="[stateColor(cl.state).bg, stateColor(cl.state).fg]"
                >{{ stateLabel(cl.state) }}</span>
              </td>
              <td class="hidden px-4 py-3.5 text-sm text-govnl-grijs-700 md:table-cell">
                {{ cl.agent_version ? `v${cl.agent_version}` : '—' }}
              </td>
              <td class="hidden px-4 py-3.5 text-sm text-govnl-grijs-600 lg:table-cell">
                {{ formatDate(cl.last_heartbeat_at, { withTime: true }) }}
              </td>
              <td class="px-4 py-3.5 text-right">
                <button
                  v-if="isClusterMgr"
                  type="button"
                  :disabled="deletingId === cl.id"
                  class="brand-btn brand-btn-danger brand-btn-sm"
                  @click="handleDelete(cl)"
                >
                  <IconTrash size="14" stroke="2" aria-hidden="true" />
                  {{ deletingId === cl.id ? '…' : 'Verwijderen' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
