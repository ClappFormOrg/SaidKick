<script setup lang="ts">
import {
  IconArrowRight, IconBrandGithub, IconMessageCircle, IconRefresh, IconThumbUp,
} from '~/lib/icons'

useHead({ title: 'Community' })

const { publicApi } = useApi()

interface Discussion {
  id: string
  number: number
  title: string
  url: string
  author: string
  author_avatar: string
  category: string
  created_at: string
  updated_at: string
  comment_count: number
  upvote_count: number
  excerpt: string
}

const discussions  = ref<Discussion[]>([])
const isLoading    = ref(true)
const fetchFailed  = ref(false)
// The API answers configured:false when this instance has no community
// repository set up. That is a configuration state, not a failure.
const notConfigured = ref(false)
const activeCategory = ref('')

// Re-callable so the error state can offer a retry.
async function loadDiscussions() {
  isLoading.value = true
  fetchFailed.value = false
  notConfigured.value = false
  try {
    const data = await publicApi<{ items: Discussion[]; configured?: boolean }>('/api/v1/community/discussions', {
      query: { page_size: 30 },
    })
    discussions.value = data.items ?? []
    notConfigured.value = data.configured === false
  } catch {
    fetchFailed.value = true
  } finally {
    isLoading.value = false
  }
}

onMounted(loadDiscussions)

const categories = computed(() => {
  const seen = new Set<string>()
  for (const d of discussions.value) {
    if (d.category) seen.add(d.category)
  }
  return Array.from(seen).sort()
})

const filtered = computed(() => {
  if (!activeCategory.value) return discussions.value
  return discussions.value.filter(d => d.category === activeCategory.value)
})

function relativeDate(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const minutes = Math.floor(diff / 60_000)
  if (minutes < 1)   return 'zojuist'
  if (minutes < 60)  return `${minutes} min geleden`
  const hours = Math.floor(minutes / 60)
  if (hours < 24)    return `${hours} uur geleden`
  const days = Math.floor(hours / 24)
  if (days < 30)     return `${days} dag${days !== 1 ? 'en' : ''} geleden`
  const months = Math.floor(days / 30)
  if (months < 12)   return `${months} maand${months !== 1 ? 'en' : ''} geleden`
  return `${Math.floor(months / 12)} jaar geleden`
}
</script>

