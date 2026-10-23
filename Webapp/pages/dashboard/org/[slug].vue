<script setup lang="ts">
import {
  IconEdit, IconX, IconCheck,
} from '~/lib/icons'
import type { OrgMember } from '~/composables/useData'
import { formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })

const route = useRoute()
const slug  = route.params.slug as string
useHead({ title: computed(() => slug) })
const { api } = useApi()
const { isOrgAdmin, isPlatformAdmin, effectiveOrgSlug } = useAuth()
useRoleGuard(isOrgAdmin)

interface MembersResponse { items: OrgMember[] }

// An org_admin may only manage their own organisation; a platform admin may
// visit any slug. Another organisation's page renders an explicit no-access
// state rather than an empty roster, which would read as "no members".
const isOwnOrg = computed(() => isPlatformAdmin.value || slug === effectiveOrgSlug.value)

// Listing members requires org_admin or platform_admin. Skip the request
// for anyone else instead of letting it fail with a 403.
const { data: members, error: membersError, refresh } = await useAsyncData<MembersResponse>(
  `org-members:${slug}`,
  () => isOrgAdmin.value && isOwnOrg.value
    ? api<MembersResponse>(`/api/v1/orgs/${slug}/members`)
    : Promise.resolve({ items: [] }),
  { server: false, default: (): MembersResponse => ({ items: [] }) },
)

const membersStatus = computed(() => {
  const err = membersError.value as { statusCode?: number; status?: number } | null | undefined
  return err ? (err.statusCode ?? err.status ?? 0) : 0
})
const noAccess = computed(() => !isOwnOrg.value || membersStatus.value === 403)
const notFound = computed(() => membersStatus.value === 404)

const editingId  = ref<string | null>(null)
const editRoles  = ref<string[]>([])

const ALL_ROLES = [
  { value: 'developer',     label: 'Developer' },
  { value: 'auditor',       label: 'Auditor' },
  { value: 'deployer',      label: 'Deployer' },
  { value: 'cluster_admin', label: 'Cluster-beheerder' },
  { value: 'org_admin',     label: 'Org-beheerder' },
  { value: 'app_user',      label: 'Gebruiker' },
]

// Only a platform admin may grant or revoke these roles; the API refuses
// anyone else with 403.
const PLATFORM_ONLY_ROLES = ['auditor']
const PLATFORM_ONLY_TOOLTIP = 'Alleen een platformbeheerder kan de rol auditor toekennen of intrekken.'

function isRoleLocked(role: string) {
  return !isPlatformAdmin.value && PLATFORM_ONLY_ROLES.includes(role)
}

function startEdit(member: OrgMember) {
  editingId.value = member.id
  editRoles.value = [...member.roles]
  saveError.value = ''
}

function cancelEdit() {
  editingId.value = null
  editRoles.value = []
}

const { run: saveRoles, pending: isSaving, error: saveError } = useAsyncAction(async (userId: string) => {
  await api<void>(`/api/v1/orgs/${slug}/members/${userId}/roles`, {
    method: 'POST',
    body: { roles: editRoles.value },
  })
  await refresh()
  editingId.value = null
}, 'Opslaan mislukt.')

function roleLabel(role: string) {
  return ALL_ROLES.find(r => r.value === role)?.label ?? role
}

</script>

