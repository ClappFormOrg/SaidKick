<script setup lang="ts">
import { IconExternalLink } from '~/lib/icons'
import {
  useMyDeployedApps, availabilityLabel, maintainerLink, type MyAppCard,
} from '~/composables/useData'
import { errorMessage } from '~/composables/useApi'

// The apps the signed-in user's organisation runs, each with a link to open
// it or a contact to ask. GET /my-apps needs only a session, so every
// signed-in user may see this page and no role guard applies.
definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Mijn apps' })

const { data, pending, error } = await useMyDeployedApps()

const apps = computed<MyAppCard[]>(() => data.value?.items ?? [])
const loadError = computed(() => (error.value ? errorMessage(error.value, 'Apps laden mislukt.') : ''))

function maintainerName(app: MyAppCard): string {
  return app.maintainer.name || app.maintainer.vendor_name || ''
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <div>
      <h1 class="brand-h1">Mijn apps</h1>
      <p class="brand-prose mt-2">De applicaties die uw organisatie heeft uitgerold.</p>
    </div>

    <div
      v-if="loadError"
      role="alert"
      class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700"
    >{{ loadError }}</div>

    <p v-else-if="pending && !apps.length" role="status" class="text-sm text-govnl-grijs-600">
      Apps worden geladen…
    </p>

    <div v-else-if="!apps.length" class="brand-card px-6 py-12 text-center">
      <h2 class="brand-h3">Nog geen applicaties</h2>
      <p class="brand-prose mx-auto mt-2 mb-5 max-w-prose">
        Uw organisatie heeft nog geen applicaties uitgerold. Zodra dat gebeurt, verschijnen ze hier.
      </p>
      <GovnlButton variant="secondary" size="sm" to="/catalogus">Naar de catalogus</GovnlButton>
    </div>

    <ul v-else class="m-0 grid list-none gap-4 p-0 sm:grid-cols-2 xl:grid-cols-3">
      <li
        v-for="app in apps"
        :key="app.deployment_id"
        class="brand-card flex flex-col gap-3 p-5"
      >
        <div class="flex items-start justify-between gap-3">
          <h2 class="brand-h3 min-w-0 break-words">{{ app.display_name }}</h2>
          <UiStatusPill :status="availabilityLabel(app.availability)" />
        </div>

        <div v-if="app.categories.length" class="flex flex-wrap gap-1.5">
          <UiTag v-for="cat in app.categories" :key="cat">{{ cat }}</UiTag>
        </div>

        <p v-if="app.cluster_name" class="m-0 text-sm text-govnl-grijs-600">Draait op {{ app.cluster_name }}</p>

        <!-- Push the action to the bottom so cards of differing height line up. -->
        <div class="mt-auto pt-1 text-sm text-brand-body">
          <GovnlButton
            v-if="app.url"
            variant="primary"
            size="sm"
            :href="app.url"
            target="_blank"
            rel="noopener"
            :icon-after="IconExternalLink"
          >Openen</GovnlButton>

          <template v-else-if="app.access === 'none'">
            <p class="m-0">Deze app heeft geen webinterface; het is een API of achtergronddienst.</p>
            <p class="m-0 mt-1">
              <template v-if="maintainerLink(app.maintainer, app.display_name)">
                Toegang via
                <a
                  :href="maintainerLink(app.maintainer, app.display_name)"
                  class="brand-link break-all"
                >{{ maintainerName(app) || 'de beheerder' }}</a>.
              </template>
              <template v-else>Neem voor toegang contact op met de beheerder van uw organisatie.</template>
            </p>
          </template>

          <p v-else class="m-0 text-govnl-grijs-600">Nog niet beschikbaar om te openen.</p>
        </div>
      </li>
    </ul>
  </div>
</template>
