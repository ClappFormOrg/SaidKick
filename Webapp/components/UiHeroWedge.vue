<script setup lang="ts">
// Hero: a light secondary field with the diagonal wedge on the right. The
// wedge is decorative and hidden from assistive tech; content stays in the
// left part so it never runs into it.
defineProps<{
  // Short bold line above the title, in the CTA colour.
  eyebrow?: string
}>()
</script>

<template>
  <section class="relative overflow-hidden bg-brand-secondary">
    <div aria-hidden="true" class="brand-wedge hidden sm:block" />
    <div class="relative mx-auto max-w-page px-6 pb-[72px] pt-16">
      <div class="max-w-[620px]">
        <p
          v-if="eyebrow"
          class="m-0 mb-[22px] text-[1.625rem] font-bold leading-none tracking-[-0.01em] text-brand-cta"
        >{{ eyebrow }}</p>
        <slot />
      </div>
    </div>
  </section>
</template>
