<script setup lang="ts">
import type { Component } from 'vue'
import {
  IconApps, IconChartLine, IconCloud, IconDatabase, IconFileText, IconMessageCircle, IconRobot,
} from '~/lib/icons'
import { CATEGORIES } from '~/composables/useData'

// A category's glyph. Categories are told apart by their icon, not by a
// colour of their own; the glyph takes currentColor. An unknown category gets
// a generic glyph and is named by its slug.
const props = withDefaults(defineProps<{
  category: string
  size?: number
  // False when a visible label next to the icon already names the category.
  labelled?: boolean
}>(), {
  size: 20,
  labelled: true,
})

const ICONS: Record<string, Component> = {
  robot: IconRobot,
  'message-circle': IconMessageCircle,
  database: IconDatabase,
  'file-text': IconFileText,
  'chart-line': IconChartLine,
  cloud: IconCloud,
}

const meta = computed(() => CATEGORIES.find(c => c.slug === props.category))
const icon = computed(() => ICONS[meta.value?.icon ?? ''] ?? IconApps)
const label = computed(() => meta.value?.title ?? props.category)
</script>

<template>
  <component
    :is="icon"
    :size="size"
    :stroke="2"
    :role="labelled ? 'img' : undefined"
    :aria-label="labelled ? label : undefined"
    :aria-hidden="labelled ? undefined : 'true'"
  />
</template>
