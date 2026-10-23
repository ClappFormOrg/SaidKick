<script setup lang="ts">
import {
  IconApps, IconAlertTriangle, IconUsers, IconRocket,
  IconCircleCheck, IconCircleX, IconClock, IconShieldSearch,
} from '~/lib/icons'
import { useApps, useDeployments, useClusters, useMyApps, useAuditQueue, autoValColor, autoValLabel, riskLabel, riskColor } from '~/composables/useData'
import { formatDate } from '~/utils/constants'
import { errorMessage } from '~/composables/useApi'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Dashboard' })

const { me, fetchMe, isDeveloper, isDeployer, isClusterMgr, isAuditor, isOrgAdmin, isAppUserOnly } = useAuth()

// A user holding only app_user has nothing on this page; the launcher is
// their dashboard.
useRoleGuard(computed(() => !isAppUserOnly.value), '/dashboard/my-apps')

onMounted(fetchMe)

const displayName  = computed(() => me.value?.name  ?? 'gebruiker')
const displayEmail = computed(() => me.value?.email ?? '')

const today = ref('')
onMounted(() => {
  today.value = new Date().toLocaleDateString('nl-NL', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })
})

// Live data — all server: false (need session cookie)
const { data: apps }        = await useApps()
const { data: deployments } = await useDeployments()
const { data: clusters }    = await useClusters()
const { data: myApps }      = await useMyApps()
const { data: auditQueue }  = await useAuditQueue()

const { api, publicApi } = useApi()

interface Advisory {
  id: string; title: string; body: string; severity: string
  app_slug?: string; published_at: string; expires_at?: string
}
interface Discussion { id: string; title: string; url: string; category: string; updated_at: string; comment_count: number }

const advisoryItems   = ref<Advisory[]>([])
const discussionItems = ref<Discussion[]>([])
const advisoriesError  = ref('')
const discussionsError = ref('')

onMounted(async () => {
  try {
    const r = await api<{ items: Advisory[] }>('/api/v1/advisories')
    advisoryItems.value = r.items ?? []
  } catch (err: unknown) {
    advisoriesError.value = errorMessage(err, 'Advisories laden mislukt.')
  }
  try {
    const r = await publicApi<{ items: Discussion[] }>('/api/v1/community/discussions', { query: { page_size: 5 } })
    discussionItems.value = r.items ?? []
  } catch (err: unknown) {
    discussionsError.value = errorMessage(err, 'Community-discussies laden mislukt.')
  }
})

const statCards = computed(() => [
  { label: 'Apps in catalogus', value: apps.value?.length ?? 0,       icon: IconApps },
  { label: 'Open advisories',   value: advisoryItems.value.length,     icon: IconAlertTriangle },
  { label: 'Community threads', value: discussionItems.value.length,   icon: IconUsers },
  { label: 'Deployments',       value: deployments.value?.total ?? 0, icon: IconRocket },
  ...(isAuditor.value ? [{ label: 'Open beoordelingen', value: auditQueue.value?.total ?? 0, icon: IconShieldSearch }] : []),
])

function deploymentStateColor(state: string) {
  switch (state) {
    case 'running':   return { bg: 'var(--govnl-color-groen-50)',     fg: 'var(--govnl-color-groen-700)' }
    case 'pending':   return { bg: 'var(--govnl-color-geel-50)',      fg: 'var(--govnl-color-bruin-700)' }
    case 'failed':    return { bg: 'var(--govnl-color-rood-50)',      fg: 'var(--govnl-color-rood-700)' }
    case 'stopped':   return { bg: 'var(--govnl-color-grijs-100)',    fg: 'var(--govnl-color-grijs-600)' }
    default:          return { bg: 'var(--govnl-color-grijs-100)',    fg: 'var(--govnl-color-grijs-600)' }
  }
}

// Column header style shared by every panel table.
const TH = 'text-left px-6 py-2.5 text-sm font-semibold text-govnl-grijs-700 bg-brand-surface border-b border-brand-border'
</script>

