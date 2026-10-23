<script setup lang="ts">
import { IconRocket, IconTrash, IconRefresh, IconHistory, IconSettings } from '~/lib/icons'
import { errorMessage } from '~/composables/useApi'
import { deploymentLoadError } from '~/utils/loadErrors'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Deployment' })

const route = useRoute()
const id    = route.params.id as string
const { api, baseURL } = useApi()
const { isDeployer } = useAuth()

interface DeploymentDetail {
  id: string
  app_id: string
  app_version_id: string
  app_slug?: string
  cluster_id: string
  namespace: string
  state: string
  last_state_change_at: string
  created_at: string
  values?: Record<string, unknown>
  values_schema?: Record<string, unknown>
  last_argocd_app_name?: string
  live_status?: { phase: string; message: string; healthy: boolean; last_synced_at: string; revision: string }
}

const { data: dep, error, refresh } = await useAsyncData<DeploymentDetail>(
  `deployment:${id}`,
  () => api<DeploymentDetail>(`/api/v1/deployments/${id}`),
  { server: false },
)

// With server: false the fetch can still be pending here on a hard reload,
// so react to the error whenever it arrives instead of checking it once.
watch(error, (err) => {
  if (err) showError(createError(deploymentLoadError(err.statusCode)))
}, { immediate: true })

interface StreamEvent { type: string; data: Record<string, unknown> }
const events      = ref<StreamEvent[]>([])
const sseError    = ref('')
const sseActive   = ref(false)
let   es: EventSource | null = null

const TERMINAL_STATES = new Set([
  'ready', 'deleted', 'deleting',
  'failed_orchestrator', 'failed_render', 'failed_vault', 'failed_secret_sync',
])

function isTerminal(state?: string) {
  return state ? TERMINAL_STATES.has(state) : false
}

function startStream() {
  if (es) es.close()
  sseError.value = ''
  sseActive.value = true

  const url = `${baseURL}/api/v1/deployments/${id}/events`
  es = new EventSource(url, { withCredentials: true })

  const handleEvent = (type: string) => (e: MessageEvent) => {
    try {
      events.value.push({ type, data: JSON.parse(e.data) })
    } catch {
      sseError.value = 'Een statusupdate kon niet worden gelezen. Ververs de pagina voor de huidige status.'
    }
  }

  es.addEventListener('state', handleEvent('state'))
  es.addEventListener('warn',  handleEvent('warn'))
  es.addEventListener('end',   (e: MessageEvent) => {
    handleEvent('end')(e)
    es?.close()
    sseActive.value = false
    refresh()
  })

  es.onerror = () => {
    sseError.value  = 'SSE-verbinding verbroken. Ververs de pagina voor de huidige status.'
    sseActive.value = false
    es?.close()
  }
}

function startStreamIfActive(d: DeploymentDetail) {
  if (!isTerminal(d.state)) startStream()
}

onMounted(() => {
  if (dep.value) {
    startStreamIfActive(dep.value)
    return
  }
  // Hard reload: the client-side fetch resolves after mount.
  const stop = watch(dep, (d) => {
    if (!d) return
    stop()
    startStreamIfActive(d)
  })
})

onUnmounted(() => {
  es?.close()
})

// Stays hand-rolled: on success the page navigates away, so the button keeps
// its busy state instead of flashing back to enabled.
const isDeleting  = ref(false)
const deleteError = ref('')

async function handleDelete() {
  if (!window.confirm('Deployment verwijderen? Dit is niet ongedaan te maken.')) return
  isDeleting.value  = true
  deleteError.value = ''
  try {
    await api(`/api/v1/deployments/${id}`, { method: 'DELETE' })
    await navigateTo('/dashboard')
  } catch (err: unknown) {
    deleteError.value = errorMessage(err, 'Verwijderen mislukt.')
    isDeleting.value  = false
  }
}

const { run: reapply, pending: isReapplying, error: reapplyError } = useAsyncAction(async (toVersionId: string) => {
  await api(`/api/v1/deployments/${id}/upgrade`, {
    method:  'POST',
    body: { to_version_id: toVersionId },
  })
  await refresh()
  startStream()
}, 'Herstarten mislukt.')

async function handleReapply() {
  if (!dep.value) return
  events.value = []
  await reapply(dep.value.app_version_id)
}

// Mirrors canChangeVersion in the API: upgrade (reapply, reconfigure) and
// rollback are only accepted from settled states.
const canChangeVersion = computed(() => {
  const s = dep.value?.state
  return s === 'ready' || s === 'degraded' || (s?.startsWith('failed_') ?? false)
})

