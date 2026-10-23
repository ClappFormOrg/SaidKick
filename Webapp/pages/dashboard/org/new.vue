<script setup lang="ts">
import { REGEX_EMAIL } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Organisatie aanmaken' })

const { isPlatformAdmin } = useAuth()
useRoleGuard(isPlatformAdmin)

const { api } = useApi()

const form = reactive({
  slug:          '',
  display_name:  '',
  contact_email: '',
})

const touched = reactive({ slug: false, display_name: false, contact_email: false })

const { run: submit, pending: isSubmitting, error: submitError } = useAsyncAction(async () => {
  const result = await api<{ slug: string }>('/api/v1/orgs', {
    method: 'POST',
    body: {
      slug:          form.slug.trim(),
      display_name:  form.display_name.trim(),
      contact_email: form.contact_email.trim(),
    },
  })
  await navigateTo(`/dashboard/org/${result.slug}`)
}, 'Aanmaken mislukt. Probeer het opnieuw.')

const SLUG_RE = /^[a-z][a-z0-9-]{1,38}[a-z0-9]$/

const slugValid  = computed(() => SLUG_RE.test(form.slug))
const nameValid  = computed(() => form.display_name.trim().length > 0)
const emailValid = computed(() => REGEX_EMAIL.test(form.contact_email.trim()))
const formValid  = computed(() => slugValid.value && nameValid.value && emailValid.value)

async function handleSubmit() {
  Object.keys(touched).forEach(k => (touched[k as keyof typeof touched] = true))
  if (!formValid.value) return
  await submit()
}
</script>

<template>
  <div class="flex flex-col gap-6 max-w-[40rem]">

    <div>
      <h1 class="brand-h1">Organisatie aanmaken</h1>
      <p class="brand-prose mt-2">Alleen beschikbaar voor Platform-beheerders.</p>
    </div>

    <div class="brand-card px-6 py-7">

      <div class="mb-5">
        <label for="org-slug" class="brand-label">
          Slug <span class="text-govnl-rood-700" aria-hidden="true">*</span>
        </label>
        <input
          id="org-slug"
          v-model="form.slug"
          type="text"
          placeholder="bijv. provincie-voorbeeld, gemeente-voorbeeld"
          class="brand-input"
          :class="touched.slug && !slugValid && '!border-govnl-rood-500'"
          @blur="touched.slug = true"
        />
        <p v-if="touched.slug && !slugValid" class="brand-help !text-govnl-rood-700">
          Slug moet voldoen aan <code>{{ '^[a-z][a-z0-9-]{1,38}[a-z0-9]$' }}</code>
        </p>
        <p v-else class="brand-help">
          Alleen kleine letters, cijfers en koppeltekens. Onveranderlijk na aanmaken.
        </p>
      </div>

      <div class="mb-5">
        <label for="org-name" class="brand-label">
          Weergavenaam <span class="text-govnl-rood-700" aria-hidden="true">*</span>
        </label>
        <input
          id="org-name"
          v-model="form.display_name"
          type="text"
          placeholder="bijv. Provincie Voorbeeld"
          class="brand-input"
          :class="touched.display_name && !nameValid && '!border-govnl-rood-500'"
          @blur="touched.display_name = true"
        />
        <p v-if="touched.display_name && !nameValid" class="brand-help !text-govnl-rood-700">
          Weergavenaam is verplicht.
        </p>
      </div>

      <div class="mb-7">
        <label for="org-email" class="brand-label">
          Contacte-mailadres <span class="text-govnl-rood-700" aria-hidden="true">*</span>
        </label>
        <input
          id="org-email"
          v-model="form.contact_email"
          type="email"
          placeholder="it@provincie.nl"
          class="brand-input"
          :class="touched.contact_email && !emailValid && '!border-govnl-rood-500'"
          @blur="touched.contact_email = true"
        />
        <p v-if="touched.contact_email && !emailValid" class="brand-help !text-govnl-rood-700">
          Geldig e-mailadres vereist.
        </p>
      </div>

      <div
        v-if="submitError"
        role="alert"
        class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700 mb-5"
      >
        {{ submitError }}
      </div>

      <div class="flex items-center justify-between gap-4 flex-wrap">
        <NuxtLink to="/dashboard" class="brand-link text-sm">← Annuleren</NuxtLink>
        <button
          type="button"
          :disabled="isSubmitting"
          class="brand-btn brand-btn-primary"
          @click="handleSubmit"
        >
          {{ isSubmitting ? 'Aanmaken…' : 'Organisatie aanmaken' }}
        </button>
      </div>

    </div>

  </div>
</template>
