<script setup lang="ts">
import { IconRocket } from '~/lib/icons'
import type { ClusterAPI } from '~/composables/useData'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Applicatie installeren' })

const route = useRoute()
const { api } = useApi()
const { isDeployer } = useAuth()
useRoleGuard(isDeployer)

function queryParam(v: string | null | (string | null)[] | undefined): string {
  if (Array.isArray(v)) return v[0] ?? ''
  return v ?? ''
}

const appId        = queryParam(route.query.app_id)
const appVersionId = queryParam(route.query.app_version_id)
const appName      = route.query.app_name ? decodeURIComponent(queryParam(route.query.app_name)) : appId
const appSlug      = queryParam(route.query.app_slug)

// Arriving without a chosen app (a bookmark, a shared link, a typed URL) is
// a normal way to reach a wizard, not a fault: render a prompt to pick an
// app and keep the form hidden.
const hasSelection = Boolean(appId && appVersionId)

const { data: clusterData } = await useAsyncData<{ items: ClusterAPI[] }>(
  'clusters',
  () => api<{ items: ClusterAPI[] }>('/api/v1/clusters'),
  { server: false, default: (): { items: ClusterAPI[] } => ({ items: [] }) },
)

interface AppSchemaResponse { values_schema?: unknown }

const { data: appDetail } = appSlug
  ? await useAsyncData<AppSchemaResponse>(
      `app-schema:${appSlug}`,
      () => api<AppSchemaResponse>(`/api/v1/apps/${appSlug}`),
      { server: false },
    )
  : { data: ref<AppSchemaResponse | null>(null) }

// computed so it stays reactive when appDetail populates after hydration
const valuesSchema = computed(() => {
  const s = appDetail.value?.values_schema
  return s && typeof s === 'object' ? s as Record<string, unknown> : null
})

const NS_RE = /^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$|^[a-z0-9]$/

const form = reactive({
  cluster_id: '',
  namespace:  '',
})

const touched = reactive({ cluster_id: false, namespace: false })

const clusterValid   = computed(() => form.cluster_id !== '')
const namespaceValid = computed(() => NS_RE.test(form.namespace.trim()))
const formValid      = computed(() => clusterValid.value && namespaceValid.value)

const values       = ref<Record<string, unknown>>({})
const secretInputs = ref<Record<string, string>>({})

// When the deployer picks a cluster, pre-fill ingress schema values from
// the cluster's defaults so they don't need to look them up manually.
const selectedCluster = computed(() =>
  (clusterData.value?.items ?? []).find(c => c.id === form.cluster_id),
)

watch(selectedCluster, (cl) => {
  if (!cl) return
  values.value = { ...values.value }
  if (cl.ingress_class_name) {
    const current = values.value as Record<string, Record<string, unknown>>
    if (!current.ingress) current.ingress = {}
    if (!current.ingress.className) current.ingress.className = cl.ingress_class_name
  }
  if (cl.base_domain && appSlug) {
    const current = values.value as Record<string, Record<string, unknown>>
    if (!current.ingress) current.ingress = {}
    if (!current.ingress.host) current.ingress.host = `${appSlug}.${cl.base_domain}`
  }
})

const { run: submit, pending: isSubmitting, error: submitError } = useAsyncAction(async () => {
  const filteredSecrets: Record<string, string> = {}
  for (const [k, v] of Object.entries(secretInputs.value)) {
    if (v.trim() !== '') filteredSecrets[k] = v
  }
  const dep = await api<{ id: string }>('/api/v1/deployments', {
    method:  'POST',
    body: {
      app_id:         appId,
      app_version_id: appVersionId,
      cluster_id:     form.cluster_id,
      namespace:      form.namespace.trim(),
      values:         values.value,
      secret_inputs:  filteredSecrets,
    },
  })
  await navigateTo(`/dashboard/deployments/${dep.id}`)
}, 'Deployment aanmaken mislukt.')

async function handleSubmit() {
  touched.cluster_id = true
  touched.namespace  = true
  if (!formValid.value) return
  await submit()
}

const readyClusters = computed(() =>
  (clusterData.value?.items ?? []).filter(c => c.state === 'ready'),
)
</script>

