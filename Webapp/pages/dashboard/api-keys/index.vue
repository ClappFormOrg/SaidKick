<script setup lang="ts">
import {
  IconKey, IconPlus, IconTrash, IconCopy, IconCheck, IconEye, IconEyeOff,
} from '~/lib/icons'
import { errorMessage } from '~/composables/useApi'
import { COPY_FEEDBACK_MS, formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'API-sleutels' })

const { api } = useApi()
const { me, isDeveloper, isDeployer, isAuditor } = useAuth()

const canManageKeys = computed(() => isDeveloper.value || isDeployer.value || isAuditor.value)

// A key may only carry roles its creator holds; the API rejects anything else.
const ROLE_LABELS: Record<string, string> = {
  developer:      'Developer',
  auditor:        'Auditor',
  deployer:       'Deployer',
  cluster_admin:  'Cluster-beheerder',
  org_admin:      'Org-beheerder',
  platform_admin: 'Platformbeheerder',
}
const roleLabel = (role: string) => ROLE_LABELS[role] ?? role
// app_user is for people in a browser; the API refuses it on a key.
const ownRoles  = computed(() => (me.value?.roles ?? []).filter(r => r !== 'app_user'))

const MAX_EXPIRES_DAYS     = 90
const DEFAULT_EXPIRES_DAYS = 30

// ─── Types ────────────────────────────────────────────────────────────────────

interface APIKey {
  id: string
  name: string
  prefix: string
  roles?: string[]
  created_at: string
  last_used_at?: string
  expires_at?: string
}

interface CreateAPIKeyResponse extends APIKey {
  token: string
}

// ─── List keys ───────────────────────────────────────────────────────────────

const keys = ref<APIKey[]>([])

const { run: loadKeys, pending: loading, error: listError } = useAsyncAction(async () => {
  const data = await api<{ items: APIKey[]; total: number }>('/api/v1/api-keys')
  keys.value = data.items ?? []
}, 'Laden mislukt.')

onMounted(loadKeys)

// ─── Create key ───────────────────────────────────────────────────────────────

const showForm    = ref(false)
const form        = reactive({ name: '', roles: [] as string[], expires_days: DEFAULT_EXPIRES_DAYS })
const newKeyResult = ref<CreateAPIKeyResponse | null>(null)
const tokenVisible = ref(false)
const tokenCopied  = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | undefined

function resetForm() {
  form.name         = ''
  // Preselect the only option; with several roles the user picks explicitly.
  form.roles        = ownRoles.value.length === 1 ? [...ownRoles.value] : []
  form.expires_days = DEFAULT_EXPIRES_DAYS
}

function toggleForm() {
  if (!showForm.value) resetForm()
  showForm.value = !showForm.value
}

const expiresValid = computed(() => {
  const days = Number(form.expires_days)
  return Number.isInteger(days) && days >= 1 && days <= MAX_EXPIRES_DAYS
})
const canSubmit = computed(() =>
  !!form.name.trim() && form.roles.length > 0 && expiresValid.value,
)

const { run: createKey, pending: isCreating, error: createErrorMsg } = useAsyncAction(async () => {
  const result = await api<CreateAPIKeyResponse>('/api/v1/api-keys', {
    method: 'POST',
    body:   {
      name:         form.name.trim(),
      roles:        form.roles,
      expires_days: Number(form.expires_days),
    },
  })
  newKeyResult.value = result
  showForm.value     = false
  resetForm()
  await loadKeys()
}, 'Aanmaken mislukt.')

async function handleCreate() {
  if (!canSubmit.value) return
  await createKey()
}

async function copyToken() {
  if (!newKeyResult.value) return
  await navigator.clipboard.writeText(newKeyResult.value.token)
  tokenCopied.value = true
  clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { tokenCopied.value = false }, COPY_FEEDBACK_MS)
}
onUnmounted(() => clearTimeout(copyTimer))

// ─── Revoke key ───────────────────────────────────────────────────────────────

const revokingId  = ref<string | null>(null)
const revokeError = ref('')

async function handleRevoke(key: APIKey) {
  if (!window.confirm(`API-sleutel "${key.name}" intrekken? Dit kan niet ongedaan worden gemaakt.`)) return
  revokingId.value  = key.id
  revokeError.value = ''
  try {
    await api(`/api/v1/api-keys/${key.id}`, { method: 'DELETE' })
    keys.value = keys.value.filter(k => k.id !== key.id)
    if (newKeyResult.value?.id === key.id) newKeyResult.value = null
  } catch (err: unknown) {
    revokeError.value = errorMessage(err, 'Intrekken mislukt.')
  } finally {
    revokingId.value = null
  }
}

</script>

