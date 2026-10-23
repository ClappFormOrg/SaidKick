<script setup lang="ts">
import { IconBuilding, IconClock, IconUsers } from '~/lib/icons'
import type { App } from '~/types'
import { CRITERIA } from '~/composables/useData'

// Catalogue list row: AppCard's content at a density that fits a screenful
// of apps. The meta column drops away below `sm`.
const props = defineProps<{ app: App }>()

const passCount = computed(() => Object.values(props.app.scores).filter(v => v === 'pass').length)
const allPass = computed(() => passCount.value === CRITERIA.length)
</script>

<template>
  <li class="grid list-none grid-cols-1 items-center gap-4 border-0 border-b border-solid border-brand-border px-5 py-4 transition-colors duration-150 ease-out hover:bg-brand-surface sm:grid-cols-[1fr_220px] lg:grid-cols-[1fr_220px_160px]">
    <div class="flex min-w-0 flex-col gap-1.5">
      <div class="flex flex-wrap items-center gap-2 text-brand-primary">
        <UiCategoryIcon :category="app.category" :size="16" />
        <UiStatusPill :status="app.status" />
      </div>
      <NuxtLink :to="`/catalogus/${app.slug}`" class="brand-link truncate font-semibold">{{ app.name }}</NuxtLink>
      <p class="m-0 truncate text-sm text-govnl-grijs-600">{{ app.tagline }}</p>
    </div>

    <div class="hidden flex-col gap-1 text-sm text-govnl-grijs-600 sm:flex">
      <p class="m-0 flex items-center gap-1.5">
        <IconBuilding size="14" stroke="2" aria-hidden="true" />
        <span class="truncate">{{ app.eigenaar }}</span>
      </p>
      <p class="m-0 flex items-center gap-1.5">
        <IconClock size="14" stroke="2" aria-hidden="true" />
        <span>{{ app.bijgewerkt }}</span>
      </p>
      <p class="m-0 flex items-center gap-1.5">
        <IconUsers size="14" stroke="2" aria-hidden="true" />
        <span>{{ app.installs }} installaties</span>
      </p>
    </div>

    <div class="flex flex-row items-center gap-3 lg:flex-col lg:items-end lg:gap-1.5">
      <UiScoreDots :scores="app.scores" />
      <span
        class="text-sm font-semibold"
        :class="allPass ? 'text-govnl-groen-700' : 'text-govnl-grijs-600'"
      >{{ passCount }}/{{ CRITERIA.length }} voldoet</span>
    </div>
  </li>
</template>
