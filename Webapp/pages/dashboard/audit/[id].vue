<script setup lang="ts">
import {
  IconCheck, IconCircleCheck, IconCircleX, IconClock,
  IconDownload, IconAlertTriangle, IconBrandGithub, IconMessage,
} from '~/lib/icons'
import type { AuditSubmissionDetail } from '~/composables/useData'
import { autoValColor } from '~/composables/useData'
import { formatDate } from '~/utils/constants'

definePageMeta({ middleware: 'auth', layout: 'dashboard' })

const { isAuditor } = useAuth()
useRoleGuard(isAuditor)

const route = useRoute()
const id = route.params.id as string
const { api, baseURL } = useApi()

const { data: sub, error } = await useAsyncData<AuditSubmissionDetail>(
  `audit:${id}`,
  () => api<AuditSubmissionDetail>(`/api/v1/audit-submissions/${id}`),
  { server: false },
)
useHead({ title: computed(() => sub.value?.app?.name ? `Beoordeling: ${sub.value.app.name}` : 'Beoordeling') })

if (error.value) {
  throw createError({ statusCode: 404, statusMessage: 'Inzending niet gevonden' })
}

const decision   = ref<'approve' | 'request_changes' | 'reject' | ''>('')
const rationale  = ref('')
const submitted  = ref(false)

const rationaleValid = computed(() => rationale.value.trim().length >= 20)
const formValid      = computed(() => decision.value !== '' && rationaleValid.value)

const decisionOptions = [
  {
    value: 'approve' as const,
    label: 'Goedkeuren',
    desc: 'App wordt gepubliceerd in de catalogus.',
    bg: 'bg-govnl-groen-50',
    border: 'border-govnl-groen-500',
    fg: 'text-govnl-groen-700',
  },
  {
    value: 'request_changes' as const,
    label: 'Wijzigingen vereist',
    desc: 'Ontwikkelaar moet aanpassingen doorvoeren.',
    bg: 'bg-govnl-oranje-50',
    border: 'border-govnl-oranje-500',
    fg: 'text-govnl-oranje-700',
  },
  {
    value: 'reject' as const,
    label: 'Afwijzen',
    desc: 'Aanvraag wordt definitief afgewezen.',
    bg: 'bg-govnl-rood-50',
    border: 'border-govnl-rood-500',
    fg: 'text-govnl-rood-700',
  },
]

const { run: submitDecision, pending: isSaving, error: saveError } = useAsyncAction(async () => {
  await api(`/api/v1/audit-submissions/${id}/decision`, {
    method: 'POST',
    body: { decision: decision.value, rationale: rationale.value.trim() },
  })
  submitted.value = true
}, 'Besluit opslaan mislukt.')

function autoValLabel(state: string) {
  switch (state) {
    case 'passed':  return 'Stage 1 geslaagd'
    case 'failed':  return 'Stage 1 mislukt'
    default:        return 'Stage 1 in behandeling'
  }
}

function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function evidenceUrl(logicalName: string) {
  return `${baseURL}/api/v1/audit-submissions/${id}/evidence/${encodeURIComponent(logicalName)}`
}

// ─── Comments ────────────────────────────────────────────────────────────────

interface AuditComment {
  id: string
  author_user_id: string
  body: string
  created_at: string
}

const comments = ref<AuditComment[]>([])

const { run: loadComments, pending: commentsLoading, error: commentsError } = useAsyncAction(async () => {
  const data = await api<{ items: AuditComment[]; total: number }>(`/api/v1/audit-submissions/${id}/comments`)
  comments.value = data.items ?? []
}, 'Opmerkingen laden mislukt.')

onMounted(loadComments)

const commentBody = ref('')

const { run: postComment, pending: isPostingComment, error: commentPostError } = useAsyncAction(async (body: string) => {
  const comment = await api<AuditComment>(`/api/v1/audit-submissions/${id}/comments`, {
    method:  'POST',
    body:    { body },
  })
  comments.value.push(comment)
  commentBody.value = ''
}, 'Opmerking plaatsen mislukt.')