<template>
  <div class="flex flex-col gap-6">

    <div>
      <p class="m-0 mb-1 text-sm font-semibold text-govnl-grijs-600">Organisatiebeheer</p>
      <h1 class="brand-h1 break-words">{{ slug }}</h1>
    </div>

    <div
      v-if="isOrgAdmin && noAccess"
      role="alert"
      class="brand-card p-12 text-center"
    >
      <h2 class="brand-h3 mb-2">Geen toegang tot deze organisatie</h2>
      <p class="m-0 text-sm text-govnl-grijs-600">
        U kunt alleen de leden van uw eigen organisatie beheren.
      </p>
    </div>

    <div
      v-else-if="isOrgAdmin && notFound"
      role="alert"
      class="brand-card p-12 text-center"
    >
      <h2 class="brand-h3">Organisatie niet gevonden</h2>
    </div>

    <div
      v-else-if="isOrgAdmin && membersError"
      role="alert"
      class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-5 py-4 text-sm text-govnl-rood-700"
    >
      Leden laden mislukt.
      <button type="button" class="ml-2 underline bg-transparent border-none text-govnl-rood-700 cursor-pointer p-0" @click="refresh()">
        Opnieuw proberen
      </button>
    </div>

    <section v-else-if="isOrgAdmin" class="brand-card overflow-hidden" aria-labelledby="members-heading">
      <div class="px-5 py-4 border-b border-brand-border flex justify-between items-center gap-4">
        <h2 id="members-heading" class="brand-h3">Leden</h2>
        <span class="text-sm text-govnl-grijs-600">{{ members?.items?.length ?? 0 }} leden</span>
      </div>

      <div v-if="!members?.items?.length" class="p-12 text-center text-govnl-grijs-600 text-sm">
        Geen leden gevonden.
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse">
          <thead>
            <tr class="bg-brand-surface border-b border-brand-border">
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Naam</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">E-mail</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700">Rollen</th>
              <th scope="col" class="px-4 py-3 text-left text-sm font-semibold text-govnl-grijs-700 hidden md:table-cell">Lid sinds</th>
              <th scope="col" class="px-4 py-3"><span class="sr-only">Acties</span></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="member in members.items"
              :key="member.id"
              class="border-b border-brand-border last:border-b-0"
            >
              <td class="px-4 py-3.5 font-semibold text-brand-body">
                {{ member.display_name }}
              </td>
              <td class="px-4 py-3.5 text-sm text-govnl-grijs-600 hidden sm:table-cell">
                {{ member.email }}
              </td>
              <td class="px-4 py-3.5">
                <div v-if="editingId === member.id">
                  <div class="flex flex-wrap gap-1.5 mb-2">
                    <!-- Selected roles take the primary tint; the check icon
                         repeats the state so it is not carried by colour. -->
                    <label
                      v-for="r in ALL_ROLES"
                      :key="r.value"
                      class="inline-flex items-center gap-1 px-2 py-0.5 rounded-card text-xs font-semibold border has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-brand-primary"
                      :class="[
                        editRoles.includes(r.value)
                          ? 'bg-brand-primary-subtle text-brand-primary border-brand-primary'
                          : 'bg-white text-govnl-grijs-700 border-brand-border',
                        isRoleLocked(r.value) ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
                      ]"
                      :title="isRoleLocked(r.value) ? PLATFORM_ONLY_TOOLTIP : undefined"
                    >
                      <input
                        type="checkbox"
                        :value="r.value"
                        v-model="editRoles"
                        :disabled="isRoleLocked(r.value)"
                        class="sr-only"
                      />
                      <IconCheck v-if="editRoles.includes(r.value)" size="12" stroke="2.5" aria-hidden="true" />
                      {{ r.label }}
                    </label>
                  </div>
                  <div v-if="saveError" class="text-xs text-govnl-rood-700 mb-1.5">{{ saveError }}</div>
                  <div class="flex gap-2">
                    <button
                      type="button"
                      class="brand-btn brand-btn-navy brand-btn-sm"
                      :disabled="isSaving"
                      @click="saveRoles(member.id)"
                    >
                      <IconCheck size="14" aria-hidden="true" />
                      {{ isSaving ? 'Opslaan…' : 'Opslaan' }}
                    </button>
                    <button
                      type="button"
                      class="brand-btn brand-btn-secondary brand-btn-sm"
                      @click="cancelEdit"
                    >
                      <IconX size="14" aria-hidden="true" /> Annuleren
                    </button>
                  </div>
                </div>
                <div v-else class="flex flex-wrap gap-1.5">
                  <span
                    v-for="r in member.roles"
                    :key="r"
                    class="inline-block px-2 py-0.5 rounded-card text-xs font-semibold border border-brand-border bg-brand-surface text-brand-body"
                  >{{ roleLabel(r) }}</span>
                  <span v-if="!member.roles.length" class="text-sm text-govnl-grijs-600">—</span>
                </div>
              </td>
              <td class="px-4 py-3.5 text-sm text-govnl-grijs-600 hidden md:table-cell">
                {{ formatDate(member.created_at) }}
              </td>
              <td class="px-4 py-3.5 text-right">
                <button
                  v-if="editingId !== member.id"
                  type="button"
                  class="brand-btn brand-btn-secondary brand-btn-sm"
                  @click="startEdit(member)"
                >
                  <IconEdit size="14" aria-hidden="true" /> Bewerken
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

  </div>
</template>