<template>
  <div>
    <div class="border-b border-brand-border bg-brand-white">
      <div class="mx-auto max-w-page px-6 py-6">
        <UiBreadcrumb
          class="mb-3"
          :items="[{ label: 'Home', to: '/' }, { label: 'Community' }]"
        />
        <UiSectionHead
          level="h1"
          as="h1"
          title="Community"
          lead="Discussies, ideeën en vragen van overheden die sAId-Kick gebruiken."
          class="mb-0"
        >
          <template #action>
            <GovnlButton
              variant="secondary"
              :icon="IconBrandGithub"
              href="https://github.com/ClappformOrg/saidkick-appstore/discussions"
              target="_blank"
              rel="noopener"
            >
              Discussie starten
            </GovnlButton>
          </template>
        </UiSectionHead>
      </div>
    </div>

    <div class="bg-brand-surface">
      <div class="mx-auto max-w-[54rem] px-6 py-10">
        <div
          v-if="!isLoading && !fetchFailed && categories.length"
          role="group"
          aria-label="Filter op categorie"
          class="mb-6 flex flex-wrap gap-2"
        >
          <button
            type="button"
            :aria-pressed="activeCategory === ''"
            class="cursor-pointer rounded-full border px-3.5 py-1.5 text-sm font-semibold transition-colors"
            :class="activeCategory === ''
              ? 'border-brand-primary bg-brand-primary text-brand-white'
              : 'border-brand-border bg-brand-white text-brand-body hover:bg-govnl-grijs-100'"
            @click="activeCategory = ''"
          >Alles</button>
          <button
            v-for="cat in categories"
            :key="cat"
            type="button"
            :aria-pressed="activeCategory === cat"
            class="cursor-pointer rounded-full border px-3.5 py-1.5 text-sm font-semibold transition-colors"
            :class="activeCategory === cat
              ? 'border-brand-primary bg-brand-primary text-brand-white'
              : 'border-brand-border bg-brand-white text-brand-body hover:bg-govnl-grijs-100'"
            @click="activeCategory = cat"
          >{{ cat }}</button>
        </div>

        <div v-if="isLoading" class="flex flex-col gap-3" aria-busy="true">
          <span class="sr-only">Discussies worden geladen</span>
          <div v-for="i in 4" :key="i" class="brand-card animate-pulse p-5" aria-hidden="true">
            <div class="mb-3 flex items-center gap-2">
              <div class="h-7 w-7 rounded-full bg-govnl-grijs-200" />
              <div class="h-3 w-24 rounded bg-govnl-grijs-200" />
              <div class="ml-auto h-3 w-16 rounded bg-govnl-grijs-100" />
            </div>
            <div class="mb-2 h-4 w-3/4 rounded bg-govnl-grijs-200" />
            <div class="mb-1 h-3 w-full rounded bg-govnl-grijs-100" />
            <div class="h-3 w-2/3 rounded bg-govnl-grijs-100" />
          </div>
        </div>

        <UiAlert v-else-if="fetchFailed" tone="negative" title="Kan discussies niet ophalen">
          <p class="m-0">De community-discussies zijn tijdelijk niet beschikbaar.</p>
          <p class="mb-0 mt-3">
            <GovnlButton variant="secondary" size="sm" :icon="IconRefresh" @click="loadDiscussions">Opnieuw proberen</GovnlButton>
          </p>
        </UiAlert>

        <div v-else-if="notConfigured" class="brand-card px-6 py-16 text-center">
          <p class="brand-h3 mb-2 mt-0">Community nog niet ingericht</p>
          <p class="brand-prose m-0">
            Voor deze omgeving is nog geen community-repository ingericht.
          </p>
        </div>

        <div v-else-if="filtered.length === 0" class="brand-card px-6 py-16 text-center">
          <p class="brand-h3 m-0">Geen discussies gevonden.</p>
        </div>

        <ul v-else class="m-0 flex list-none flex-col gap-3 p-0">
          <li v-for="d in filtered" :key="d.id">
            <a
              :href="d.url"
              target="_blank"
              rel="noopener"
              class="brand-card brand-card-interactive block p-5 no-underline"
            >
              <div class="mb-2.5 flex flex-wrap items-center gap-2.5">
                <img
                  v-if="d.author_avatar"
                  :src="d.author_avatar"
                  :alt="d.author"
                  class="h-6 w-6 shrink-0 rounded-full"
                  loading="lazy"
                />
                <span class="text-sm font-semibold text-govnl-grijs-700">{{ d.author }}</span>
                <UiTag v-if="d.category">{{ d.category }}</UiTag>
                <span class="ml-auto shrink-0 text-sm text-govnl-grijs-600">
                  {{ relativeDate(d.updated_at) }}
                </span>
              </div>

              <h2 class="brand-h3 mb-2 mt-0 leading-snug">{{ d.title }}</h2>

              <p v-if="d.excerpt" class="mb-3 mt-0 line-clamp-2 text-sm leading-relaxed text-govnl-grijs-700">
                {{ d.excerpt }}
              </p>

              <div class="flex items-center gap-4 text-sm text-govnl-grijs-600">
                <span class="flex items-center gap-1.5">
                  <IconMessageCircle size="15" stroke="2" aria-hidden="true" />
                  {{ d.comment_count }}<span class="sr-only"> reacties</span>
                </span>
                <span class="flex items-center gap-1.5">
                  <IconThumbUp size="15" stroke="2" aria-hidden="true" />
                  {{ d.upvote_count }}<span class="sr-only"> stemmen</span>
                </span>
                <span class="ml-auto flex items-center gap-1.5 font-semibold text-brand-primary">
                  Bekijken op GitHub
                  <IconArrowRight size="15" stroke="2" aria-hidden="true" />
                  <span class="sr-only">(opent in nieuw tabblad)</span>
                </span>
              </div>
            </a>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>
