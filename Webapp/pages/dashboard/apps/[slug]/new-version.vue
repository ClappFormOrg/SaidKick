<script setup lang="ts">
import { IconGitBranch, IconInfoCircle } from '~/lib/icons'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })

const route = useRoute()
const slug  = route.params.slug as string
const { api } = useApi()
const { isDeveloper } = useAuth()
useRoleGuard(isDeveloper)

const gitRef      = ref('')
const { run: handleSubmit, pending: isSubmitting, error: submitError } = useAsyncAction(async () => {
  await api(`/api/v1/apps/${slug}/versions`, {
    method: 'POST',
    body: { git_ref: gitRef.value.trim() || undefined },
  })
  await navigateTo(`/dashboard/apps/${slug}`)
}, 'Aanmaken mislukt. Probeer het opnieuw.')
</script>

<template>
  <div class="max-w-[720px]">

    <!-- No "Apps" crumb: /dashboard/apps has no index page. -->
    <UiBreadcrumb
      class="mb-6"
      :items="[
        { label: 'Dashboard', to: '/dashboard' },
        { label: slug, to: `/dashboard/apps/${slug}` },
        { label: 'Nieuwe versie' },
      ]"
    />

    <template v-if="isDeveloper">
      <div class="mb-6 flex items-start gap-3">
        <IconGitBranch size="28" stroke="2" class="mt-1 shrink-0 text-brand-primary" aria-hidden="true" />
        <h1 class="brand-h1">Nieuwe versie aanmaken</h1>
      </div>

      <!-- Info banner -->
      <div class="mb-6 flex gap-3 rounded-card border border-govnl-hemelblauw-500 bg-govnl-hemelblauw-50 px-5 py-4">
        <IconInfoCircle size="18" stroke="2" class="mt-0.5 shrink-0 text-govnl-hemelblauw-800" aria-hidden="true" />
        <p class="m-0 text-sm leading-[1.5] text-govnl-hemelblauw-800">
          Het aanmaken van een versie haalt <code class="rounded-card bg-govnl-hemelblauw-100 px-1 py-px">appstore.yaml</code> op uit de repository op het opgegeven
          git-ref en voert automatisch Stage&nbsp;1-validatie uit (SARIF-scan, manifestschema).
        </p>
      </div>

      <!-- Form card -->
      <section class="brand-card px-6 py-7" aria-label="Versiegegevens">
        <!-- Git ref field -->
        <div class="mb-6">
          <label for="git-ref" class="brand-label">
            Git ref
            <span class="font-normal text-govnl-grijs-600">(optioneel)</span>
          </label>
          <input
            id="git-ref"
            v-model="gitRef"
            type="text"
            placeholder="bijv. main, v1.2.0, a3f8c12"
            class="brand-input"
            aria-describedby="git-ref-help"
            @keydown.enter="handleSubmit"
          />
          <p id="git-ref-help" class="brand-help">
            Laat leeg om de standaardbranch van de repository te gebruiken.
            Branch-namen, tags en volledige commit-SHA's zijn toegestaan.
          </p>
        </div>

        <!-- Error -->
        <p
          v-if="submitError"
          role="alert"
          class="mb-5 mt-0 rounded-card border border-govnl-rood-500 bg-govnl-rood-50 px-4 py-3 text-sm text-govnl-rood-700"
        >
          {{ submitError }}
        </p>

        <!-- Actions -->
        <div class="flex flex-wrap items-center justify-between gap-4">
          <NuxtLink :to="`/dashboard/apps/${slug}`" class="brand-link text-sm">← Annuleren</NuxtLink>
          <!-- A plain button: the role-guard tests click only real <button> elements. -->
          <button
            type="button"
            :disabled="isSubmitting"
            class="brand-btn brand-btn-primary"
            @click="handleSubmit"
          >
            {{ isSubmitting ? 'Aanmaken…' : 'Versie aanmaken' }}
          </button>
        </div>
      </section>
    </template>

  </div>
</template>
