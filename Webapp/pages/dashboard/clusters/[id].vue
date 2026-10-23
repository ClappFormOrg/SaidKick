<script setup lang="ts">
import {
  IconServer, IconShieldLock, IconLock, IconWorld,
  IconBuilding, IconCertificate, IconTrash, IconPlus, IconNetwork,
} from '~/lib/icons'
import type { ClusterAPI, ClusterAccessGrant } from '~/composables/useData'
import { errorMessage } from '~/composables/useApi'
import { formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })

const route = useRoute()
const id = route.params.id as string

const { api } = useApi()
const { me, hasRole, isClusterMgr, isPlatformAdmin } = useAuth()

const { data: cluster, error: clusterError } = await useAsyncData<ClusterAPI>(
  `cluster:${id}`,
  () => api<ClusterAPI>(`/api/v1/clusters/${id}`),
  { server: false },
)
useHead({ title: computed(() => cluster.value?.name ?? 'Cluster') })

const grants = ref<ClusterAccessGrant[]>([])

const { run: fetchGrants, pending: grantsLoading, error: grantsError } = useAsyncAction(async () => {
  const res = await api<{ items: ClusterAccessGrant[] }>(`/api/v1/clusters/${id}/access`)
  grants.value = res.items ?? []
}, 'Laden mislukt.')

async function loadGrants() {
  if (!isPlatformAdmin.value || cluster.value?.scope !== 'restricted') return
  await fetchGrants()
}

const grantOrgId = ref('')

const { run: grantAccess, pending: isGranting, error: grantError } = useAsyncAction(async (orgId: string) => {
  await api(`/api/v1/clusters/${id}/access`, {
    method:  'POST',
    body:    { organization_id: orgId },
  })
  grantOrgId.value = ''
  await loadGrants()
}, 'Toevoegen mislukt.')

async function handleGrant() {
  if (!grantOrgId.value.trim()) return
  await grantAccess(grantOrgId.value.trim())
}

const revokingOrgId = ref<string | null>(null)
const revokeError   = ref('')

async function handleRevoke(orgId: string) {
  if (!window.confirm('Toegang intrekken voor deze organisatie?')) return
  revokingOrgId.value = orgId
  revokeError.value   = ''
  try {
    await api(`/api/v1/clusters/${id}/access/${orgId}`, { method: 'DELETE' })
    await loadGrants()
  } catch (err: unknown) {
    revokeError.value = errorMessage(err, 'Intrekken mislukt.')
  } finally {
    revokingOrgId.value = null
  }
}

// ─── Agent credential rotation ────────────────────────────────────────────────
// Mirrors the API rule for POST /clusters/:id/rotate-credential: a platform
// admin, or a cluster admin of the organisation that owns the cluster.
const canRotateCredential = computed(() => {
  if (isPlatformAdmin.value) return true
  const owner = cluster.value?.owning_organization_id
  return hasRole('cluster_admin') && !!owner && owner === me.value?.org_id
})

const certNotAfter = computed(() =>
  cluster.value?.agent_cert_not_after ?? cluster.value?.agent_cert_expires_at ?? '',
)

const { run: requestRotation, pending: isRotating, error: rotateError } = useAsyncAction(async () => {
  const res = await api<{ rotation_requested_at: string }>(`/api/v1/clusters/${id}/rotate-credential`, {
    method: 'POST',
  })
  if (cluster.value) cluster.value.agent_cert_rotation_requested_at = res.rotation_requested_at
}, 'Vernieuwen mislukt.')

async function handleRotateCredential() {
  if (!window.confirm(
    'Inloggegevens van de agent vernieuwen? De agent vraagt bij de volgende heartbeat een nieuw certificaat aan. '
    + 'Het huidige certificaat werkt tot de agent het nieuwe gebruikt.',
  )) return
  await requestRotation()
}

onMounted(loadGrants)
onUnmounted(() => clearTimeout(ingressSavedTimer))

// ─── Ingress defaults ─────────────────────────────────────────────────────────
const ingressForm    = reactive({ ingress_class_name: '', base_domain: '' })
const ingressSaved   = ref(false)
let ingressSavedTimer: ReturnType<typeof setTimeout> | undefined

