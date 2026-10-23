<script setup lang="ts">
import {
  IconArrowRight, IconBrandGithub, IconInfoCircle,
  IconCircleNumber1, IconCircleNumber2, IconCircleNumber3, IconCircleNumber4,
  IconDownload,
} from '~/lib/icons'
import type { Component } from 'vue'

const howToSteps: { icon: Component; title: string; body: string }[] = [
  {
    icon: IconCircleNumber1,
    title: 'Maak uw repository klaar',
    body: 'Zorg dat uw GitHub-repository een geldig appstore.yaml-manifest bevat, een Helm-chart en alle audit-bestanden.',
  },
  {
    icon: IconCircleNumber2,
    title: 'Installeer de GitHub App',
    body: 'Installeer de sAId-Kick GitHub App op uw repository zodat de Appstore toegang heeft om bestanden op te halen.',
  },
  {
    icon: IconCircleNumber3,
    title: 'Vul dit formulier in',
    body: 'Kopieer het installation ID uit de URL van uw GitHub App-installatie en vul de repository-gegevens in.',
  },
  {
    icon: IconCircleNumber4,
    title: 'Dien in voor beoordeling',
    body: 'Na registratie verschijnt uw app in het dashboard. Klik op "Indienen voor beoordeling" om de auditprocedure te starten.',
  },
]

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'App registreren' })

const { api } = useApi()
const router = useRouter()
const { isDeveloper } = useAuth()
useRoleGuard(isDeveloper)

const form = reactive({
  github_installation_id: '',
  repo_owner: '',
  repo_name: '',
  branch: '',
})
const touched = reactive({
  github_installation_id: false,
  repo_owner: false,
  repo_name: false,
})

const {
  run: submit, pending: isSubmitting, error: submitError, fieldErrors: submitFieldErrors,
} = useAsyncAction(async () => {
  const result = await api<{ slug: string; id: string }>('/api/v1/apps', {
    method: 'POST',
    body: {
      github_installation_id: Number(form.github_installation_id),
      repo_owner:             form.repo_owner.trim(),
      repo_name:              form.repo_name.trim(),
      ...(form.branch.trim() ? { branch: form.branch.trim() } : {}),
    },
  })
  router.push(`/dashboard/apps/${result.slug}`)
}, 'Er is een fout opgetreden. Probeer het opnieuw.')

const installationIdValid = computed(
  () => form.github_installation_id !== '' && Number(form.github_installation_id) > 0,
)
const repoOwnerValid = computed(() => form.repo_owner.trim() !== '')
const repoNameValid  = computed(() => form.repo_name.trim() !== '')
const formValid = computed(() => installationIdValid.value && repoOwnerValid.value && repoNameValid.value)

async function handleSubmit() {
  touched.github_installation_id = true
  touched.repo_owner = true
  touched.repo_name = true
  if (!formValid.value) return
  await submit()
}
</script>

