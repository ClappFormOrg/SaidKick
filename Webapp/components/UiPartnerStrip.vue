<script setup lang="ts">
// The organisations taking part, as text wordmarks. Not placed on any page
// yet: the provinces have not confirmed they want to be listed (#156, Q6).
// Logos, once licensed, must be self-hosted to pass the CSP's img-src 'self'.
withDefaults(defineProps<{
  partners: string[]
  label?: string
}>(), {
  label: 'In samenwerking met',
})
</script>

<template>
  <section class="bg-brand-white" :aria-label="label">
    <div class="mx-auto max-w-page px-6 pb-11 pt-8">
      <p class="m-0 mb-[22px] text-brand-body">{{ label }}</p>
      <!-- grijs-600 (7.58:1 on white). grijs-500/80 blended to 3.24:1 and
           failed AA (#46). -->
      <ul class="m-0 flex list-none flex-wrap items-center justify-between gap-6 p-0">
        <li
          v-for="p in partners"
          :key="p"
          class="whitespace-nowrap font-semibold text-govnl-grijs-600"
        >{{ p }}</li>
      </ul>
    </div>
  </section>
</template>
