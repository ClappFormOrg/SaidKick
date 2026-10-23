<script setup lang="ts">
import type { Component } from 'vue'
import { IconLayoutGrid, IconList } from '~/lib/icons'

// A set of toggle buttons where exactly one is on, such as the catalogue's
// grid/list view. Each option is a button with aria-pressed inside a labelled
// group; the active one is filled with the surface colour and set in bold, so
// the state never rests on colour alone.
interface SegmentedOption {
  id: string
  label: string
  icon?: Component
}

const props = withDefaults(defineProps<{
  modelValue: string
  // Accessible name for the group, e.g. "Weergave".
  label: string
  options?: SegmentedOption[]
}>(), {
  options: () => [
    { id: 'raster', label: 'Raster', icon: IconLayoutGrid },
    { id: 'lijst', label: 'Lijst', icon: IconList },
  ],
})

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

function select(id: string) {
  if (id !== props.modelValue) emit('update:modelValue', id)
}
</script>

<template>
  <div
    role="group"
    :aria-label="label"
    class="inline-flex overflow-hidden rounded-card border border-brand-border"
  >
    <button
      v-for="(opt, i) in options"
      :key="opt.id"
      type="button"
      :aria-pressed="opt.id === modelValue ? 'true' : 'false'"
      class="inline-flex cursor-pointer items-center gap-[7px] border-0 px-3.5 py-[7px] text-base"
      :class="[
        i > 0 && 'border-l border-solid border-brand-border',
        opt.id === modelValue
          ? 'bg-brand-surface font-bold text-brand-primary'
          : 'bg-brand-white font-normal text-govnl-grijs-700 hover:bg-govnl-grijs-50',
      ]"
      @click="select(opt.id)"
    >
      <component :is="opt.icon" v-if="opt.icon" :size="16" :stroke="2" aria-hidden="true" />
      {{ opt.label }}
    </button>
  </div>
</template>
