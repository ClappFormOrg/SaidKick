<script setup lang="ts">
import type { Component } from 'vue'

// Value pillar: a round outlined badge, a title and a short centred body.
// Used in threes or fours, inside a <ul>. The badge holds an icon or a step
// number; a numbered step shows no icon.
defineProps<{
  title: string
  body: string
  icon?: Component
  step?: string | number
}>()
</script>

<template>
  <li class="flex max-w-[260px] flex-col items-center gap-3 text-center">
    <span
      class="flex h-14 w-14 shrink-0 items-center justify-center rounded-full border border-brand-border bg-brand-white text-brand-primary"
      :class="step !== undefined && 'text-h3'"
    >
      <template v-if="step !== undefined">{{ step }}</template>
      <component :is="icon" v-else-if="icon" :size="26" :stroke="2" aria-hidden="true" />
    </span>
    <span class="brand-h3">{{ title }}</span>
    <p class="m-0 leading-[1.5] text-govnl-grijs-700">{{ body }}</p>
  </li>
</template>
