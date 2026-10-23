<script setup lang="ts">
import { IconChevronRight } from '~/lib/icons'

// Breadcrumb trail. The last item is the current page: never a link, and
// marked aria-current="page".
defineProps<{
  items: { label: string; to?: string }[]
}>()
</script>

<template>
  <nav aria-label="Kruimelpad">
    <ol class="m-0 flex list-none flex-wrap items-center gap-1.5 p-0 text-sm">
      <li v-for="(item, i) in items" :key="`${i}-${item.label}`" class="flex items-center gap-1.5">
        <IconChevronRight
          v-if="i > 0"
          size="14"
          stroke="2"
          class="text-govnl-grijs-600"
          aria-hidden="true"
        />
        <NuxtLink v-if="item.to && i < items.length - 1" :to="item.to" class="brand-link">
          {{ item.label }}
        </NuxtLink>
        <span v-else aria-current="page" class="font-semibold text-brand-body">{{ item.label }}</span>
      </li>
    </ol>
  </nav>
</template>
