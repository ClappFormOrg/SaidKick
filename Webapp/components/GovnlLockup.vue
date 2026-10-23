<script setup lang="ts">
withDefaults(defineProps<{
  width?: number
  height?: number
  reverse?: boolean
  title?: string
  subtitle?: string
}>(), {
  width: 36,
  height: 64,
  reverse: false,
  title: 'AI-Appstore',
  subtitle: 'AI-applicaties voor de overheid',
})
</script>

<template>
  <div :style="{ display: 'flex', alignItems: 'center', gap: '12px', textDecoration: 'none' }">
    <GovnlRijkslint :width="width" :height="height" :reverse="reverse" />
    <div :style="{ display: 'flex', flexDirection: 'column', gap: '2px' }">
      <span
        :style="{
          fontFamily: 'inherit',
          fontSize: '26px',
          fontWeight: '700',
          lineHeight: '1.1',
          color: reverse ? 'var(--govnl-color-wit)' : 'var(--brand-primary)',
        }"
      >{{ title }}</span>
      <span
        :style="{
          fontFamily: 'var(--govnl-font-family-secondary)',
          fontSize: '14px',
          fontWeight: '400',
          color: reverse ? 'rgba(255,255,255,0.75)' : 'var(--govnl-color-grijs-600)',
          lineHeight: '1.3',
        }"
      >{{ subtitle }}</span>
    </div>
  </div>
</template>