// Upgrade, rollback and delete all require the deployer role; the version
// actions additionally need a settled state.
const canDeployVersion = computed(() => isDeployer.value && canChangeVersion.value)

const showReconfigureForm      = ref(false)
const reconfigureValues        = ref<Record<string, unknown>>({})
const reconfigureSecretInputs  = ref<Record<string, string>>({})

const { run: reconfigure, pending: isReconfiguring, error: reconfigureError } = useAsyncAction(async (toVersionId: string) => {
  const filteredSecrets: Record<string, string> = {}
  for (const [k, v] of Object.entries(reconfigureSecretInputs.value)) {
    if (v.trim() !== '') filteredSecrets[k] = v
  }
  await api(`/api/v1/deployments/${id}/upgrade`, {
    method:  'POST',
    body: {
      to_version_id:     toVersionId,
      new_values:        reconfigureValues.value,
      new_secret_inputs: filteredSecrets,
    },
  })
  showReconfigureForm.value = false
  await refresh()
  startStream()
}, 'Herconfiguratie mislukt.')

function openReconfigure() {
  reconfigureValues.value       = { ...(dep.value?.values ?? {}) }
  reconfigureSecretInputs.value = {}
  reconfigureError.value        = ''
  showReconfigureForm.value     = true
}

async function handleReconfigure() {
  if (!dep.value) return
  events.value = []
  await reconfigure(dep.value.app_version_id)
}

const showRollbackForm   = ref(false)
const rollbackVersionId  = ref('')

const { run: rollback, pending: isRollingBack, error: rollbackError } = useAsyncAction(async (toRevision: string) => {
  await api(`/api/v1/deployments/${id}/rollback`, {
    method:  'POST',
    body:    { to_revision: toRevision },
  })
  showRollbackForm.value  = false
  rollbackVersionId.value = ''
  await refresh()
  startStream()
}, 'Terugdraaien mislukt.')

async function handleRollback() {
  const toRevision = rollbackVersionId.value.trim()
  if (!toRevision) return
  events.value = []
  await rollback(toRevision)
}

// Read ingress.host from the deployment values blob — set at deploy time
// from the cluster's base_domain default and confirmed by the deployer.
const ingressHost = computed(() => {
  const v = dep.value?.values as Record<string, unknown> | undefined
  const ingress = v?.ingress as Record<string, unknown> | undefined
  return ingress?.host as string | undefined
})

function stateColor(state?: string) {
  switch (state) {
    case 'ready':         return { bg: 'bg-govnl-groen-50',   fg: 'text-govnl-groen-700' }
    case 'applying':
    case 'reconciling':   return { bg: 'bg-govnl-geel-50',    fg: 'text-govnl-bruin-700' }
    case 'degraded':      return { bg: 'bg-govnl-oranje-50',  fg: 'text-govnl-oranje-700' }
    case 'pending':
    case 'pending_orchestrator':
                          return { bg: 'bg-govnl-grijs-100',  fg: 'text-govnl-grijs-600' }
    case 'deleting':
    case 'deleted':       return { bg: 'bg-govnl-grijs-200',  fg: 'text-govnl-grijs-700' }
    default:              return { bg: 'bg-govnl-rood-50',    fg: 'text-govnl-rood-700' }
  }
}

function stateLabel(state?: string) {
  const map: Record<string, string> = {
    pending:               'In afwachting',
    applying:              'Aan het uitrollen',
    reconciling:           'Aan het reconciliëren',
    ready:                 'Actief',
    degraded:              'Gedegradeerd',
    deleting:              'Aan het verwijderen',
    deleted:               'Verwijderd',
    pending_orchestrator:  'Wacht op orchestrator',
    failed_orchestrator:   'Uitrol mislukt',
    failed_render:         'Render mislukt',
    failed_vault:          'Vault-fout',
    failed_secret_sync:    'Secret-fout',
  }
  return map[state ?? ''] ?? state ?? '—'
}

function eventColor(type: string) {
  switch (type) {
    case 'state': return 'text-govnl-hemelblauw-600'
    // oranje-600 is 3.9:1 on white; bruin-700 keeps the warning tone at AA.
    case 'warn':  return 'text-govnl-bruin-700'
    case 'end':   return 'text-govnl-grijs-600'
    default:      return 'text-govnl-grijs-600'
  }
}