async function handlePostComment() {
  const body = commentBody.value.trim()
  if (!body) return
  await postComment(body)
}
</script>

<template>
  <div class="flex flex-col gap-5">

    <UiBreadcrumb
      :items="[
        { label: 'Beoordelingswachtrij', to: '/dashboard/audit' },
        { label: sub?.app?.name ?? id },
      ]"
    />

    <div v-if="!sub" class="py-16 text-center text-govnl-grijs-600">
      Inzending laden…
    </div>

    <template v-else>
      <div class="brand-card p-6">
        <div class="flex items-start justify-between flex-wrap gap-4">
          <div class="min-w-0">
            <h1 class="brand-h1 mb-2 break-words">{{ sub.app.name }}</h1>
            <div class="flex items-center gap-3 flex-wrap">
              <a
                :href="`https://github.com/${sub.app.slug}`"
                target="_blank"
                rel="noopener"
                class="brand-link inline-flex items-center gap-1 text-sm"
              >
                <IconBrandGithub size="14" aria-hidden="true" /> {{ sub.app.slug }}
              </a>
              <span class="text-govnl-grijs-600" aria-hidden="true">·</span>
              <code class="text-sm bg-brand-surface px-1.5 py-0.5 rounded-card">{{ sub.version.git_ref }}</code>
              <code class="text-xs text-govnl-grijs-600 font-mono">{{ sub.version.git_sha.slice(0, 8) }}</code>
            </div>
          </div>
          <span
            class="inline-flex items-center gap-1.5 px-3 py-1 rounded-card text-sm font-bold"
            :class="[autoValColor(sub.auto_validation_state).bg, autoValColor(sub.auto_validation_state).fg]"
          >
            <IconCircleCheck v-if="sub.auto_validation_state === 'passed'" size="14" aria-hidden="true" />
            <IconCircleX v-else-if="sub.auto_validation_state === 'failed'" size="14" aria-hidden="true" />
            <IconClock v-else size="14" aria-hidden="true" />
            {{ autoValLabel(sub.auto_validation_state) }}
          </span>
        </div>
      </div>

      <div
        class="rounded-card p-4 border"
        :class="{
          'bg-govnl-groen-50 border-govnl-groen-300': sub.auto_validation_state === 'passed',
          'bg-govnl-rood-50 border-govnl-rood-200': sub.auto_validation_state === 'failed',
          'bg-white border-brand-border': sub.auto_validation_state !== 'passed' && sub.auto_validation_state !== 'failed',
        }"
      >
        <div class="flex items-center gap-2 mb-1.5">
          <IconCircleCheck v-if="sub.auto_validation_state === 'passed'" size="16" class="text-govnl-groen-700" aria-hidden="true" />
          <IconAlertTriangle v-else-if="sub.auto_validation_state === 'failed'" size="16" class="text-govnl-rood-700" aria-hidden="true" />
          <IconClock v-else size="16" class="text-govnl-grijs-600" aria-hidden="true" />
          <span class="font-semibold text-brand-body">
            Stage 1 automatische validatie:
            {{ sub.auto_validation_state === 'passed' ? 'geslaagd' : sub.auto_validation_state === 'failed' ? 'mislukt' : 'in behandeling' }}
          </span>
        </div>
        <ul
          v-if="sub.auto_validation_errors?.length"
          class="mt-2 pl-5 text-sm text-govnl-rood-700 m-0"
        >
          <li v-for="(err, i) in sub.auto_validation_errors" :key="i">{{ err }}</li>
        </ul>
        <p v-else-if="sub.auto_validation_state === 'passed'" class="mt-1 text-sm text-govnl-groen-700 mb-0">
          Alle SARIF-bevindingen vallen onder de drempelwaarde (CVSS &lt; 7.0).
        </p>
      </div>

      <section class="brand-card overflow-hidden" aria-labelledby="evidence-heading">
        <div class="px-5 py-4 border-b border-brand-border">
          <h2 id="evidence-heading" class="brand-h3">Bewijsdocumenten</h2>
        </div>
        <div v-if="!sub.evidence_files?.length" class="p-6 text-sm text-govnl-grijs-600">
          Geen bewijsdocumenten gevonden.
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full border-collapse">
            <thead>
              <tr class="bg-brand-surface border-b border-brand-border">
                <th scope="col" class="px-4 py-2.5 text-left text-sm font-semibold text-govnl-grijs-700">Bestand</th>
                <th scope="col" class="px-4 py-2.5 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">Type</th>
                <th scope="col" class="px-4 py-2.5 text-left text-sm font-semibold text-govnl-grijs-700 hidden sm:table-cell">Grootte</th>
                <th scope="col" class="px-4 py-2.5"><span class="sr-only">Acties</span></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="f in sub.evidence_files"
                :key="f.logical_name"
                class="border-b border-brand-border last:border-b-0"
              >
                <td class="px-4 py-3 text-sm font-semibold text-brand-body">{{ f.logical_name }}</td>
                <td class="px-4 py-3 text-sm text-govnl-grijs-600 hidden sm:table-cell">{{ f.content_type }}</td>
                <td class="px-4 py-3 text-sm text-govnl-grijs-600 hidden sm:table-cell">{{ fileSize(f.size_bytes) }}</td>
                <td class="px-4 py-3 text-right">
                  <a
                    :href="evidenceUrl(f.logical_name)"
                    target="_blank"
                    rel="noopener"
                    class="brand-link inline-flex items-center gap-1 text-sm font-semibold"
                  >
                    <IconDownload size="14" aria-hidden="true" /> Downloaden
                  </a>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <div
        v-if="submitted"
        role="status"
        class="bg-govnl-groen-50 border border-govnl-groen-300 rounded-card px-6 py-5 flex items-center justify-between flex-wrap gap-4"
      >
        <div class="flex items-center gap-2.5">
          <IconCircleCheck size="20" class="text-govnl-groen-700" aria-hidden="true" />
          <span class="font-semibold text-govnl-groen-700">Besluit opgeslagen.</span>
        </div>
        <NuxtLink to="/dashboard/audit" class="brand-link text-sm font-semibold">← Terug naar wachtrij</NuxtLink>
      </div>

      <section v-else class="brand-card p-6" aria-labelledby="decision-label">
        <h2 id="decision-label" class="brand-h3 mb-4">Besluit</h2>

        <div role="radiogroup" aria-labelledby="decision-label" class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-5">
          <label
            v-for="opt in decisionOptions"
            :key="opt.value"
            :for="`decision-${opt.value}`"
            class="block p-3.5 rounded-card border-2 cursor-pointer transition-colors duration-150 has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-brand-primary"
            :class="decision === opt.value
              ? [opt.bg, opt.border]
              : 'bg-white border-brand-border hover:bg-brand-surface'"
          >
            <input
              :id="`decision-${opt.value}`"
              type="radio"
              name="decision"
              :value="opt.value"
              v-model="decision"
              class="sr-only"
            />
            <div
              class="flex items-center gap-1.5 font-bold mb-1"
              :class="decision === opt.value ? opt.fg : 'text-brand-body'"
            >
              <IconCheck v-if="decision === opt.value" size="16" stroke="2.5" aria-hidden="true" />
              {{ opt.label }}
            </div>
            <div class="text-sm text-govnl-grijs-700">{{ opt.desc }}</div>
          </label>
        </div>

        <div class="mb-5">
          <label for="rationale" class="brand-label">
            Motivering <span class="text-govnl-grijs-600 font-normal">(minimaal 20 tekens)</span>
          </label>
          <textarea
            id="rationale"
            v-model="rationale"
            rows="5"
            placeholder="Beschrijf uw bevindingen, verwijs naar SARIF-bevindingen of relevante passages in de auditdocumenten…"
            class="brand-input resize-y"
            :class="rationale && !rationaleValid && '!border-govnl-rood-500'"
            :aria-invalid="rationale && !rationaleValid ? 'true' : undefined"
            :aria-describedby="rationale && !rationaleValid ? 'rationale-error' : undefined"
          />
          <div class="flex justify-between gap-4 text-sm mt-1">
            <span v-if="rationale && !rationaleValid" id="rationale-error" role="alert" class="text-govnl-rood-700">
              Minimaal 20 tekens vereist (nu {{ rationale.trim().length }}).
            </span>
            <span v-else />
            <span class="text-govnl-grijs-600">{{ rationale.trim().length }} tekens</span>
          </div>
        </div>

        <div
          v-if="saveError"
          role="alert"
          class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700 mb-4"
        >
          {{ saveError }}
        </div>

        <div class="flex items-center justify-between gap-4 flex-wrap">
          <NuxtLink to="/dashboard/audit" class="brand-link text-sm">← Annuleren</NuxtLink>
          <button
            type="button"
            :disabled="!formValid || isSaving"
            class="brand-btn brand-btn-primary"
            @click="submitDecision"
          >
            {{ isSaving ? 'Opslaan…' : 'Besluit opslaan' }}
          </button>
        </div>
      </section>

      <!-- Comments -->
      <section class="brand-card overflow-hidden" aria-labelledby="comments-heading">
        <div class="px-5 py-4 border-b border-brand-border flex items-center gap-2">
          <IconMessage size="18" class="text-brand-primary" aria-hidden="true" />
          <h2 id="comments-heading" class="brand-h3">Opmerkingen</h2>
          <span class="text-sm text-govnl-grijs-600 ml-auto">{{ comments.length }}</span>
        </div>

        <div v-if="commentsLoading" class="p-6 text-sm text-govnl-grijs-600 text-center">Laden…</div>
        <div
          v-else-if="commentsError"
          class="px-5 py-3 text-sm text-govnl-rood-700 bg-govnl-rood-50"
        >{{ commentsError }}</div>
        <div
          v-else-if="!comments.length"
          class="px-5 py-8 text-sm text-govnl-grijs-600 text-center"
        >Nog geen opmerkingen.</div>
        <div v-else class="divide-y divide-brand-border">
          <div v-for="c in comments" :key="c.id" class="px-5 py-4">
            <div class="flex items-baseline gap-2 mb-1.5">
              <code class="text-xs text-govnl-grijs-700 font-mono bg-brand-surface px-1.5 py-0.5 rounded-card">
                {{ c.author_user_id.slice(0, 8) }}
              </code>
              <span class="text-xs text-govnl-grijs-600">{{ formatDate(c.created_at) }}</span>
            </div>
            <p class="m-0 text-brand-body whitespace-pre-wrap leading-relaxed">{{ c.body }}</p>
          </div>
        </div>

        <div class="px-5 py-4 border-t border-brand-border">
          <label for="comment-body" class="sr-only">Opmerking toevoegen</label>
          <textarea
            id="comment-body"
            v-model="commentBody"
            rows="3"
            placeholder="Voeg een opmerking toe…"
            class="brand-input resize-y mb-2.5"
          />
          <div
            v-if="commentPostError"
            class="text-sm text-govnl-rood-700 mb-2.5"
            role="alert"
          >{{ commentPostError }}</div>
          <button
            type="button"
            :disabled="isPostingComment || !commentBody.trim()"
            class="brand-btn brand-btn-secondary brand-btn-sm"
            @click="handlePostComment"
          >{{ isPostingComment ? 'Plaatsen…' : 'Opmerking plaatsen' }}</button>
        </div>
      </section>

    </template>
  </div>
</template>
