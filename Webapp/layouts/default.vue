<script setup lang="ts">
import {
  IconChevronRight, IconLogout, IconMenu2, IconSearch, IconUser, IconX,
} from '~/lib/icons'

// Public shell: the Rijksoverheid masthead and one application header inside
// a single <header>, then the page and a primary-coloured footer. Signed-in
// pages under /dashboard nest this layout in layouts/dashboard.vue.

const router = useRouter()
const route = useRoute()

const searchQuery = ref('')
const mobileMenuOpen = ref(false)
const mobileMenuButtonRef = ref<HTMLButtonElement | null>(null)
const mobileDrawerRef = ref<HTMLDivElement | null>(null)

function submitSearch() {
  mobileMenuOpen.value = false
  if (searchQuery.value.trim()) {
    router.push(`/catalogus?q=${encodeURIComponent(searchQuery.value.trim())}`)
  } else {
    router.push('/catalogus')
  }
}

async function openMobileMenu() {
  mobileMenuOpen.value = true
  await nextTick()
  const focusable = mobileDrawerRef.value?.querySelector<HTMLElement>('a[href], button:not([disabled])')
  focusable?.focus()
}

function closeMobileMenu() {
  mobileMenuOpen.value = false
  nextTick(() => mobileMenuButtonRef.value?.focus())
}

function trapFocus(e: KeyboardEvent) {
  if (e.key !== 'Tab' || !mobileDrawerRef.value) return
  const focusable = Array.from(
    mobileDrawerRef.value.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input, [tabindex]:not([tabindex="-1"])',
    ),
  ).filter(el => !el.closest('[hidden]'))
  if (!focusable.length) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}

const navItems = [
  { label: 'Home',             path: '/' },
  { label: 'Catalogus',        path: '/catalogus' },
  { label: 'Toelatingseisen',  path: '/toelating' },
  { label: 'Community',        path: '/community' },
]

function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}

// Every link needs a real destination. Entries without one are declared with
// `to: null` and filtered out of the render rather than pointed at "#" or at
// an unrelated page: the privacy and accessibility statements are published
// documents, and choosing their targets is not a front-end decision. To
// publish one, give it a `to` and it appears.
interface FooterLink {
  label: string
  to: string | null
}

const SUPPORT_MAIL = 'mailto:appstore@saidkick.nl'

const footerColumns: { heading: string; links: FooterLink[] }[] = [
  {
    heading: 'Over de App-store',
    links: [
      { label: 'Over ons',          to: null },
      { label: 'Hoe het werkt',     to: '/toelating' },
      { label: 'Nieuws',            to: null },
      { label: 'Privacyverklaring', to: null },
    ],
  },
  {
    heading: 'Catalogus',
    links: [
      { label: 'Alle applicaties', to: '/catalogus' },
      { label: 'Agents',           to: '/catalogus?category=agent' },
      { label: 'Chatbots',         to: '/catalogus?category=chatbot' },
      { label: 'RAG & zoeken',     to: '/catalogus?category=rag' },
    ],
  },
  {
    heading: 'Toelatingseisen',
    links: [
      { label: 'DPIA-vereisten', to: '/toelating' },
      { label: 'BIO-framework',  to: '/toelating' },
      { label: 'EU AI Act',      to: '/toelating' },
      { label: 'WCAG 2.2',       to: '/toelating' },
    ],
  },
  {
    heading: 'Contact',
    links: [
      { label: 'Servicedesk',          to: SUPPORT_MAIL },
      { label: 'App aanmelden',        to: '/dashboard/apps/new' },
      { label: 'Community',            to: '/community' },
      { label: 'appstore@saidkick.nl', to: SUPPORT_MAIL },
    ],
  },
]

const legalLinks: FooterLink[] = [
  { label: 'Toegankelijkheidsverklaring', to: null },
  { label: 'Cookies',                     to: null },
]

// Only the columns that still have something to show, with dead entries removed.
const visibleFooterColumns = computed(() =>
  footerColumns
    .map(col => ({ ...col, links: col.links.filter(l => l.to) }))
    .filter(col => col.links.length > 0),
)
const visibleLegalLinks = computed(() => legalLinks.filter(l => l.to))


