<script setup lang="ts">
// Tag chip, with 4px corners to set it apart from a fully rounded badge.
// `neutral` is the plain chip; `brand` tints it with the primary colour for a
// chip that names an attribute of the thing rather than a free label.
withDefaults(defineProps<{
  tone?: 'neutral' | 'brand'
}>(), {
  tone: 'neutral',
})
</script>

<template>
  <span
    class="inline-flex items-center whitespace-nowrap rounded-card border px-[9px] py-0.5 text-sm leading-[1.4]"
    :class="tone === 'brand'
      ? 'border-brand-primary-subtle bg-brand-primary-subtle font-semibold text-brand-primary'
      : 'border-brand-border bg-brand-white text-govnl-grijs-700'"
  >
    <slot />
  </span>
</template>
