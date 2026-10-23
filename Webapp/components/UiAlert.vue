<script setup lang="ts">
import type { Component } from 'vue'
import {
  IconAlertCircle, IconAlertTriangle, IconCircleCheck, IconInfoCircle,
} from '~/lib/icons'

// Feedback message in one of the four feedback tones. Colour carries meaning
// here, so every tone also has an icon and a spoken prefix; a negative message
// is announced as an alert, the others as a status.
const props = withDefaults(defineProps<{
  tone?: 'informative' | 'positive' | 'warning' | 'negative'
  title?: string
}>(), {
  tone: 'informative',
})

interface Tone {
  classes: string
  icon: Component
  role: 'status' | 'alert'
  label: string
}

const TONES: Record<string, Tone> = {
  informative: {
    classes: 'bg-govnl-hemelblauw-50 border-govnl-hemelblauw-500 text-govnl-hemelblauw-800',
    icon: IconInfoCircle,
    role: 'status',
    label: 'Informatie',
  },
  positive: {
    classes: 'bg-govnl-groen-50 border-govnl-groen-500 text-govnl-groen-700',
    icon: IconCircleCheck,
    role: 'status',
    label: 'Gelukt',
  },
  warning: {
    classes: 'bg-govnl-geel-100 border-govnl-bruin-500 text-govnl-bruin-700',
    icon: IconAlertTriangle,
    role: 'status',
    label: 'Let op',
  },
  negative: {
    classes: 'bg-govnl-rood-50 border-govnl-rood-500 text-govnl-rood-700',
    icon: IconAlertCircle,
    role: 'alert',
    label: 'Fout',
  },
}

const tone = computed(() => TONES[props.tone] ?? TONES.informative!)
</script>

<template>
  <div
    :role="tone.role"
    class="flex items-start gap-3 rounded-card border px-4 py-3"
    :class="tone.classes"
  >
    <component :is="tone.icon" :size="18" :stroke="2" class="mt-0.5 shrink-0" aria-hidden="true" />
    <div class="min-w-0">
      <p class="m-0 font-semibold">
        <span v-if="title" class="sr-only">{{ tone.label }}: </span>{{ title ?? tone.label }}
      </p>
      <div v-if="$slots.default" class="mt-1">
        <slot />
      </div>
    </div>
  </div>
</template>
