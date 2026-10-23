<script setup lang="ts">
// Large primary-coloured promo panel, the heaviest element on a page and used
// once. Actions go in the default slot, pinned to the bottom so panels side by
// side line their buttons up.
defineProps<{
  title: string
  body: string
}>()
</script>

<template>
  <div class="flex flex-col gap-4 bg-brand-primary p-7 text-brand-white">
    <h2 class="brand-h2 !text-brand-white">{{ title }}</h2>
    <p class="m-0 text-body leading-[1.55]">{{ body }}</p>
    <div v-if="$slots.default" class="mt-auto flex flex-wrap gap-3 pt-4">
      <slot />
    </div>
  </div>
</template>