watch(() => cluster.value, (cl) => {
  if (cl) {
    ingressForm.ingress_class_name = cl.ingress_class_name ?? ''
    ingressForm.base_domain        = cl.base_domain ?? ''
  }
}, { immediate: true })

const { run: patchIngress, pending: isSavingIngress, error: ingressError } = useAsyncAction(async () => {
  const updated = await api<ClusterAPI>(`/api/v1/clusters/${id}`, {
    method:  'PATCH',
    body: {
      ingress_class_name: ingressForm.ingress_class_name.trim() || null,
      base_domain:        ingressForm.base_domain.trim() || null,
    },
  })
  if (cluster.value) {
    cluster.value.ingress_class_name = updated.ingress_class_name
    cluster.value.base_domain        = updated.base_domain
  }
  ingressSaved.value = true
  clearTimeout(ingressSavedTimer)
  ingressSavedTimer = setTimeout(() => { ingressSaved.value = false }, 2500)
}, 'Opslaan mislukt.')

async function saveIngress() {
  ingressSaved.value = false
  await patchIngress()
}

function stateColor(state: string) {
  switch (state) {
    case 'ready':               return { bg: 'bg-govnl-groen-50',  fg: 'text-govnl-groen-700' }
    case 'pending_registration': return { bg: 'bg-govnl-geel-50',  fg: 'text-govnl-bruin-700' }
    case 'decommissioned':      return { bg: 'bg-govnl-grijs-100', fg: 'text-govnl-grijs-600' }
    default:                    return { bg: 'bg-govnl-grijs-100', fg: 'text-govnl-grijs-600' }
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

function scopeLabel(scope: string) {
  switch (scope) {
    case 'public':     return 'Publiek'
    case 'org':        return 'Organisatie'
    case 'restricted': return 'Beperkt'
    default:           return scope
  }
}
</script>

<template>
  <div>
    <UiBreadcrumb
      class="mb-6"
      :items="[
        { label: 'Dashboard', to: '/dashboard' },
        { label: 'Clusters', to: '/dashboard/clusters' },
        { label: cluster?.name ?? id },
      ]"
    />

    <UiAlert v-if="clusterError" tone="negative" title="Cluster niet gevonden of niet toegankelijk." />

    <template v-else-if="cluster">

      <!-- Header -->
      <div class="mb-8 flex flex-wrap items-start justify-between gap-4">
        <div class="flex items-center gap-3">
          <IconServer size="28" stroke="2" class="shrink-0 text-brand-primary" aria-hidden="true" />
          <div>
            <h1 class="brand-h1">{{ cluster.name }}</h1>
            <p v-if="cluster.description" class="brand-prose mt-1 text-govnl-grijs-600">{{ cluster.description }}</p>
          </div>
        </div>
        <span
          class="inline-block rounded-card px-3 py-1 text-sm font-semibold"
          :class="[stateColor(cluster.state).bg, stateColor(cluster.state).fg]"
        >{{ stateLabel(cluster.state) }}</span>
      </div>

      <div class="mb-6 grid grid-cols-1 gap-6 md:grid-cols-2">

        <!-- Cluster info -->
        <section class="brand-card p-6" aria-labelledby="cluster-info-title">
          <h2 id="cluster-info-title" class="brand-h3 mb-4">Clusterinformatie</h2>
          <dl class="m-0 grid grid-cols-[auto_1fr] gap-x-6 gap-y-3 text-sm">
            <dt class="text-govnl-grijs-600">Scope</dt>
            <dd class="m-0 flex items-center gap-1.5">
              <IconWorld v-if="cluster.scope === 'public'" size="14" stroke="2" class="text-brand-primary" aria-hidden="true" />
              <IconBuilding v-else-if="cluster.scope === 'org'" size="14" stroke="2" class="text-brand-primary" aria-hidden="true" />
              <IconLock v-else-if="cluster.scope === 'restricted'" size="14" stroke="2" class="text-brand-primary" aria-hidden="true" />
              <span class="font-semibold text-brand-body">{{ scopeLabel(cluster.scope) }}</span>
            </dd>

            <dt class="text-govnl-grijs-600">Orchestrator naam</dt>
            <dd class="m-0 font-mono text-[13px] text-brand-body">
              {{ cluster.orchestrator_cluster_name || '—' }}
            </dd>

            <dt class="text-govnl-grijs-600">Argo CD project</dt>
            <dd class="m-0 font-mono text-[13px] text-brand-body">
              {{ cluster.argocd_project_name || 'default' }}
            </dd>

            <dt class="text-govnl-grijs-600">Geregistreerd op</dt>
            <dd class="m-0 text-brand-body">{{ formatDate(cluster.registered_at) }}</dd>

            <dt v-if="cluster.owning_organization_id" class="text-govnl-grijs-600">Eigenaar org</dt>
            <dd v-if="cluster.owning_organization_id" class="m-0 break-all font-mono text-[13px] text-brand-body">
              {{ cluster.owning_organization_id }}
            </dd>
          </dl>
        </section>

        <!-- Agent info -->
        <section class="brand-card p-6" aria-labelledby="cluster-agent-title">
          <h2 id="cluster-agent-title" class="brand-h3 mb-4 flex items-center gap-2">
            <IconCertificate size="20" stroke="2" class="text-brand-primary" aria-hidden="true" />Agent &amp; certificaat
          </h2>
          <dl class="m-0 grid grid-cols-[auto_1fr] gap-x-6 gap-y-3 text-sm">
            <dt class="text-govnl-grijs-600">Versie</dt>
            <dd class="m-0 text-brand-body">{{ cluster.agent_version ? `v${cluster.agent_version}` : '—' }}</dd>

            <dt class="text-govnl-grijs-600">Laatste heartbeat</dt>
            <dd class="m-0 text-brand-body">{{ formatDate(cluster.last_heartbeat_at) }}</dd>

            <dt class="text-govnl-grijs-600">Cert serienummer</dt>
            <dd class="m-0 break-all font-mono text-[13px] text-brand-body">
              {{ cluster.agent_cert_serial || '—' }}
            </dd>

            <dt class="text-govnl-grijs-600">Certificaat geldig tot</dt>
            <dd class="m-0 text-brand-body" data-testid="cert-not-after">{{ formatDate(certNotAfter) }}</dd>
          </dl>
          <p
            v-if="cluster.agent_cert_rotation_requested_at"
            class="mb-0 mt-4 inline-block rounded-card bg-govnl-geel-100 px-2.5 py-1 text-[13px] font-semibold text-govnl-bruin-700"
            data-testid="rotation-pending"
          >Vernieuwing aangevraagd</p>
          <div v-if="canRotateCredential" class="mt-4">
            <!-- Plain buttons with the brand-btn classes on this page: the role-guard
                 tests stub GovnlButton and click only real <button> elements. -->
            <button
              type="button"
              :disabled="isRotating"
              class="brand-btn brand-btn-secondary brand-btn-sm"
              data-testid="rotate-credential"
              @click="handleRotateCredential"
            >{{ isRotating ? 'Bezig…' : 'Inloggegevens vernieuwen' }}</button>
            <p v-if="rotateError" role="alert" class="mb-0 mt-2 text-sm text-govnl-rood-700">{{ rotateError }}</p>
          </div>
        </section>

        <!-- Ingress defaults -->
        <section class="brand-card p-6 md:col-span-2" aria-labelledby="cluster-ingress-title">
          <h2 id="cluster-ingress-title" class="brand-h3 mb-2 flex items-center gap-2">
            <IconNetwork size="20" stroke="2" class="text-brand-primary" aria-hidden="true" />Ingress standaarden
          </h2>
          <p class="brand-help mb-4 mt-0">
            Worden vooringevuld in het deploy-formulier. Deployers kunnen ze per installatie overschrijven.
          </p>
          <div class="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <label for="ingress-class" class="brand-label">Ingress class</label>
              <input
                id="ingress-class"
                v-model="ingressForm.ingress_class_name"
                :readonly="!isClusterMgr"
                type="text"
                placeholder="nginx, traefik, haproxy…"
                class="brand-input"
              />
            </div>
            <div>
              <label for="base-domain" class="brand-label">Basis-domein</label>
              <input
                id="base-domain"
                v-model="ingressForm.base_domain"
                :readonly="!isClusterMgr"
                type="text"
                placeholder="apps.gemeente.nl"
                class="brand-input"
              />
            </div>
          </div>
          <p v-if="ingressError" class="mb-3 mt-0 text-sm text-govnl-rood-700">{{ ingressError }}</p>
          <button
            v-if="isClusterMgr"
            type="button"
            :disabled="isSavingIngress"
            class="brand-btn brand-btn-navy brand-btn-sm"
            @click="saveIngress"
          >{{ ingressSaved ? 'Opgeslagen ✓' : (isSavingIngress ? 'Opslaan…' : 'Opslaan') }}</button>
        </section>

      </div>

      <!-- Access grants (restricted scope). Scope and access management is
           platform_admin only in the API, unlike the cluster_admin edits above. -->
      <section
        v-if="cluster.scope === 'restricted' && isPlatformAdmin"
        class="brand-card overflow-hidden"
        aria-labelledby="cluster-access-title"
      >
        <div class="flex items-center gap-2 border-b border-brand-border px-6 py-4">
          <IconShieldLock size="20" stroke="2" class="text-brand-primary" aria-hidden="true" />
          <h2 id="cluster-access-title" class="brand-h3">Toegang — organisaties</h2>
          <span class="ml-auto text-sm text-govnl-grijs-600">{{ grants.length }} organisatie{{ grants.length !== 1 ? 's' : '' }}</span>
        </div>

        <p v-if="grantsError" class="m-0 px-6 py-4 text-sm text-govnl-rood-700">{{ grantsError }}</p>
        <p v-else-if="grantsLoading" class="m-0 px-6 py-4 text-sm text-govnl-grijs-600">Laden…</p>
        <p v-else-if="!grants.length" class="m-0 px-6 py-5 text-sm text-govnl-grijs-600">
          Geen organisaties hebben momenteel toegang tot dit cluster.
        </p>

        <table v-else class="w-full border-collapse">
          <thead>
            <tr class="border-b border-brand-border bg-brand-surface">
              <th class="px-6 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Organisatie</th>
              <th class="hidden px-6 py-3 text-left text-sm font-semibold text-govnl-grijs-700 sm:table-cell">Verleend op</th>
              <th class="px-6 py-3"><span class="sr-only">Acties</span></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="g in grants"
              :key="g.organization_id"
              class="border-b border-brand-border last:border-0"
            >
              <td class="px-6 py-3.5">
                <div class="text-sm font-semibold text-brand-body">
                  {{ g.organization_name || g.organization_id.slice(0, 8) }}
                </div>
                <div class="mt-0.5 break-all font-mono text-xs text-govnl-grijs-600">{{ g.organization_id }}</div>
              </td>
              <td class="hidden px-6 py-3.5 text-sm text-govnl-grijs-600 sm:table-cell">
                {{ formatDate(g.granted_at) }}
              </td>
              <td class="px-6 py-3.5 text-right">
                <button
                  type="button"
                  :disabled="revokingOrgId === g.organization_id"
                  class="brand-btn brand-btn-danger brand-btn-sm"
                  @click="handleRevoke(g.organization_id)"
                >
                  <IconTrash size="14" stroke="2" aria-hidden="true" />
                  {{ revokingOrgId === g.organization_id ? '…' : 'Intrekken' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>

        <p v-if="revokeError" role="alert" class="m-0 px-6 py-3 text-sm text-govnl-rood-700">{{ revokeError }}</p>

        <!-- Grant form -->
        <div class="border-t border-brand-border bg-brand-surface px-6 py-5">
          <p id="grant-org-label" class="brand-label">Toegang verlenen aan organisatie</p>
          <div class="flex flex-wrap items-start gap-3">
            <input
              v-model="grantOrgId"
              aria-labelledby="grant-org-label"
              type="text"
              placeholder="Organisatie-UUID"
              class="brand-input min-w-0 flex-1 font-mono sm:min-w-[260px]"
              @keydown.enter="handleGrant"
            />
            <button
              type="button"
              :disabled="isGranting || !grantOrgId.trim()"
              class="brand-btn brand-btn-navy"
              @click="handleGrant"
            >
              <IconPlus size="16" stroke="2" aria-hidden="true" />
              {{ isGranting ? 'Bezig…' : 'Verlenen' }}
            </button>
          </div>
          <p v-if="grantError" class="m-0 mt-2 text-sm text-govnl-rood-700">{{ grantError }}</p>
        </div>
      </section>

    </template>
  </div>
</template>
