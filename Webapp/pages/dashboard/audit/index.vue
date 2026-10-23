<script setup lang="ts">
import {
  IconChevronRight, IconCircleCheck, IconCircleX, IconClock,
} from '~/lib/icons'
import type { AuditQueueItem, AuditQueueResponse } from '~/composables/useData'
import { autoValColor, autoValLabel, riskLabel, riskColor } from '~/composables/useData'
import { formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Beoordelingswachtrij' })

const { isAuditor } = useAuth()
useRoleGuard(isAuditor)

const { api } = useApi()

const { data: queue, pending } = await useAsyncData<AuditQueueResponse>(
  'audit-queue',
  () => api<AuditQueueResponse>('/api/v1/audit-submissions', {
    query: { state: 'submitted', page_size: 50 },
  }),
  { server: false, default: (): AuditQueueResponse => ({ items: [], total: 0, page: 1, page_size: 50 }) },
)

</script>

<template>
  <div class="flex flex-col gap-6">

    <div class="flex items-center gap-3 flex-wrap">
      <h1 class="brand-h1">Beoordelingswachtrij</h1>
      <span
        v-if="queue && queue.total > 0"
        class="inline-flex items-center justify-center min-w-[1.75rem] h-7 px-2 rounded-full text-sm font-bold bg-brand-primary text-white"
      >{{ queue.total }}</span>
    </div>

    <div class="brand-card overflow-hidden">
      <div v-if="pending" class="p-12 text-center text-govnl-grijs-600">
        Wachtrij laden…
      </div>

      <div v-else-if="!queue?.items?.length" class="p-16 text-center">
        <IconCircleCheck size="48" class="text-govnl-groen-700 mx-auto mb-4" aria-hidden="true" />
        <p class="text-body-lg font-semibold text-brand-body m-0">
          Geen openstaande inzendingen
        </p>
        <p class="text-sm text-govnl-grijs-600 mt-1.5 mb-0">
          Alle ingediende applicaties zijn beoordeeld.
        </p>
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse">
          <thead>
            <tr class="bg-brand-surface border-b border-brand-border">
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">App</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Versie</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">Risico</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">Stage 1</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden md:table-cell">Ingediend op</th>
              <th scope="col" class="px-4 py-3"><span class="sr-only">Acties</span></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="item in queue.items"
              :key="item.id"
              class="border-b border-brand-border last:border-b-0 transition-colors duration-150 hover:bg-brand-surface"
            >
              <td class="px-4 py-3.5">
                <NuxtLink
                  :to="`/dashboard/audit/${item.id}`"
                  class="font-semibold text-brand-primary no-underline hover:underline"
                >{{ item.app.name }}</NuxtLink>
                <div class="text-xs text-govnl-grijs-600 mt-0.5">{{ item.app.slug }}</div>
              </td>
              <td class="px-4 py-3.5 text-sm text-brand-body">
                <code class="text-sm bg-brand-surface px-1.5 py-0.5 rounded-card">{{ item.version.git_ref }}</code>
                <div class="text-xs text-govnl-grijs-600 mt-0.5 font-mono">{{ item.version.git_sha.slice(0,8) }}</div>
              </td>
              <td class="px-4 py-3.5 hidden sm:table-cell">
                <span
                  class="inline-block px-2 py-0.5 rounded-card text-xs font-semibold"
                  :class="[riskColor(item.app.risk_class).bg, riskColor(item.app.risk_class).fg]"
                >{{ riskLabel(item.app.risk_class) }}</span>
              </td>
              <td class="px-4 py-3.5 hidden sm:table-cell">
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
              <td class="px-4 py-3.5 text-sm text-govnl-grijs-600 hidden md:table-cell">
                {{ formatDate(item.created_at) }}
              </td>
              <td class="px-4 py-3.5 text-right">
                <NuxtLink
                  :to="`/dashboard/audit/${item.id}`"
                  class="brand-link inline-flex items-center gap-1 text-sm font-semibold"
                >
                  Beoordelen <IconChevronRight size="14" aria-hidden="true" />
                </NuxtLink>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

  </div>
</template>