function formatDate(iso?: string) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('nl-NL', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
</script>

<template>
  <div>
    <UiBreadcrumb
      class="mb-6"
      :items="[
        { label: 'Dashboard', to: '/dashboard' },
        { label: 'Deployment' },
        { label: `${id.slice(0, 8)}…` },
      ]"
    />

    <div v-if="dep">

      <div class="mb-8 flex flex-wrap items-start justify-between gap-4">
        <div class="flex items-center gap-3">
          <IconRocket size="28" stroke="2" class="shrink-0 text-brand-primary" aria-hidden="true" />
          <div>
            <h1 class="brand-h1 flex flex-wrap items-center gap-3">
              Deployment
              <span
                class="inline-block rounded-card px-2.5 py-0.5 text-sm font-semibold"
                :class="[stateColor(dep.state).bg, stateColor(dep.state).fg]"
              >{{ stateLabel(dep.state) }}</span>
            </h1>
            <p class="m-0 mt-1 break-all font-mono text-sm text-govnl-grijs-600">{{ dep.id }}</p>
          </div>
        </div>
        <!-- Plain buttons with the brand-btn classes on this page: the role-guard
             tests stub GovnlButton and click only real <button> elements. -->
        <div class="flex flex-wrap gap-2.5">
          <button
            v-if="canDeployVersion"
            type="button"
            :disabled="isReapplying"
            class="brand-btn brand-btn-secondary brand-btn-sm"
            @click="handleReapply"
          >
            <IconRefresh size="16" stroke="2" aria-hidden="true" />
            {{ isReapplying ? 'Bezig…' : 'Herstarten' }}
          </button>
          <button
            v-if="canDeployVersion && dep.values_schema"
            type="button"
            :disabled="isReconfiguring"
            class="brand-btn brand-btn-secondary brand-btn-sm"
            :aria-expanded="showReconfigureForm"
            @click="openReconfigure"
          >
            <IconSettings size="16" stroke="2" aria-hidden="true" />
            Configureren
          </button>
          <button
            v-if="canDeployVersion"
            type="button"
            :disabled="isRollingBack"
            class="brand-btn brand-btn-secondary brand-btn-sm"
            :aria-expanded="showRollbackForm"
            @click="showRollbackForm = !showRollbackForm"
          >
            <IconHistory size="16" stroke="2" aria-hidden="true" />
            Terugdraaien
          </button>
          <button
            v-if="isDeployer"
            type="button"
            :disabled="isDeleting"
            class="brand-btn brand-btn-danger brand-btn-sm"
            @click="handleDelete"
          >
            <IconTrash size="16" stroke="2" aria-hidden="true" />
            {{ isDeleting ? 'Bezig…' : 'Verwijderen' }}
          </button>
        </div>
      </div>

      <section
        v-if="showReconfigureForm && isDeployer"
        class="brand-card mb-6 px-6 py-5"
        aria-labelledby="deploy-reconfigure-title"
      >
        <h2 id="deploy-reconfigure-title" class="brand-h3 mb-1">Configuratie bijwerken</h2>
        <p class="brand-help mb-4 mt-0">
          Wijzig waarden of secrets voor deze installatie. De huidige Helm-versie blijft ongewijzigd.
        </p>

        <DeployValuesForm
          :schema="dep.values_schema"
          :model-values="reconfigureValues"
          :model-secret-inputs="reconfigureSecretInputs"
          :reconfigure-mode="true"
          @update:model-values="reconfigureValues = $event"
          @update:model-secret-inputs="reconfigureSecretInputs = $event"
        />

        <p
          v-if="reconfigureError"
          class="mb-0 mt-4 text-sm text-govnl-rood-700"
          role="alert"
        >{{ reconfigureError }}</p>

        <div class="mt-5 flex flex-wrap gap-2.5">
          <button
            type="button"
            :disabled="isReconfiguring"
            class="brand-btn brand-btn-navy brand-btn-sm"
            @click="handleReconfigure"
          >{{ isReconfiguring ? 'Bezig…' : 'Opslaan & heruitrollen' }}</button>
          <button
            type="button"
            class="brand-btn brand-btn-link brand-btn-sm"
            @click="showReconfigureForm = false; reconfigureError = ''"
          >Annuleren</button>
        </div>
      </section>

      <p
        v-if="deleteError || reapplyError"
        role="alert"
        class="mb-6 mt-0 rounded-card border border-govnl-rood-500 bg-govnl-rood-50 px-4 py-3 text-sm text-govnl-rood-700"
      >{{ deleteError || reapplyError }}</p>

      <section
        v-if="showRollbackForm && isDeployer"
        class="brand-card mb-6 px-6 py-5"
        aria-labelledby="deploy-rollback-title"
      >
        <h2 id="deploy-rollback-title" class="brand-h3 mb-1">Terugdraaien naar eerdere versie</h2>
        <p id="rollback-version-label" class="brand-help mb-3 mt-0">
          Voer het AppVersion-ID in van de versie waarnaar u wilt terugdraaien.
        </p>
        <div class="flex flex-wrap items-center gap-2.5">
          <input
            v-model="rollbackVersionId"
            aria-labelledby="rollback-version-label"
            type="text"
            placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
            class="brand-input min-w-0 flex-1 font-mono"
            @keydown.enter="handleRollback"
          />
          <button
            type="button"
            :disabled="isRollingBack || !rollbackVersionId.trim()"
            class="brand-btn brand-btn-navy"
            @click="handleRollback"
          >{{ isRollingBack ? 'Bezig…' : 'Bevestigen' }}</button>
          <button
            type="button"
            class="brand-btn brand-btn-link"
            @click="showRollbackForm = false; rollbackError = ''"
          >Annuleren</button>
        </div>
        <p
          v-if="rollbackError"
          class="mb-0 mt-2 text-sm text-govnl-rood-700"
          role="alert"
        >{{ rollbackError }}</p>
      </section>

      <section class="brand-card mb-6 px-6 py-5" aria-label="Gegevens">
        <dl class="m-0 grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Namespace</dt>
            <dd class="m-0 font-mono text-sm text-brand-body">{{ dep.namespace }}</dd>
          </div>
          <div>
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Cluster ID</dt>
            <dd class="m-0 break-all font-mono text-[13px] text-brand-body">{{ dep.cluster_id }}</dd>
          </div>
          <div>
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Aangemaakt</dt>
            <dd class="m-0 text-sm text-brand-body">{{ formatDate(dep.created_at) }}</dd>
          </div>
          <div>
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Laatste statuswijziging</dt>
            <dd class="m-0 text-sm text-brand-body">{{ formatDate(dep.last_state_change_at) }}</dd>
          </div>
          <div v-if="dep.last_argocd_app_name">
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Argo CD App</dt>
            <dd class="m-0 break-all font-mono text-[13px] text-brand-body">{{ dep.last_argocd_app_name }}</dd>
          </div>
          <div v-if="dep.live_status">
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Live status</dt>
            <dd class="m-0 text-sm text-brand-body">{{ dep.live_status.phase }} — {{ dep.live_status.message || '—' }}</dd>
          </div>
          <div v-if="ingressHost && (dep.state === 'ready' || dep.state === 'degraded')">
            <dt class="mb-0.5 text-sm text-govnl-grijs-600">Toegankelijk via</dt>
            <dd class="m-0">
              <a
                :href="`https://${ingressHost}`"
                target="_blank"
                rel="noopener"
                class="brand-link break-all font-mono text-sm"
              >https://{{ ingressHost }}</a>
            </dd>
          </div>
        </dl>
      </section>

      <section class="brand-card overflow-hidden" aria-labelledby="deploy-events-title">
        <div class="flex items-center justify-between gap-4 border-b border-brand-border px-5 py-4">
          <h2 id="deploy-events-title" class="brand-h3 flex items-center gap-2">
            Live gebeurtenissen
            <span
              v-if="sseActive"
              class="inline-block h-2 w-2 rounded-full bg-govnl-groen-500 motion-safe:animate-pulse"
              aria-hidden="true"
            />
            <span v-if="sseActive" class="sr-only">(verbonden)</span>
          </h2>
          <button
            v-if="!sseActive && !isTerminal(dep.state)"
            type="button"
            class="brand-btn brand-btn-link text-sm"
            @click="startStream"
          >Opnieuw verbinden</button>
        </div>

        <p
          v-if="sseError"
          role="status"
          class="m-0 border-b border-govnl-oranje-200 bg-govnl-oranje-50 px-5 py-3 text-sm text-govnl-bruin-700"
        >{{ sseError }}</p>

        <p
          v-if="!events.length"
          class="m-0 p-8 text-center text-sm text-govnl-grijs-600"
        >
          {{ sseActive ? 'Wacht op gebeurtenissen…' : (isTerminal(dep.state) ? 'Geen live stream — deployment is in eindfase.' : 'Geen gebeurtenissen ontvangen.') }}
        </p>

        <ol v-else class="m-0 flex max-h-[400px] list-none flex-col overflow-y-auto px-5 py-2" aria-live="polite">
          <li
            v-for="(ev, i) in events"
            :key="i"
            class="flex gap-3 border-b border-brand-border py-2.5 text-[13px] last:border-0"
          >
            <span
              class="min-w-[40px] shrink-0 pt-px text-[11px] font-bold uppercase tracking-[0.05em]"
              :class="eventColor(ev.type)"
            >{{ ev.type }}</span>
            <pre class="m-0 flex-1 whitespace-pre-wrap break-all font-mono text-[13px] text-brand-body">{{ JSON.stringify(ev.data, null, 2) }}</pre>
          </li>
        </ol>
      </section>

    </div>
  </div>
</template>
