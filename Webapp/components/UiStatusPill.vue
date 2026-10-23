<script setup lang="ts">
import { statusDotColor } from '~/composables/useData'

// Status badge: a neutral pill with a coloured dot and the status as text.
// The label always names the status, so the dot is decorative.
const props = defineProps<{ status: string }>()

const dot = computed(() => statusDotColor(props.status))
</script>

<template>
  <span class="inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border border-brand-border bg-brand-surface px-2.5 py-0.5 text-sm leading-[1.4] text-brand-body">
    <span
      aria-hidden="true"
      class="h-2 w-2 shrink-0 rounded-full"
      :style="{ backgroundColor: dot }"
    />
    {{ status }}
  </span>
</template>
