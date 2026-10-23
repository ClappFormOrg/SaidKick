<script setup lang="ts">
import { IconSearch } from '~/lib/icons'
import { useApps, CATEGORIES, CRITERIA } from '~/composables/useData'
import { ALL_STATUSES, useCatalogueFilters } from '~/composables/useCatalogueFilters'

useHead({ title: 'Catalogus' })

const route = useRoute()
const router = useRouter()

const { data: apps } = await useApps()

// Filtering, sorting and facet counting live in the composable so the
// counting rule is unit-testable.
const {
  selectedCategories, selectedStatuses, selectedCriteria, textQuery, sortOrder,
  filteredApps, sortedApps, categoryCounts, statusCounts,
  hasFilters, clearFilters, toggle,
} = useCatalogueFilters(apps)

onMounted(() => {
  if (route.query.q) textQuery.value = String(route.query.q)
  if (route.query.category) selectedCategories.value = [String(route.query.category)]
})

const toggleCategory  = (slug: string) => toggle(selectedCategories.value, slug)
const toggleStatus    = (status: string) => toggle(selectedStatuses.value, status)
const toggleCriterion = (key: string) => toggle(selectedCriteria.value, key)
</script>

<template>
  <div>
    <!-- Page header -->
    <div class="border-b border-brand-border bg-brand-white">
      <div class="mx-auto max-w-page px-6 py-6">
        <UiBreadcrumb
          class="mb-3"
          :items="[{ label: 'App-store', to: '/' }, { label: 'Catalogus' }]"
        />
        <div class="flex flex-wrap items-baseline gap-4">
          <h1 class="brand-h1 m-0">Catalogus</h1>
          <span class="text-govnl-grijs-600">{{ filteredApps.length }} applicatie{{ filteredApps.length !== 1 ? 's' : '' }}</span>
        </div>
      </div>
    </div>

    <!-- Layout -->
    <div class="mx-auto grid max-w-page grid-cols-1 items-start gap-8 px-6 py-8 lg:grid-cols-[16.25rem_1fr]">
      <!-- Sidebar -->
      <aside aria-label="Filteropties" class="brand-card p-5">
        <!-- Text search -->
        <div class="relative mb-6">
          <IconSearch
            size="18"
            stroke="2"
            class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-govnl-grijs-600"
            aria-hidden="true"
          />
          <input
            v-model="textQuery"
            type="search"
            placeholder="Zoeken..."
            aria-label="Catalogus doorzoeken"
            class="brand-input pl-10"
          />
        </div>

        <!-- Filter: Onderwerp -->
        <fieldset class="m-0 mb-6 border-none p-0">
          <legend class="mb-2.5 p-0 font-bold text-brand-primary">Onderwerp</legend>
          <ul class="m-0 flex list-none flex-col gap-1.5 p-0">
            <li v-for="cat in CATEGORIES" :key="cat.slug">
              <label class="flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  :checked="selectedCategories.includes(cat.slug)"
                  class="h-4 w-4 cursor-pointer accent-brand-primary"
                  @change="toggleCategory(cat.slug)"
                />
                <span class="flex text-brand-primary">
                  <UiCategoryIcon :category="cat.slug" :size="16" :labelled="false" />
                </span>
                <span>{{ cat.title }}</span>
                <span class="ml-auto text-sm text-govnl-grijs-600">{{ categoryCounts[cat.slug] ?? 0 }}</span>
              </label>
            </li>
          </ul>
        </fieldset>

        <!-- Filter: Status -->
        <fieldset class="m-0 mb-6 border-none p-0">
          <legend class="mb-2.5 p-0 font-bold text-brand-primary">Status</legend>
          <ul class="m-0 flex list-none flex-col gap-1.5 p-0">
            <li v-for="status in ALL_STATUSES" :key="status">
              <label class="flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  :checked="selectedStatuses.includes(status)"
                  class="h-4 w-4 cursor-pointer accent-brand-primary"
                  @change="toggleStatus(status)"
                />
                <span>{{ status }}</span>
                <span class="ml-auto text-sm text-govnl-grijs-600">{{ statusCounts[status] ?? 0 }}</span>
              </label>
            </li>
          </ul>
        </fieldset>

        <!-- Filter: Toelatingseisen -->
        <fieldset class="m-0 mb-6 border-none p-0">
          <legend class="mb-2.5 p-0 font-bold text-brand-primary">Toelatingseisen</legend>
          <ul class="m-0 flex list-none flex-col gap-1.5 p-0">
            <li v-for="c in CRITERIA" :key="c.key">
              <label class="flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  :checked="selectedCriteria.includes(c.key)"
                  class="h-4 w-4 cursor-pointer accent-brand-primary"
                  @change="toggleCriterion(c.key)"
                />
                <span>{{ c.label }}</span>
              </label>
            </li>
          </ul>
        </fieldset>

        <GovnlButton v-if="hasFilters" variant="tertiary" @click="clearFilters">
          Filters wissen
        </GovnlButton>
      </aside>

      <!-- Content -->
      <div>
        <!-- Sort bar -->
        <div class="mb-5 flex flex-wrap items-center justify-between gap-x-4 gap-y-3 border-b border-brand-border pb-4">
          <span class="text-govnl-grijs-600" aria-live="polite" aria-atomic="true">
            {{ sortedApps.length }} resultate{{ sortedApps.length !== 1 ? 'n' : '' }}
          </span>
          <div class="flex items-center gap-2">
            <label for="sort-order" class="text-govnl-grijs-700">Sorteren op:</label>
            <select
              id="sort-order"
              v-model="sortOrder"
              class="brand-input min-h-[2.25rem] w-auto py-1"
            >
              <option value="installs">Populairheid</option>
              <option value="name">Naam</option>
              <option value="bijgewerkt">Bijgewerkt</option>
            </select>
          </div>
        </div>

        <!-- Results list -->
        <ul
          v-if="sortedApps.length"
          class="brand-card m-0 list-none overflow-hidden p-0"
        >
          <AppCatalogueRow
            v-for="app in sortedApps"
            :key="app.slug"
            :app="app"
          />
        </ul>

        <div v-else class="brand-card px-6 py-16 text-center">
          <p class="brand-h3 mb-2 mt-0">Geen applicaties gevonden</p>
          <p class="brand-prose">Pas uw filters aan of wis ze om meer resultaten te zien.</p>
        </div>
      </div>
    </div>
  </div>
</template>