<template>
  <div class="flex flex-col gap-6">

    <UiSectionHead
      title="API-sleutels"
      lead="Machine-tokens voor programmatische toegang."
      level="h1"
      as="h1"
      class="!mb-0"
    >
      <template v-if="canManageKeys" #action>
        <button
          type="button"
          class="brand-btn brand-btn-primary"
          @click="toggleForm"
        >
          <IconPlus size="18" stroke="2" aria-hidden="true" />
          Sleutel aanmaken
        </button>
      </template>
    </UiSectionHead>

    <div
      v-if="listError"
      class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700"
      role="alert"
    >{{ listError }}</div>

    <!-- New key token reveal (shown once after creation) -->
    <div
      v-if="newKeyResult"
      class="bg-govnl-groen-50 border border-govnl-groen-300 rounded-card px-6 py-5"
      role="status"
    >
      <div class="text-body font-bold text-govnl-groen-700 mb-1">
        Sleutel "{{ newKeyResult.name }}" aangemaakt
      </div>
      <p class="text-sm text-govnl-groen-700 m-0 mb-3.5">
        Kopieer de sleutel nu — deze wordt <strong>nooit meer getoond</strong>.
      </p>
      <div class="relative bg-white border border-govnl-groen-300 rounded-card px-4 pr-32 py-3.5 font-mono text-sm text-brand-body break-all">
        {{ tokenVisible ? newKeyResult.token : newKeyResult.token.replace(/./g, '•') }}
        <div class="absolute top-2.5 right-2.5 flex gap-1.5">
          <button
            type="button"
            class="px-2 py-1 border border-govnl-groen-300 rounded-card bg-white cursor-pointer inline-flex items-center text-xs text-govnl-groen-700"
            :aria-label="tokenVisible ? 'Verbergen' : 'Tonen'"
            @click="tokenVisible = !tokenVisible"
          >
            <IconEye v-if="!tokenVisible" size="14" aria-hidden="true" />
            <IconEyeOff v-else size="14" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="px-2 py-1 border border-govnl-groen-300 rounded-card bg-white cursor-pointer inline-flex items-center gap-1 text-xs text-govnl-groen-700"
            @click="copyToken"
          >
            <IconCheck v-if="tokenCopied" size="12" aria-hidden="true" />
            <IconCopy v-else size="12" aria-hidden="true" />
            {{ tokenCopied ? 'Gekopieerd' : 'Kopiëren' }}
          </button>
        </div>
      </div>
      <button
        type="button"
        class="brand-btn brand-btn-link brand-btn-sm mt-3"
        @click="newKeyResult = null"
      >Sluiten ×</button>
    </div>

    <!-- Create form -->
    <section v-if="showForm" class="brand-card p-6" aria-labelledby="new-key-heading">
      <h2 id="new-key-heading" class="brand-h3 mb-5">Nieuwe API-sleutel</h2>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-4">
        <div>
          <label for="key-name" class="brand-label">
            Naam <span class="text-govnl-rood-700" aria-hidden="true">*</span>
          </label>
          <input
            id="key-name"
            v-model="form.name"
            type="text"
            placeholder="bijv. ci-pipeline-key"
            class="brand-input"
            @keydown.enter="handleCreate"
          />
        </div>
        <div>
          <label for="key-expires" class="brand-label">
            Verlooptermijn (dagen) <span class="text-govnl-rood-700" aria-hidden="true">*</span>
          </label>
          <input
            id="key-expires"
            v-model.number="form.expires_days"
            type="number"
            min="1"
            :max="MAX_EXPIRES_DAYS"
            step="1"
            required
            aria-describedby="key-expires-hint"
            :aria-invalid="!expiresValid"
            class="brand-input"
          />
          <p
            id="key-expires-hint"
            class="brand-help"
            :class="!expiresValid && '!text-govnl-rood-700'"
          >Tussen 1 en {{ MAX_EXPIRES_DAYS }} dagen.</p>
        </div>
      </div>

      <fieldset class="border-none p-0 m-0 mb-4">
        <legend class="brand-label p-0">
          Rollen <span class="text-govnl-rood-700" aria-hidden="true">*</span>
        </legend>
        <p class="brand-help mt-0 mb-2">
          De sleutel krijgt alleen de gekozen rollen, en alleen zolang u ze zelf heeft.
        </p>
        <div class="flex flex-wrap gap-2">
          <label
            v-for="r in ownRoles"
            :key="r"
            class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-card text-sm font-semibold cursor-pointer border has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-brand-primary"
            :class="form.roles.includes(r)
              ? 'bg-brand-primary-subtle text-brand-primary border-brand-primary'
              : 'bg-white text-govnl-grijs-700 border-brand-border'"
          >
            <input
              v-model="form.roles"
              type="checkbox"
              :value="r"
              class="accent-brand-primary"
            />
            {{ roleLabel(r) }}
          </label>
        </div>
      </fieldset>

      <div
        v-if="createErrorMsg"
        role="alert"
        class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700 mb-4"
      >{{ createErrorMsg }}</div>

      <div class="flex gap-3 items-center flex-wrap">
        <button
          type="button"
          :disabled="isCreating || !canSubmit"
          class="brand-btn brand-btn-navy"
          @click="handleCreate"
        >{{ isCreating ? 'Aanmaken…' : 'Aanmaken' }}</button>
        <button
          type="button"
          class="brand-btn brand-btn-secondary"
          @click="showForm = false; createErrorMsg = ''"
        >Annuleren</button>
      </div>
    </section>

    <!-- Revoke error -->
    <div
      v-if="revokeError"
      role="alert"
      class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700"
    >{{ revokeError }}</div>

    <!-- Keys table -->
    <section class="brand-card overflow-hidden" aria-labelledby="keys-heading">
      <div class="px-5 py-4 border-b border-brand-border flex justify-between items-center gap-4">
        <h2 id="keys-heading" class="brand-h3">Actieve sleutels</h2>
        <span class="text-sm text-govnl-grijs-600">{{ keys.length }} sleutel{{ keys.length !== 1 ? 's' : '' }}</span>
      </div>

      <div v-if="loading" class="p-8 text-center text-govnl-grijs-600 text-sm">
        Laden…
      </div>

      <div
        v-else-if="!keys.length"
        class="p-12 text-center text-govnl-grijs-600"
      >
        <IconKey size="32" class="mx-auto mb-3 text-brand-primary" aria-hidden="true" />
        <div class="font-semibold mb-1 text-brand-body">Geen API-sleutels</div>
        <div class="text-sm">Klik op "Sleutel aanmaken" om een machine-token te genereren.</div>
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse">
          <thead>
            <tr class="bg-brand-surface border-b border-brand-border">
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Naam</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Prefix</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">Rollen</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">Aangemaakt</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden md:table-cell">Laatste gebruik</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden lg:table-cell">Verloopt</th>
              <th scope="col" class="px-4 py-3"><span class="sr-only">Acties</span></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="key in keys"
              :key="key.id"
              class="border-b border-brand-border last:border-b-0"
            >
              <td class="px-4 py-3.5 font-semibold text-brand-body">{{ key.name }}</td>
              <td class="px-4 py-3.5">
                <code class="bg-brand-surface px-1.5 py-0.5 rounded-card text-xs font-mono text-govnl-grijs-700">{{ key.prefix }}…</code>
              </td>
              <td class="px-4 py-3.5 text-sm text-govnl-grijs-600 hidden sm:table-cell">
                {{ key.roles?.length ? key.roles.map(roleLabel).join(', ') : '—' }}
              </td>
              <td class="px-4 py-3.5 text-sm text-govnl-grijs-600 hidden sm:table-cell">{{ formatDate(key.created_at) }}</td>
              <td class="px-4 py-3.5 text-sm text-govnl-grijs-600 hidden md:table-cell">{{ key.last_used_at ? formatDate(key.last_used_at) : 'Nooit' }}</td>
              <td class="px-4 py-3.5 text-sm hidden lg:table-cell">
                <span
                  v-if="key.expires_at"
                  :class="new Date(key.expires_at) < new Date() ? 'text-govnl-rood-700' : 'text-govnl-grijs-600'"
                >{{ formatDate(key.expires_at) }}</span>
                <span v-else class="text-govnl-grijs-600">Nooit</span>
              </td>
              <td class="px-4 py-3.5 text-right">
                <button
                  type="button"
                  :disabled="revokingId === key.id"
                  class="inline-flex items-center gap-1 px-2.5 py-1 text-sm font-semibold border border-govnl-rood-300 rounded-card bg-white text-govnl-rood-700 cursor-pointer hover:bg-govnl-rood-50 disabled:opacity-50 disabled:cursor-not-allowed"
                  :aria-label="`Sleutel ${key.name} intrekken`"
                  @click="handleRevoke(key)"
                >
                  <IconTrash size="14" aria-hidden="true" />
                  {{ revokingId === key.id ? '…' : 'Intrekken' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Security note -->
    <div class="px-4 py-3.5 bg-white border border-brand-border rounded-card text-sm text-govnl-grijs-700 leading-relaxed">
      <strong>Beveiliging:</strong> API-sleutels geven toegang met de gekozen rollen, nooit meer dan uw eigen rollen, en verlopen na maximaal {{ MAX_EXPIRES_DAYS }} dagen. Bewaar ze veilig, deel ze niet en trek sleutels in die niet meer in gebruik zijn.
      Sleutels worden nooit opgeslagen — alleen het gehashte token wordt bewaard.
    </div>

  </div>
</template>