<template>
  <div class="flex flex-col gap-6">

    <div>
      <h1 class="brand-h1">Welkom terug, {{ displayName }}</h1>
      <p class="mt-2 text-sm text-govnl-grijs-600">
        {{ displayEmail }}<template v-if="displayEmail && today"> &middot; </template>{{ today }}
      </p>
    </div>

    <!-- Auditor queue panel -->
    <section v-if="isAuditor" class="brand-card overflow-hidden" aria-labelledby="audit-queue-heading">
      <div class="px-6 py-4 border-b border-brand-border flex flex-wrap items-center justify-between gap-4">
        <div class="flex items-center gap-3">
          <IconShieldSearch size="22" stroke="1.5" class="text-brand-primary shrink-0" aria-hidden="true" />
          <h2 id="audit-queue-heading" class="brand-h3">Beoordelingswachtrij</h2>
          <span
            v-if="auditQueue && auditQueue.total > 0"
            class="inline-flex items-center justify-center min-w-[1.5rem] h-6 px-1.5 rounded-full text-xs font-bold bg-brand-primary text-white"
          >{{ auditQueue.total }}</span>
        </div>
        <NuxtLink to="/dashboard/audit" class="brand-link text-sm font-semibold shrink-0">Alle inzendingen →</NuxtLink>
      </div>

      <div
        v-if="!auditQueue?.items?.length"
        class="px-6 py-10 text-center text-govnl-grijs-600 text-sm flex flex-col items-center gap-2"
      >
        <IconCircleCheck size="32" class="text-govnl-groen-700" aria-hidden="true" />
        Geen openstaande inzendingen.
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse" aria-labelledby="audit-queue-heading">
          <thead>
            <tr>
              <th
                v-for="col in ['App', 'Versie', 'Risico', 'Stage 1', 'Ingediend op', '']"
                :key="col"
                scope="col"
                :class="TH"
              >{{ col }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="item in auditQueue.items.slice(0, 5)"
              :key="item.id"
              class="border-b border-brand-border hover:bg-brand-surface transition-colors duration-150"
            >
              <td class="px-6 py-3.5">
                <NuxtLink
                  :to="`/dashboard/audit/${item.id}`"
                  class="font-semibold text-brand-primary no-underline hover:underline"
                >{{ item.app.name }}</NuxtLink>
                <div class="text-xs text-govnl-grijs-600 mt-0.5">{{ item.app.slug }}</div>
              </td>
              <td class="px-6 py-3.5 text-sm text-brand-body">
                <code class="text-sm bg-brand-surface px-1.5 py-0.5 rounded-card">{{ item.version.git_ref }}</code>
                <div class="text-xs text-govnl-grijs-600 mt-0.5 font-mono">{{ item.version.git_sha.slice(0, 8) }}</div>
              </td>
              <td class="px-6 py-3.5 hidden sm:table-cell">
                <span
                  class="inline-block px-2 py-0.5 rounded-card text-xs font-semibold"
                  :class="[riskColor(item.app.risk_class).bg, riskColor(item.app.risk_class).fg]"
                >{{ riskLabel(item.app.risk_class) }}</span>
              </td>
              <td class="px-6 py-3.5 hidden sm:table-cell">
                <span
                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-card text-xs font-semibold"
                  :class="[autoValColor(item.auto_validation_state).bg, autoValColor(item.auto_validation_state).fg]"
                >
                  <IconCircleCheck v-if="item.auto_validation_state === 'passed'" size="12" aria-hidden="true" />
                  <IconCircleX v-else-if="item.auto_validation_state === 'failed'" size="12" aria-hidden="true" />
                  <IconClock v-else size="12" aria-hidden="true" />
                  {{ autoValLabel(item.auto_validation_state) }}
                </span>
              </td>
              <td class="px-6 py-3.5 text-sm text-govnl-grijs-600 hidden md:table-cell">
                {{ formatDate(item.created_at) }}
              </td>
              <td class="px-6 py-3.5 text-right">
                <NuxtLink
                  :to="`/dashboard/audit/${item.id}`"
                  class="brand-link inline-flex items-center gap-1 text-sm font-semibold"
                >Beoordelen →</NuxtLink>
              </td>
            </tr>
          </tbody>
        </table>

        <div
          v-if="(auditQueue?.total ?? 0) > 5"
          class="px-6 py-3 text-right text-sm"
        >
          <NuxtLink to="/dashboard/audit" class="brand-link font-semibold">Alle {{ auditQueue.total }} inzendingen bekijken →</NuxtLink>
        </div>
      </div>
    </section>

    <!-- Stat cards -->
    <ul class="m-0 p-0 list-none grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
      <li
        v-for="card in statCards"
        :key="card.label"
        class="brand-card p-5 flex flex-col gap-3"
      >
        <div class="flex items-center gap-2.5">
          <component :is="card.icon" size="20" stroke="2" class="text-brand-primary shrink-0" aria-hidden="true" />
          <span class="text-sm text-govnl-grijs-600 font-medium">{{ card.label }}</span>
        </div>
        <div class="text-h1 font-bold text-brand-primary leading-none">
          {{ card.value }}
        </div>
      </li>
    </ul>

    <!-- Deployments panel (read-only) — deployer sees their own, org_admin
         their org's, platform_admin all, matching ListDeployments -->
    <section v-if="me && (isDeployer || isOrgAdmin)" class="brand-card overflow-hidden" aria-labelledby="deployments-heading">
      <h2 id="deployments-heading" class="brand-h3 px-6 py-4 border-b border-brand-border">
        Deployments (laatste 10)
      </h2>

      <div
        v-if="!deployments?.items?.length"
        class="px-6 py-10 text-center text-govnl-grijs-600 text-sm"
      >U heeft nog geen deployments.</div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse" aria-labelledby="deployments-heading">
          <thead>
            <tr>
              <th
                v-for="col in ['ID', 'Namespace', 'Status', 'Bijgewerkt']"
                :key="col"
                scope="col"
                :class="TH"
              >{{ col }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="dep in (deployments?.items ?? []).slice(0, 10)"
              :key="dep.id"
              class="border-b border-brand-border last:border-b-0"
            >
              <td class="px-6 py-3 font-mono text-sm text-govnl-grijs-600">
                {{ dep.id.slice(0, 8) }}&hellip;
              </td>
              <td class="px-6 py-3 text-sm text-brand-body">
                {{ dep.namespace || '—' }}
              </td>
              <td class="px-6 py-3">
                <span
                  class="inline-block px-2.5 py-0.5 rounded-full text-sm font-semibold capitalize"
                  :style="{ backgroundColor: deploymentStateColor(dep.state).bg, color: deploymentStateColor(dep.state).fg }"
                >{{ dep.state }}</span>
              </td>
              <td class="px-6 py-3 text-sm text-govnl-grijs-600">
                {{ formatDate(dep.last_state_change_at) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Clusters — cluster_admin / platform_admin -->
    <section v-if="me && isClusterMgr" class="brand-card overflow-hidden" aria-labelledby="clusters-heading">
      <div class="px-6 py-4 border-b border-brand-border flex flex-wrap items-center justify-between gap-4">
        <h2 id="clusters-heading" class="brand-h3">Custom cluster Configuratie</h2>
        <NuxtLink
          v-if="clusters?.items?.length"
          to="/dashboard/clusters"
          class="brand-link text-sm font-semibold"
        >Alle clusters beheren →</NuxtLink>
      </div>

      <div
        v-if="!clusters?.items?.length"
        class="p-6 text-center text-govnl-grijs-600 text-sm flex flex-col gap-3 items-center"
      >
        <span>Geen cluster geconfigureerd.</span>
        <GovnlButton variant="secondary" size="sm" to="/dashboard/clusters">Cluster configureren</GovnlButton>
      </div>

      <ul v-else class="m-0 list-none px-6 py-4 flex flex-col gap-3">
        <li
          v-for="cl in clusters.items"
          :key="cl.id"
          class="flex items-center justify-between gap-4 px-4 py-3 bg-brand-surface rounded-card border border-brand-border"
        >
          <div class="min-w-0">
            <div class="font-semibold text-brand-body">{{ cl.name }}</div>
            <div class="text-sm text-govnl-grijs-700 mt-0.5">{{ cl.scope }} &middot; v{{ cl.agent_version || '—' }}</div>
          </div>
          <span
            class="px-2.5 py-0.5 rounded-full text-sm font-semibold capitalize border"
            :class="cl.state === 'active' ? 'bg-govnl-groen-50 text-govnl-groen-700 border-govnl-groen-300' : 'bg-white text-govnl-grijs-700 border-brand-border'"
          >{{ cl.state }}</span>
        </li>
      </ul>
    </section>

    <!-- Geüploade applicaties — developer -->
    <section v-if="me && isDeveloper" class="brand-card overflow-hidden" aria-labelledby="myapps-heading">
      <div class="px-6 py-4 border-b border-brand-border flex flex-wrap items-center justify-between gap-4">
        <h2 id="myapps-heading" class="brand-h3">Geüploade applicaties</h2>
        <GovnlButton variant="primary" size="sm" to="/dashboard/apps/new">App toevoegen</GovnlButton>
      </div>

      <div
        v-if="!myApps?.length"
        class="px-6 py-10 text-center text-govnl-grijs-600 text-sm"
      >U heeft nog geen applicaties ingediend. Klik op "App toevoegen" om te beginnen.</div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse" aria-labelledby="myapps-heading">
          <thead>
            <tr>
              <th
                v-for="col in ['Naam', 'Versie', 'Status', 'Bijgewerkt', 'Installaties']"
                :key="col"
                scope="col"
                :class="TH"
              >{{ col }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="a in myApps"
              :key="a.slug"
              class="border-b border-brand-border last:border-b-0"
            >
              <td class="px-6 py-3.5">
                <NuxtLink
                  :to="`/dashboard/apps/${a.slug}`"
                  class="font-semibold text-brand-primary no-underline hover:underline"
                >{{ a.name }}</NuxtLink>
              </td>
              <td class="px-6 py-3.5 font-mono text-sm text-brand-body">
                {{ a.versie ? `v${a.versie}` : '—' }}
              </td>
              <td class="px-6 py-3.5"><UiStatusPill :status="a.status" /></td>
              <td class="px-6 py-3.5 text-sm text-govnl-grijs-600">{{ a.bijgewerkt || '—' }}</td>
              <td class="px-6 py-3.5 text-sm text-govnl-grijs-600">{{ a.installs }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Updates panel -->
    <section class="brand-card overflow-hidden" aria-labelledby="updates-heading">
      <h2 id="updates-heading" class="brand-h3 px-6 py-4 border-b border-brand-border">
        Updates — Community &amp; Security
      </h2>
      <div
        v-if="advisoriesError || discussionsError"
        role="alert"
        class="px-6 py-3 border-b border-brand-border bg-govnl-rood-50 text-sm text-govnl-rood-700"
      >
        <div v-if="advisoriesError">{{ advisoriesError }}</div>
        <div v-if="discussionsError">{{ discussionsError }}</div>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full border-collapse" aria-labelledby="updates-heading">
          <thead>
            <tr>
              <th
                v-for="col in ['Ernst', 'Omschrijving', 'Datum']"
                :key="col"
                scope="col"
                :class="TH"
              >{{ col }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="advisoryItems.length === 0 && !advisoriesError">
              <td colspan="4" class="px-6 py-6 text-center text-sm text-govnl-grijs-600">Geen actieve advisories.</td>
            </tr>
            <tr
              v-for="adv in advisoryItems"
              :key="adv.id"
              class="border-b border-brand-border last:border-b-0"
            >
              <td class="px-6 py-3">
                <span
                  class="inline-flex items-center gap-1.5 text-xs font-semibold px-2 py-0.5 rounded-full uppercase tracking-[0.04em]"
                  :class="adv.severity === 'critical' ? 'bg-govnl-rood-50 text-govnl-rood-700'
                        : adv.severity === 'warning'  ? 'bg-govnl-geel-50 text-govnl-bruin-700'
                        : 'bg-govnl-hemelblauw-50 text-govnl-hemelblauw-800'"
                >
                  <IconAlertTriangle size="12" stroke="2" aria-hidden="true" />
                  {{ adv.severity }}
                </span>
              </td>
              <td class="px-6 py-3 text-sm text-brand-body">
                {{ adv.title }}
                <span v-if="adv.app_slug" class="ml-2 text-xs text-govnl-grijs-600 font-mono">{{ adv.app_slug }}</span>
              </td>
              <td class="px-6 py-3 text-sm text-govnl-grijs-600" colspan="2">
                {{ new Date(adv.published_at).toLocaleDateString('nl-NL', { day: 'numeric', month: 'short', year: 'numeric' }) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

  </div>
</template>