const { me, fetchMe, isAppUserOnly, logout } = useAuth()

const accountLink = computed(() => {
  if (!me.value) return { to: '/inloggen', label: 'Inloggen' }
  if (isAppUserOnly.value) return { to: '/dashboard/my-apps', label: `Mijn apps (${me.value.name})` }
  return { to: '/dashboard', label: `Mijn dashboard (${me.value.name})` }
})

async function handleMobileLogout() {
  mobileMenuOpen.value = false
  await logout()
}

onMounted(fetchMe)
</script>

<template>
  <a
    href="#main-content"
    class="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-[100] focus:rounded-card focus:bg-brand-primary focus:px-4 focus:py-2 focus:font-semibold focus:text-brand-white"
  >Ga naar hoofdinhoud</a>

  <div class="flex min-h-screen flex-col">
    <header>
      <!-- The Rijkslint lockup, or the uploaded instance logo in its place
           (#158). -->
      <RijksMasthead />

      <div class="border-b border-brand-border bg-brand-white">
        <div class="mx-auto flex max-w-page items-center gap-6 px-6 py-2 lg:min-h-[60px] lg:py-0">
          <NuxtLink
            to="/"
            class="shrink-0 text-[1.1875rem] font-bold tracking-[-0.01em] text-brand-cta no-underline"
          >sAId-Kick Appstore</NuxtLink>

          <nav class="hidden items-stretch self-stretch lg:flex" aria-label="Hoofdnavigatie">
            <NuxtLink
              v-for="item in navItems"
              :key="item.path"
              :to="item.path"
              :aria-current="isActive(item.path) ? 'page' : undefined"
              class="inline-flex items-center border-b-[3px] px-3.5 text-brand-primary no-underline"
              :class="isActive(item.path)
                ? 'border-brand-cta font-bold'
                : 'border-transparent font-normal hover:border-brand-border'"
            >{{ item.label }}</NuxtLink>
          </nav>

          <div class="ml-auto flex items-center gap-4">
            <form
              class="hidden w-[15rem] md:flex"
              role="search"
              aria-label="Catalogus doorzoeken"
              @submit.prevent="submitSearch"
            >
              <input
                v-model="searchQuery"
                type="search"
                placeholder="Zoek in de catalogus"
                aria-label="Zoek in de catalogus"
                class="brand-input min-h-[2.25rem] rounded-r-none border-r-0 text-sm"
              />
              <button
                type="submit"
                aria-label="Zoeken"
                class="flex min-h-[2.25rem] cursor-pointer items-center rounded-r-card border border-brand-primary bg-brand-primary px-3 text-brand-white hover:bg-brand-primary-hover"
              >
                <IconSearch size="18" stroke="2" aria-hidden="true" />
              </button>
            </form>

            <UserMenu v-if="me" class="hidden sm:block" />

            <NuxtLink
              v-else
              to="/inloggen"
              class="hidden items-center gap-2 text-brand-primary no-underline hover:underline sm:inline-flex"
            >
              <IconUser size="18" stroke="2" aria-hidden="true" />
              Inloggen
            </NuxtLink>

            <button
              ref="mobileMenuButtonRef"
              type="button"
              class="flex h-10 w-10 cursor-pointer items-center justify-center rounded-card bg-transparent text-brand-primary hover:bg-brand-surface lg:hidden"
              :aria-label="mobileMenuOpen ? 'Sluit menu' : 'Open menu'"
              :aria-expanded="mobileMenuOpen"
              aria-controls="mobile-nav-drawer"
              @click="mobileMenuOpen ? closeMobileMenu() : openMobileMenu()"
            >
              <IconX v-if="mobileMenuOpen" size="22" aria-hidden="true" />
              <IconMenu2 v-else size="22" aria-hidden="true" />
            </button>
          </div>
        </div>
      </div>
    </header>

    <!-- Mobile drawer -->
    <div
      v-if="mobileMenuOpen"
      id="mobile-nav-drawer"
      ref="mobileDrawerRef"
      role="dialog"
      aria-modal="true"
      aria-label="Hoofdmenu"
      class="z-50 border-b border-brand-border bg-brand-white shadow-elev-2 lg:hidden"
      @keydown.escape="closeMobileMenu"
      @keydown="trapFocus"
    >
      <form
        class="flex px-6 py-4"
        role="search"
        aria-label="Catalogus doorzoeken"
        @submit.prevent="submitSearch"
      >
        <input
          v-model="searchQuery"
          type="search"
          placeholder="Zoek in de catalogus"
          aria-label="Zoek in de catalogus"
          class="brand-input rounded-r-none border-r-0"
        />
        <button
          type="submit"
          aria-label="Zoeken"
          class="flex cursor-pointer items-center rounded-r-card border border-brand-primary bg-brand-primary px-4 text-brand-white"
        >
          <IconSearch size="18" stroke="2" aria-hidden="true" />
        </button>
      </form>

      <nav class="flex flex-col pb-2" aria-label="Hoofdnavigatie">
        <NuxtLink
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          :aria-current="isActive(item.path) ? 'page' : undefined"
          class="border-l-4 px-6 py-3.5 no-underline"
          :class="isActive(item.path)
            ? 'border-brand-cta bg-brand-surface font-bold text-brand-primary'
            : 'border-transparent font-normal text-brand-body hover:bg-govnl-grijs-50'"
          @click="closeMobileMenu"
        >{{ item.label }}</NuxtLink>

        <NuxtLink
          :to="accountLink.to"
          class="border-l-4 border-transparent px-6 py-3.5 font-semibold text-brand-primary no-underline hover:bg-govnl-grijs-50"
          @click="closeMobileMenu"
        >{{ accountLink.label }}</NuxtLink>

        <button
          v-if="me"
          type="button"
          class="flex w-full cursor-pointer items-center gap-2.5 border-l-4 border-transparent bg-transparent px-6 py-3.5 text-left text-base text-brand-body hover:bg-govnl-grijs-50"
          @click="handleMobileLogout"
        >
          <IconLogout size="18" stroke="2" aria-hidden="true" />
          Uitloggen
        </button>
      </nav>
    </div>

    <main id="main-content" tabindex="-1" class="flex-1">
      <slot />
    </main>

    <!-- One 40px corner cut, top left. -->
    <footer class="mt-16 rounded-tl-cut bg-brand-primary text-brand-white">
      <div class="mx-auto max-w-page px-6 pt-12">
        <div class="grid grid-cols-1 gap-8 pb-11 sm:grid-cols-2 lg:grid-cols-4">
          <div v-for="col in visibleFooterColumns" :key="col.heading">
            <h2 class="m-0 mb-[18px] text-h3 text-brand-white">{{ col.heading }}</h2>
            <nav :aria-label="col.heading">
              <ul class="m-0 flex list-none flex-col gap-2.5 p-0">
                <li v-for="link in col.links" :key="link.label">
                  <NuxtLink
                    :to="link.to!"
                    class="inline-flex items-center gap-2 text-brand-white no-underline hover:underline"
                  >
                    <IconChevronRight size="15" stroke="2.5" aria-hidden="true" />
                    {{ link.label }}
                  </NuxtLink>
                </li>
              </ul>
            </nav>
          </div>
        </div>

        <p class="m-0 border-t border-white/20 py-6 italic">
          Voorkom dat iedere organisatie opnieuw het wiel uitvindt.
        </p>

        <div class="flex flex-col items-start justify-between gap-2 pb-8 text-sm text-white/80 sm:flex-row sm:items-center">
          <span>Versie 1.0.0-beta &mdash; &copy; 2026 sAId-Kick</span>
          <div v-if="visibleLegalLinks.length" class="flex gap-4">
            <NuxtLink
              v-for="link in visibleLegalLinks"
              :key="link.label"
              :to="link.to!"
              class="text-white/80 no-underline hover:text-brand-white hover:underline"
            >{{ link.label }}</NuxtLink>
          </div>
        </div>
      </div>
    </footer>
  </div>
</template>
