<script setup lang="ts">
import { IconCircleCheck } from '~/lib/icons'

// The stats bar directly under the hero. One rounded corner, bottom right.
defineProps<{
  stats: { value: string | number; label: string }[]
}>()
</script>

<template>
  <div class="rounded-br-hero bg-brand-surface">
    <ul class="mx-auto my-0 flex max-w-page list-none flex-wrap justify-center gap-x-12 gap-y-3 px-6 py-[18px]">
      <li
        v-for="s in stats"
        :key="s.label"
        class="flex items-center gap-2.5 text-brand-primary"
      >
        <IconCircleCheck size="20" stroke="2" aria-hidden="true" />
        <span><strong class="font-bold">{{ s.value }}</strong> {{ s.label }}</span>
      </li>
    </ul>
  </div>
</template>
