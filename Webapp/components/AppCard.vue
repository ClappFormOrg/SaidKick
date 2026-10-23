<script setup lang="ts">
import { IconArrowRight } from '~/lib/icons'
import type { App } from '~/types'
import { CATEGORIES } from '~/composables/useData'

// Catalogue card. Reading order: type badge and status, the underlined title,
// the owner, the description, the criteria pinned to the bottom. A 1px border
// all round; hover changes the fill, never the shape. The whole card is the
// link, so the hit target matches what the card looks like.
const props = defineProps<{ app: App }>()

const category = computed(() => CATEGORIES.find(c => c.slug === props.app.category))
</script>

<template>
  <NuxtLink
    :to="`/catalogus/${app.slug}`"
    class="brand-card brand-card-interactive flex h-full min-h-[230px] flex-col gap-2 p-4 no-underline"
  >
    <div class="flex flex-wrap items-center gap-2">
      <UiTypeBadge>{{ category?.title ?? app.category }}</UiTypeBadge>
      <UiStatusPill :status="app.status" />
    </div>

    <span class="mt-0.5 line-clamp-3 text-body font-bold leading-[1.35] text-brand-primary underline underline-offset-[3px]">{{ app.name }}</span>

    <span class="text-sm font-bold text-brand-primary">{{ app.eigenaar }}</span>

    <p v-if="app.tagline" class="m-0 line-clamp-3 leading-[1.5] text-govnl-grijs-700">{{ app.tagline }}</p>

    <p v-if="app.versie" class="m-0 text-sm text-govnl-grijs-600">
      <span class="sr-only">versie </span>{{ app.versie }}
    </p>

    <div class="mt-auto flex items-center justify-between gap-3 border-0 border-t border-solid border-brand-border pt-3">
      <UiScoreDots :scores="app.scores" />
      <span class="flex items-center gap-1 text-sm font-semibold text-brand-primary">
        Bekijken
        <IconArrowRight size="15" stroke="2" aria-hidden="true" />
      </span>
    </div>
  </NuxtLink>
</template>
