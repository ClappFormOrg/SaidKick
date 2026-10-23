<script setup lang="ts">
// The Rijkslint: the national ribbon glyph in a solid block. It appears only
// in the primary colour on white, or in white on the primary colour, never
// over imagery. The glyph is a mask over a filled box, so it takes its colour
// from a token. public/rijkslint.svg is same-origin, which the CSP's
// img-src 'self' allows.
//
// Proportions follow the masthead: a 46x104 block carries a glyph about 28%
// of the block height, about 10% up from the base.
const props = withDefaults(defineProps<{
  width?: number
  height?: number
  // A white block with a primary-coloured glyph, for primary surfaces.
  reverse?: boolean
}>(), {
  width: 36,
  height: 64,
  reverse: false,
})

const GLYPH = 'url("/rijkslint.svg")'

const glyphHeight = computed(() => Math.round(props.height * 0.28))
const glyphInset = computed(() => Math.round(props.height * 0.1))
</script>

<template>
  <div
    :style="{
      display: 'flex',
      alignItems: 'flex-end',
      justifyContent: 'center',
      width: width + 'px',
      height: height + 'px',
      backgroundColor: reverse ? 'var(--govnl-color-wit)' : 'var(--brand-primary)',
      flexShrink: '0',
    }"
  >
    <span
      aria-hidden="true"
      :style="{
        display: 'block',
        height: glyphHeight + 'px',
        width: Math.round(glyphHeight * 1.2) + 'px',
        marginBottom: glyphInset + 'px',
        backgroundColor: reverse ? 'var(--brand-primary)' : 'var(--govnl-color-wit)',
        maskImage: GLYPH,
        WebkitMaskImage: GLYPH,
        maskSize: 'contain',
        WebkitMaskSize: 'contain',
        maskRepeat: 'no-repeat',
        WebkitMaskRepeat: 'no-repeat',
        maskPosition: 'center',
        WebkitMaskPosition: 'center',
      }"
    />
  </div>
</template>