<template>
  <div>
    <UiBreadcrumb
      class="mb-6"
      :items="[{ label: 'Dashboard', to: '/dashboard' }, { label: 'App toevoegen' }]"
    />

    <template v-if="isDeveloper">
      <div class="mb-8 flex items-start gap-3">
        <IconBrandGithub size="28" stroke="2" class="mt-1 shrink-0 text-brand-primary" aria-hidden="true" />
        <div>
          <h1 class="brand-h1">App registreren</h1>
          <p class="brand-lead mt-1">Koppel uw GitHub-repository aan de AI-Appstore</p>
        </div>
      </div>

      <div class="grid grid-cols-1 items-start gap-6 lg:grid-cols-[1fr_320px]">
        <!-- Form card -->
        <section class="brand-card px-6 py-7 sm:px-8" aria-label="Repositorygegevens">
          <form class="flex flex-col gap-5" @submit.prevent="handleSubmit">

            <!-- GitHub Installation ID -->
            <div>
              <label for="installation-id" class="brand-label">GitHub App Installation ID</label>
              <input
                id="installation-id"
                v-model="form.github_installation_id"
                type="number"
                min="1"
                placeholder="12345678"
                class="brand-input font-mono"
                :class="touched.github_installation_id && !installationIdValid && '!border-govnl-rood-500'"
                :aria-invalid="touched.github_installation_id && !installationIdValid ? 'true' : undefined"
                :aria-describedby="touched.github_installation_id && !installationIdValid ? 'installation-id-error' : 'installation-id-hint'"
                @blur="touched.github_installation_id = true"
              />
              <p
                v-if="touched.github_installation_id && !installationIdValid"
                id="installation-id-error"
                role="alert"
                class="brand-help !text-govnl-rood-700"
              >Vul een geldig numeriek installation ID in.</p>
              <p
                v-else
                id="installation-id-hint"
                class="brand-help flex items-start gap-1.5"
              >
                <IconInfoCircle size="16" stroke="2" class="mt-0.5 shrink-0" aria-hidden="true" />
                Ga naar GitHub → Instellingen → GitHub Apps → uw app-installatie → kopieer het ID uit de URL.
              </p>
            </div>

            <!-- Repo owner -->
            <div>
              <label for="repo-owner" class="brand-label">Repository eigenaar</label>
              <input
                id="repo-owner"
                v-model="form.repo_owner"
                type="text"
                placeholder="bijv. mijn-organisatie"
                autocomplete="off"
                spellcheck="false"
                class="brand-input"
                :class="touched.repo_owner && !repoOwnerValid && '!border-govnl-rood-500'"
                :aria-invalid="touched.repo_owner && !repoOwnerValid ? 'true' : undefined"
                :aria-describedby="touched.repo_owner && !repoOwnerValid ? 'repo-owner-error' : undefined"
                @blur="touched.repo_owner = true"
              />
              <p
                v-if="touched.repo_owner && !repoOwnerValid"
                id="repo-owner-error"
                role="alert"
                class="brand-help !text-govnl-rood-700"
              >Vul de GitHub gebruikersnaam of organisatienaam in.</p>
            </div>

            <!-- Repo name -->
            <div>
              <label for="repo-name" class="brand-label">Repository naam</label>
              <input
                id="repo-name"
                v-model="form.repo_name"
                type="text"
                placeholder="bijv. mijn-ai-app"
                autocomplete="off"
                spellcheck="false"
                class="brand-input"
                :class="touched.repo_name && !repoNameValid && '!border-govnl-rood-500'"
                :aria-invalid="touched.repo_name && !repoNameValid ? 'true' : undefined"
                :aria-describedby="touched.repo_name && !repoNameValid ? 'repo-name-error' : undefined"
                @blur="touched.repo_name = true"
              />
              <p
                v-if="touched.repo_name && !repoNameValid"
                id="repo-name-error"
                role="alert"
                class="brand-help !text-govnl-rood-700"
              >Vul de naam van de repository in.</p>
              <p
                v-else-if="form.repo_owner && form.repo_name"
                class="brand-help font-mono"
              >
                github.com/{{ form.repo_owner.trim() }}/{{ form.repo_name.trim() }}
              </p>
            </div>

            <!-- Branch (optional) -->
            <div>
              <label for="branch" class="brand-label">
                Branch
                <span class="text-sm font-normal text-govnl-grijs-600"> — optioneel</span>
              </label>
              <input
                id="branch"
                v-model="form.branch"
                type="text"
                placeholder="Standaard: hoofdbranch van de repository"
                class="brand-input"
              />
            </div>

            <!-- Error banner -->
            <div
              v-if="submitError"
              role="alert"
              class="rounded-card border border-govnl-rood-500 bg-govnl-rood-50"
            >
              <p class="m-0 px-4 py-3 text-sm font-semibold text-govnl-rood-700">
                {{ submitError }}
              </p>
              <ul
                v-if="submitFieldErrors.length"
                class="m-0 flex list-none flex-col gap-1.5 px-4 pb-3"
              >
                <li
                  v-for="fe in submitFieldErrors"
                  :key="fe.path"
                  class="flex flex-col gap-0.5"
                >
                  <span class="w-fit rounded-card bg-govnl-rood-100 px-1.5 py-0.5 font-mono text-xs text-govnl-rood-700">
                    {{ fe.path }}
                  </span>
                  <span class="pl-0.5 text-sm text-govnl-rood-700">{{ fe.message }}</span>
                </li>
              </ul>
            </div>

            <!-- Actions -->
            <div class="flex flex-wrap items-center gap-3">
              <button
                type="submit"
                :disabled="isSubmitting"
                class="brand-btn brand-btn-primary"
              >{{ isSubmitting ? 'Bezig…' : 'App registreren' }}</button>
              <GovnlButton variant="secondary" to="/dashboard">Annuleren</GovnlButton>
            </div>
          </form>
        </section>

        <!-- How-to panel -->
        <aside class="flex flex-col gap-4" aria-label="Uitleg">

          <section class="brand-card" aria-labelledby="howto-title">
            <h2 id="howto-title" class="brand-h3 border-b border-brand-border px-5 py-3.5 !text-base">
              Hoe werkt het?
            </h2>
            <ol class="m-0 flex list-none flex-col gap-4 px-5 py-4">
              <li
                v-for="step in howToSteps"
                :key="step.title"
                class="flex items-start gap-3"
              >
                <component
                  :is="step.icon"
                  :size="22"
                  :stroke="1.5"
                  class="mt-px shrink-0 text-brand-primary"
                  aria-hidden="true"
                />
                <div>
                  <p class="m-0 mb-1 text-sm font-semibold text-brand-primary">{{ step.title }}</p>
                  <p class="m-0 text-sm leading-[1.55] text-govnl-grijs-700">{{ step.body }}</p>
                </div>
              </li>
            </ol>
          </section>

          <section class="brand-card overflow-hidden" aria-labelledby="structure-title">
            <h2 id="structure-title" class="brand-h3 border-b border-brand-border px-5 py-3.5 !text-base">
              Vereiste bestandsstructuur
            </h2>
            <pre class="m-0 overflow-x-auto bg-brand-surface px-5 py-4 font-mono text-xs leading-[1.7] text-govnl-grijs-800">uw-repo/
├── appstore.yaml    ← verplicht
├── chart/
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
├── docs/
│   ├── openapi.yaml
│   └── screenshots/ ← min. 3 PNG/JPG
└── audit/
    ├── dpia.pdf
    ├── security-scan.sarif
    ├── ai-risk-assessment.pdf
    └── accessibility-report.pdf</pre>
          </section>

          <div class="flex flex-col gap-3">
            <GovnlButton
              variant="secondary"
              size="sm"
              href="/appstore.example.yaml"
              download="appstore.example.yaml"
              :icon="IconDownload"
            >Download voorbeeld appstore.yaml</GovnlButton>
            <GovnlButton variant="tertiary" to="/toelating" :icon-after="IconArrowRight">
              Bekijk toelatingseisen
            </GovnlButton>
          </div>

        </aside>
      </div>
    </template>
  </div>
</template>
