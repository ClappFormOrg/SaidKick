<script setup lang="ts">
import { CRITERIA, SCORE } from '~/composables/useData'

const props = defineProps<{ scores: Record<string, string> }>()

const scoreLabel: Record<string, string> = {
  pass: 'voldoet',
  partial: 'gedeeltelijk',
  fail: 'voldoet niet',
  pending: 'in beoordeling',
}

const ariaLabel = computed(() =>
  'Criteria: ' + CRITERIA.map(c =>
    `${c.label} ${scoreLabel[props.scores[c.key] ?? 'pending'] ?? 'in beoordeling'}`
  ).join(', ')
)

const dotColor = (val: string) => (SCORE[val] ?? SCORE.pending!).color
</script>

<template>
  <div role="img" :aria-label="ariaLabel" class="flex items-center gap-1">
    <span
      v-for="c in CRITERIA"
      :key="c.key"
      aria-hidden="true"
      class="block h-[11px] w-[11px] shrink-0 rounded-[2px]"
      :style="{ backgroundColor: dotColor(scores[c.key] ?? 'pending') }"
    />
  </div>
</template>
