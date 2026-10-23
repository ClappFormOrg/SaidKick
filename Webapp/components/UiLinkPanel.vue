<script setup lang="ts">
import { IconArrowRight } from '~/lib/icons'

// White card that is a link as a whole, with an arrow bottom right. The card
// is the hit target; there is no separate "read more".
defineProps<{
  title: string
  body: string
  to: string
}>()
</script>

<template>
  <NuxtLink
    :to="to"
    class="brand-card brand-card-interactive flex min-h-32 flex-col gap-2 p-[22px] no-underline"
  >
    <span class="brand-h3">{{ title }}</span>
    <span class="brand-prose">{{ body }}</span>
    <span class="mt-auto flex self-end text-brand-primary">
      <IconArrowRight size="22" stroke="2" aria-hidden="true" />
    </span>
  </NuxtLink>
</template>