<template>
  <div class="max-w-[720px]">
    <UiBreadcrumb
      class="mb-6"
      :items="[
        { label: 'Dashboard', to: '/dashboard' },
        { label: 'Catalogus', to: '/catalogus' },
        { label: 'Installeren' },
      ]"
    />

    <template v-if="isDeployer">
      <div class="mb-6 flex items-start gap-3">
        <IconRocket size="28" stroke="2" class="mt-1 shrink-0 text-brand-primary" aria-hidden="true" />
        <div>
          <h1 class="brand-h1">Applicatie installeren</h1>
          <p class="brand-lead mt-1">
            {{ hasSelection ? appName : 'Nog geen applicatie gekozen' }}
          </p>
        </div>
      </div>

      <section v-if="!hasSelection" class="brand-card px-6 py-10 text-center">
        <p class="brand-prose mx-auto mb-6 max-w-[28rem]">
          Kies eerst een applicatie in de catalogus. Op de detailpagina van een
          applicatie start u de installatie met de knop &ldquo;Installeren&rdquo;.
        </p>
        <GovnlButton variant="primary" to="/catalogus">Naar de catalogus</GovnlButton>
      </section>

      <section v-else class="brand-card px-6 py-7" aria-label="Installatiegegevens">

        <div class="mb-6">
          <label for="cluster" class="brand-label">
            Cluster <span class="text-govnl-rood-700" aria-hidden="true">*</span>
          </label>

          <div
            v-if="!clusterData?.items?.length"
            class="rounded-card border border-govnl-bruin-500 bg-govnl-geel-100 px-4 py-3 text-sm text-govnl-bruin-700"
          >
            Geen clusters beschikbaar.
            <NuxtLink to="/dashboard/clusters" class="font-semibold text-govnl-bruin-700 underline">Een cluster registreren →</NuxtLink>
          </div>

          <select
            v-else
            id="cluster"
            v-model="form.cluster_id"
            class="brand-input"
            :class="touched.cluster_id && !clusterValid && '!border-govnl-rood-500'"
            :aria-invalid="touched.cluster_id && !clusterValid ? 'true' : undefined"
            @blur="touched.cluster_id = true"
          >
            <option value="">— Selecteer cluster —</option>
            <option
              v-for="cl in clusterData.items"
              :key="cl.id"
              :value="cl.id"
              :disabled="cl.state !== 'ready'"
            >
              {{ cl.name }}{{ cl.state !== 'ready' ? ` (${cl.state})` : '' }} — {{ cl.scope }}
            </option>
          </select>

          <p v-if="touched.cluster_id && !clusterValid" class="brand-help !text-govnl-rood-700">
            Selecteer een cluster.
          </p>
          <p v-if="readyClusters.length === 0 && clusterData?.items?.length" class="brand-help !text-govnl-bruin-700">
            Geen clusters in staat "ready". Wacht totdat het cluster de handshake heeft voltooid.
          </p>
        </div>

        <div class="mb-7">
          <label for="namespace" class="brand-label">
            Namespace <span class="text-govnl-rood-700" aria-hidden="true">*</span>
          </label>
          <input
            id="namespace"
            v-model="form.namespace"
            type="text"
            required
            placeholder="bijv. productie, test-omgeving"
            class="brand-input"
            :class="touched.namespace && !namespaceValid && '!border-govnl-rood-500'"
            :aria-invalid="touched.namespace && !namespaceValid ? 'true' : undefined"
            aria-describedby="namespace-help"
            @blur="touched.namespace = true"
            @keydown.enter="handleSubmit"
          />
          <p v-if="touched.namespace && !namespaceValid" id="namespace-help" class="brand-help !text-govnl-rood-700">
            Geldige Kubernetes-namespace vereist (DNS-1123: kleine letters, cijfers en koppeltekens).
          </p>
          <p v-else id="namespace-help" class="brand-help">
            Kubernetes-namespace voor deze installatie.
          </p>
        </div>

        <div v-if="valuesSchema" class="mb-7 border-t border-brand-border pt-6">
          <h2 class="brand-h3 mb-4">Configuratie</h2>
          <DeployValuesForm
            :schema="valuesSchema"
            :model-values="values"
            :model-secret-inputs="secretInputs"
            @update:model-values="values = $event"
            @update:model-secret-inputs="secretInputs = $event"
          />
        </div>

        <p
          v-if="submitError"
          role="alert"
          class="mb-5 mt-0 rounded-card border border-govnl-rood-500 bg-govnl-rood-50 px-4 py-3 text-sm text-govnl-rood-700"
        >{{ submitError }}</p>

        <div class="flex flex-wrap items-center justify-between gap-4">
          <NuxtLink to="/catalogus" class="brand-link text-sm">← Annuleren</NuxtLink>
          <!-- A plain button: the role-guard tests click only real <button> elements. -->
          <button
            type="button"
            :disabled="isSubmitting"
            class="brand-btn brand-btn-primary"
            @click="handleSubmit"
          >
            {{ isSubmitting ? 'Installeren…' : 'Installeren' }}
          </button>
        </div>

      </section>
    </template>
  </div>
</template>
