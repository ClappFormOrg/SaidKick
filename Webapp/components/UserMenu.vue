<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  IconApps, IconBuilding, IconChevronDown, IconLayoutDashboard, IconLogout,
} from '~/lib/icons'

// The signed-in user's menu in the header, so logout is reachable from every
// page. WAI-ARIA menu button: the trigger owns aria-haspopup/aria-expanded,
// the panel is a menu of menuitems, the arrow keys move between them, and
// Escape closes it and returns focus to the trigger.
//
// Renders nothing until useAuth has a profile. The server never knows the
// user and useAuth applies the cached profile only after hydration, so the
// server HTML and the first client render agree.
const { me, initials, effectiveOrgSlug, isAppUserOnly, logout } = useAuth()

const open = ref(false)
const triggerRef = ref<HTMLButtonElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)
const isLoggingOut = ref(false)

function items(): HTMLElement[] {
  return Array.from(panelRef.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])
}

async function openMenu(focus: 'first' | 'last' = 'first') {
  open.value = true
  await nextTick()
  const list = items()
  ;(focus === 'first' ? list[0] : list[list.length - 1])?.focus()
}

function closeMenu(returnFocus = true) {
  open.value = false
  if (returnFocus) nextTick(() => triggerRef.value?.focus())
}

function onTriggerKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    openMenu('first')
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    openMenu('last')
  }
}

function onPanelKeydown(e: KeyboardEvent) {
  const list = items()
  if (!list.length) return
  const idx = list.indexOf(document.activeElement as HTMLElement)
  const focusAt = (i: number) => list[(i + list.length) % list.length]?.focus()

  switch (e.key) {
    case 'Escape':
      e.preventDefault()
      closeMenu()
      break
    case 'ArrowDown':
      e.preventDefault()
      focusAt(idx + 1)
      break
    case 'ArrowUp':
      e.preventDefault()
      focusAt(idx - 1)
      break
    case 'Home':
      e.preventDefault()
      focusAt(0)
      break
    case 'End':
      e.preventDefault()
      focusAt(list.length - 1)
      break
    case 'Tab':
      // Tabbing away dismisses the menu; focus moves on as usual.
      closeMenu(false)
      break
  }
}

function onDocumentPointerDown(e: PointerEvent) {
  if (!open.value) return
  const target = e.target as Node
  if (triggerRef.value?.contains(target) || panelRef.value?.contains(target)) return
  closeMenu(false)
}

onMounted(() => document.addEventListener('pointerdown', onDocumentPointerDown))
onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocumentPointerDown))

async function handleLogout() {
  if (isLoggingOut.value) return
  isLoggingOut.value = true
  try {
    await logout()
  } finally {
    isLoggingOut.value = false
    open.value = false
  }
}
</script>

<template>
  <div v-if="me" class="relative">
    <button
      ref="triggerRef"
      type="button"
      aria-haspopup="menu"
      :aria-expanded="open ? 'true' : 'false'"
      aria-controls="user-menu-panel"
      class="inline-flex cursor-pointer items-center gap-2.5 rounded-full border border-brand-border bg-brand-surface px-3.5 py-[7px] text-brand-primary hover:bg-govnl-grijs-200"
      @click="open ? closeMenu(false) : openMenu()"
      @keydown="onTriggerKeydown"
    >
      <span
        aria-hidden="true"
        class="inline-flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-full bg-brand-primary text-[0.625rem] font-bold text-brand-white"
      >{{ initials }}</span>
      <span class="max-w-[10rem] truncate">{{ me.name }}</span>
      <IconChevronDown
        size="16"
        stroke="2"
        aria-hidden="true"
        class="transition-transform duration-150"
        :class="open && 'rotate-180'"
      />
    </button>

    <div
      v-if="open"
      id="user-menu-panel"
      ref="panelRef"
      role="menu"
      :aria-label="`Menu van ${me.name}`"
      class="absolute right-0 z-50 mt-2 w-64 rounded-card border border-brand-border bg-brand-white py-2 shadow-elev-2"
      @keydown="onPanelKeydown"
    >
      <p class="m-0 border-b border-brand-border px-4 pb-2 pt-1">
        <span class="block truncate font-bold text-brand-primary">{{ me.name }}</span>
        <span class="block truncate text-sm text-govnl-grijs-600">{{ me.email }}</span>
      </p>

      <p
        v-if="me.org_name || effectiveOrgSlug"
        class="m-0 flex items-center gap-2 px-4 py-2 text-sm text-govnl-grijs-700"
      >
        <IconBuilding size="16" stroke="2" aria-hidden="true" />
        <span class="truncate">{{ me.org_name || effectiveOrgSlug }}</span>
      </p>

      <NuxtLink
        v-if="!isAppUserOnly"
        to="/dashboard"
        role="menuitem"
        class="flex items-center gap-2.5 px-4 py-2.5 text-brand-body no-underline hover:bg-brand-surface"
        @click="closeMenu(false)"
      >
        <IconLayoutDashboard size="18" stroke="2" aria-hidden="true" />
        Mijn dashboard
      </NuxtLink>

      <NuxtLink
        to="/dashboard/my-apps"
        role="menuitem"
        class="flex items-center gap-2.5 px-4 py-2.5 text-brand-body no-underline hover:bg-brand-surface"
        @click="closeMenu(false)"
      >
        <IconApps size="18" stroke="2" aria-hidden="true" />
        Mijn apps
      </NuxtLink>

      <button
        type="button"
        role="menuitem"
        :disabled="isLoggingOut"
        class="flex w-full cursor-pointer items-center gap-2.5 border-0 bg-transparent px-4 py-2.5 text-left text-base text-brand-body hover:bg-brand-surface disabled:cursor-not-allowed disabled:opacity-50"
        @click="handleLogout"
      >
        <IconLogout size="18" stroke="2" aria-hidden="true" />
        {{ isLoggingOut ? 'Uitloggen…' : 'Uitloggen' }}
      </button>
    </div>
  </div>
</template>
