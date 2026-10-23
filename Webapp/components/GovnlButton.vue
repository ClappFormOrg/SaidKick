<script setup lang="ts">
import { resolveComponent, type Component } from 'vue'

// The app's button. The variant states the intent, and the intent sets the
// colour, so the CTA colour stays reserved for the main action:
//
//   primary   CTA fill. The one most important action in a view.
//   navy      Primary-colour fill. A strong action that is not the main one.
//   secondary White with a primary-colour edge. Actions beside a primary.
//   tertiary  Link style. Inline and low-stakes.
//   ghost     White outline, for primary-coloured surfaces only.
//   danger    Destructive actions only.
//
// The look comes from the brand-btn classes in tailwind.config.ts, which a
// plain <button type="submit"> in a form can use as well. They read the
// --brand-* variables, so a saved brand recolours every button.
const props = withDefaults(defineProps<{
  variant?: 'primary' | 'navy' | 'secondary' | 'tertiary' | 'ghost' | 'danger'
  size?: 'sm' | 'md' | 'lg'
  // Icon components, e.g. IconBrandGithub from ~/lib/icons.
  icon?: Component
  iconAfter?: Component
  // Internal route, rendered as a NuxtLink.
  to?: string
  // External URL or mailto:, rendered as a plain anchor.
  href?: string
  disabled?: boolean
}>(), {
  variant: 'primary',
  size: 'md',
  disabled: false,
})

const emit = defineEmits<{ click: [e: MouseEvent] }>()

const VARIANT_CLASSES: Record<string, string> = {
  primary: 'brand-btn-primary',
  navy: 'brand-btn-navy',
  secondary: 'brand-btn-secondary',
  tertiary: 'brand-btn-link',
  ghost: 'brand-btn-ghost',
  danger: 'brand-btn-danger',
}

const SIZE_CLASSES: Record<string, string> = { sm: 'brand-btn-sm', md: '', lg: 'brand-btn-lg' }

// The link-style variant takes no size padding.
const classes = computed(() => [
  'brand-btn',
  VARIANT_CLASSES[props.variant],
  props.variant === 'tertiary' ? '' : SIZE_CLASSES[props.size],
])

const iconSize = computed(() => (props.size === 'sm' ? 16 : props.size === 'lg' ? 22 : 18))

const isLink = computed(() => Boolean(props.to || props.href))

// A disabled link renders as a bare <a> without href: NuxtLink needs a
// destination, and an anchor without href is not navigable.
const tag = computed<string | Component>(() => {
  if (props.to && !props.disabled) return resolveComponent('NuxtLink') as Component
  return isLink.value ? 'a' : 'button'
})

// Attributes for the element tag renders: a destination for links, and the
// disabled state in the form that element supports.
const attrs = computed(() => {
  if (props.to && !props.disabled) return { to: props.to }
  if (isLink.value) {
    return props.disabled
      ? { 'aria-disabled': 'true', tabindex: -1 }
      : { href: props.href }
  }
  return { type: 'button', disabled: props.disabled || undefined }
})

function handleClick(e: MouseEvent) {
  if (props.disabled) {
    e.preventDefault()
    return
  }
  emit('click', e)
}
</script>

<template>
  <component
    :is="tag"
    v-bind="attrs"
    :class="classes"
    @click="handleClick"
  >
    <component :is="icon" v-if="icon" :size="iconSize" :stroke="2" aria-hidden="true" />
    <slot />
    <component :is="iconAfter" v-if="iconAfter" :size="iconSize" :stroke="2" aria-hidden="true" />
  </component>
</template>
