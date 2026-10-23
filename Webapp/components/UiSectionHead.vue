<script setup lang="ts">
// Section heading with an optional lead paragraph and an optional trailing
// action, so every section starts the same way. `level` sets the type size;
// `as` sets the element, so the document outline can differ from the size.
withDefaults(defineProps<{
  title: string
  lead?: string
  level?: 'h1' | 'h2' | 'h3'
  as?: 'h1' | 'h2' | 'h3' | 'h4'
  id?: string
  center?: boolean
}>(), {
  level: 'h2',
  center: false,
})
</script>

<template>
  <div
    class="mb-6 flex flex-wrap items-end justify-between gap-x-6 gap-y-2"
    :class="center && 'flex-col items-center text-center'"
  >
    <div :class="center ? 'max-w-[45rem]' : 'max-w-[52rem]'">
      <component
        :is="as ?? level"
        :id="id"
        :class="`brand-${level}`"
      >{{ title }}</component>
      <p v-if="lead" class="brand-prose mt-3">{{ lead }}</p>
    </div>
    <div v-if="$slots.action" class="shrink-0">
      <slot name="action" />
    </div>
  </div>
</template>
