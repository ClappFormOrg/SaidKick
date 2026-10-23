<script setup lang="ts">
import type { Component } from 'vue'
import {
  IconApps, IconBuilding, IconClipboardCheck, IconKey, IconLayoutDashboard, IconPalette, IconServer2,
} from '~/lib/icons'

// Dashboard navigation: a bordered white card with a 4px CTA rail on the
// active entry. Inactive entries keep a transparent rail of the same width, so
// nothing shifts when the active entry changes.
//
// Each entry shows only to the roles the API route table admits for what the
// page does (API/internal/handlers/handlers.go), through the plain-membership
// helpers in useAuth. The server is still the gate; this keeps the sidebar
// free of entries that would bounce the user straight back.
interface SidebarItem {
  to: string
  label: string
  icon: Component
  visible: boolean
}

withDefaults(defineProps<{
  title?: string
  // Optional count badges keyed by route, fed by the page that owns the data.
  counts?: Record<string, number>
}>(), {
  title: 'Mijn AI-Appstore',
  counts: () => ({}),
})

const route = useRoute()
const {
  me, isDeveloper, isDeployer, isClusterMgr, isAuditor, isOrgAdmin, isPlatformAdmin, isAppUserOnly,
  effectiveOrgSlug,
} = useAuth()

const items = computed<SidebarItem[]>(() => {
  const signedIn = Boolean(me.value)
  return [
    // A user holding only app_user is redirected from /dashboard to the
    // launcher, so the entry would lead nowhere for them.
    { to: '/dashboard', label: 'Mijn dashboard', icon: IconLayoutDashboard, visible: signedIn && !isAppUserOnly.value },
    // GET /my-apps needs only a session.
    { to: '/dashboard/my-apps', label: 'Mijn apps', icon: IconApps, visible: signedIn },
    // Cluster writes: cluster_admin or platform_admin.
    { to: '/dashboard/clusters', label: 'Clusters', icon: IconServer2, visible: isClusterMgr.value },
    // /audit-submissions: auditor.
    { to: '/dashboard/audit', label: 'Beoordelingen', icon: IconClipboardCheck, visible: isAuditor.value },
    // POST /api-keys: developer, deployer or auditor.
    {
      to: '/dashboard/api-keys',
      label: 'API-sleutels',
      icon: IconKey,
      visible: isDeveloper.value || isDeployer.value || isAuditor.value,
    },
    // /orgs/:slug/members: org_admin or platform_admin, for the user's own org.
    {
      to: `/dashboard/org/${effectiveOrgSlug.value}`,
      label: 'Organisatie',
      icon: IconBuilding,
      visible: isOrgAdmin.value && Boolean(effectiveOrgSlug.value),
    },
    // PUT /branding: platform_admin.
    { to: '/dashboard/branding', label: 'Huisstijl', icon: IconPalette, visible: isPlatformAdmin.value },
  ]
})

const visibleItems = computed(() => items.value.filter(i => i.visible))

// /dashboard matches only itself; the other entries match their subtree.
function isActive(to: string): boolean {
  if (to === '/dashboard') return route.path === '/dashboard'
  return route.path === to || route.path.startsWith(`${to}/`)
}
</script>

<template>
  <nav
    :aria-label="title"
    class="brand-card w-full shrink-0 self-start py-4 lg:w-[280px]"
  >
    <p class="m-0 px-[18px] pb-3 font-bold text-brand-primary">{{ title }}</p>
    <ul class="m-0 list-none p-0">
      <li v-for="item in visibleItems" :key="item.to">
        <NuxtLink
          :to="item.to"
          :aria-current="isActive(item.to) ? 'page' : undefined"
          class="flex items-center gap-3 border-0 border-l-4 border-solid px-[18px] py-[11px] no-underline"
          :class="isActive(item.to)
            ? 'border-brand-cta bg-brand-surface font-bold text-brand-primary'
            : 'border-transparent font-normal text-brand-body hover:bg-govnl-grijs-50'"
        >
          <component
            :is="item.icon"
            :size="20"
            :stroke="2"
            aria-hidden="true"
            :class="isActive(item.to) ? 'text-brand-primary' : 'text-govnl-grijs-600'"
          />
          <span>{{ item.label }}</span>
          <span
            v-if="counts[item.to]"
            class="ml-auto inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-brand-cta px-1.5 text-[0.75rem] font-bold text-brand-white"
          >{{ counts[item.to] }}</span>
        </NuxtLink>
      </li>
    </ul>
  </nav>
</template>
