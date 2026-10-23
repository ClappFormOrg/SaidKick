<script setup lang="ts">
import {
  IconAlertTriangle, IconCheck, IconPhoto, IconRefresh, IconTrash, IconUpload,
} from '~/lib/icons'
import {
  BRAND_COLOR_KEYS, CONTRAST_PAIR_LABELS, brandColors, brandLogoSrc, contrastFailures, parseBranding,
  type BrandColorKey, type BrandColors, type BrandLogo, type ContrastFailure,
} from '~/utils/branding'

// Huisstijl (#158): the instance-wide palette and logo. platform_admin only,
// as the API's /branding write routes are.
definePageMeta({ middleware: 'auth', layout: 'dashboard' })
useHead({ title: 'Huisstijl' })

const { isPlatformAdmin } = useAuth()
useRoleGuard(isPlatformAdmin)

const { api } = useApi()
const { apply } = useBranding()

const COLOR_FIELDS: { key: BrandColorKey; label: string; hint: string }[] = [
  { key: 'cta',       label: 'Actiekleur',      hint: 'Belangrijkste knoppen en accenten. Witte tekst moet erop leesbaar zijn.' },
  { key: 'primary',   label: 'Primaire kleur',  hint: 'Knoppen, links en de kopbalk.' },
  { key: 'secondary', label: 'Secundaire kleur', hint: 'Lichte vlakken achter primaire tekst.' },
  { key: 'surface',   label: 'Achtergrondkleur', hint: 'Achtergrond van panelen en pagina’s.' },
  { key: 'body_text', label: 'Tekstkleur',      hint: 'Lopende tekst.' },
  { key: 'border',    label: 'Randkleur',       hint: 'Scheidingslijnen en kaders.' },
]

// Read-only shades the API derives from the saved palette.
const DERIVED: { key: string; label: string }[] = [
  { key: 'cta_hover',      label: 'Actiekleur (hover)' },
  { key: 'cta_active',     label: 'Actiekleur (actief)' },
  { key: 'cta_subtle',     label: 'Actiekleur (licht)' },
  { key: 'primary_hover',  label: 'Primaire kleur (hover)' },
  { key: 'primary_active', label: 'Primaire kleur (actief)' },
  { key: 'primary_subtle', label: 'Primaire kleur (licht)' },
]

const HEX_INPUT = /^#[0-9a-fA-F]{6}$/

const form = reactive<BrandColors>({
  cta: '', primary: '', secondary: '', surface: '', body_text: '', border: '',
})
const tokens = ref<Record<string, string>>({})
const isDefault = ref(true)
const logo = ref<BrandLogo | null>(null)
// Failing pairs of the last refused save. They stay on screen until a save
// succeeds, so the admin can work through them.
const failures = ref<ContrastFailure[]>([])
const notice = ref('')

function invalid(key: BrandColorKey): boolean {
  return !HEX_INPUT.test(form[key])
}
const formValid = computed(() => BRAND_COLOR_KEYS.every(k => !invalid(k)))

// Applies an API response to the page and to the whole site.
function show(raw: unknown) {
  const colors = brandColors(raw)
  if (colors) Object.assign(form, colors)
  const r = raw as { tokens?: Record<string, string>; is_default?: boolean }
  tokens.value = r.tokens ?? {}
  isDefault.value = r.is_default === true
  logo.value = parseBranding(raw)?.logo ?? null
  apply(raw)
}

const { run: load, pending: loading, error: loadError } = useAsyncAction(async () => {
  show(await api('/api/v1/branding'))
}, 'Huisstijl laden mislukt.')

onMounted(load)

// ─── Colours ──────────────────────────────────────────────────────────────────

const { run: save, pending: saving, error: saveError } = useAsyncAction(async () => {
  notice.value = ''
  try {
    show(await api('/api/v1/branding', {
      method: 'PUT',
      body: Object.fromEntries(BRAND_COLOR_KEYS.map(k => [k, form[k].toLowerCase()])),
    }))
  } catch (err: unknown) {
    const refused = contrastFailures(err)
    if (refused.length) {
      failures.value = refused
      throw new Error('contrast')
    }
    throw err
  }
  failures.value = []
  notice.value = 'Kleuren opgeslagen.'
}, 'Opslaan mislukt.')

const { run: resetColors, pending: resetting, error: resetError } = useAsyncAction(async () => {
  notice.value = ''
  show(await api('/api/v1/branding', { method: 'DELETE' }))
  failures.value = []
  notice.value = 'Standaardkleuren hersteld. Het logo is ongewijzigd.'
}, 'Herstellen mislukt.')

const colorError = computed(() =>
  failures.value.length ? 'Niet opgeslagen: het palet haalt het minimale contrast (WCAG AA) niet.' : saveError.value,
)

// Preview of the unsaved form, scoped to the preview box. Inline style
// bindings are allowed by the CSP (style-src-attr).
const previewStyle = computed(() => {
  const v = (k: BrandColorKey, fallback: string) => (invalid(k) ? fallback : form[k])
  return {
    '--preview-cta': v('cta', 'var(--brand-cta)'),
    '--preview-primary': v('primary', 'var(--brand-primary)'),
    '--preview-secondary': v('secondary', 'var(--brand-secondary)'),
    '--preview-surface': v('surface', 'var(--brand-surface)'),
    '--preview-body': v('body_text', 'var(--brand-body)'),
    '--preview-border': v('border', 'var(--brand-border)'),
  }
})

function pairLabel(pair: string) {
  return CONTRAST_PAIR_LABELS[pair] ?? pair
}

// ─── Logo ─────────────────────────────────────────────────────────────────────

const MAX_LOGO_BYTES = 512 * 1024

const fileInput = ref<HTMLInputElement | null>(null)
const logoFile = ref<File | null>(null)
const logoAlt = ref('')
const logoFileError = ref('')

function chooseLogo() {
  fileInput.value?.click()
}

function onLogoSelected(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0] ?? null
  logoFileError.value = ''
  logoFile.value = null
  if (!file) return
  if (file.type !== 'image/png') {
    logoFileError.value = 'Alleen PNG-bestanden zijn toegestaan.'
  } else if (file.size > MAX_LOGO_BYTES) {
    logoFileError.value = 'Het bestand is groter dan 512 KB.'
  } else {
    logoFile.value = file
  }
}

const logoValid = computed(() => Boolean(logoFile.value) && logoAlt.value.trim().length > 0 && logoAlt.value.length <= 255)

const { run: uploadLogo, pending: uploading, error: uploadError } = useAsyncAction(async () => {
  if (!logoFile.value) return
  notice.value = ''
  const body = new FormData()
  body.append('file', logoFile.value)
  body.append('alt', logoAlt.value.trim())
  show(await api('/api/v1/branding/logo', { method: 'POST', body }))
  logoFile.value = null
  logoAlt.value = ''
  if (fileInput.value) fileInput.value.value = ''
  notice.value = 'Logo opgeslagen.'
}, 'Logo uploaden mislukt. Gebruik een PNG van 16×16 tot 2000×800 pixels, maximaal 512 KB.')

const { run: deleteLogo, pending: deleting, error: deleteError } = useAsyncAction(async () => {
  notice.value = ''
  show(await api('/api/v1/branding/logo', { method: 'DELETE' }))
  notice.value = 'Logo verwijderd. De kopbalk toont weer het standaardbeeldmerk.'
}, 'Logo verwijderen mislukt.')
</script>

<template>
  <div class="flex flex-col gap-6">

    <div>
      <h1 class="brand-h1">Huisstijl</h1>
      <p class="brand-prose mt-2">
        Kleuren en logo voor deze hele installatie. Alleen beschikbaar voor platformbeheerders.
        Statuskleuren (groen, rood, oranje, geel) en grijstinten liggen vast.
      </p>
    </div>

    <template v-if="isPlatformAdmin">
      <p v-if="loadError" role="alert" class="m-0 bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700">
        {{ loadError }}
      </p>
      <p v-if="notice" role="status" class="m-0 bg-govnl-groen-50 border border-govnl-groen-300 rounded-card px-4 py-3 text-sm text-govnl-groen-700">
        {{ notice }}
      </p>

      <!-- Colours -->
      <section aria-labelledby="colors-heading" class="brand-card p-6 flex flex-col gap-5">
        <div class="flex items-center justify-between gap-4 flex-wrap">
          <h2 id="colors-heading" class="brand-h3">Kleuren</h2>
          <span class="text-sm text-govnl-grijs-600">{{ isDefault ? 'Standaardpalet' : 'Eigen palet' }}</span>
        </div>

        <!-- The contrast panel stays until a save succeeds. -->
        <div
          v-if="failures.length"
          role="alert"
          class="bg-govnl-rood-50 border border-govnl-rood-200 rounded-card px-4 py-3 text-sm text-govnl-rood-700"
        >
          <p class="m-0 mb-2 font-semibold flex items-center gap-2">
            <IconAlertTriangle :size="18" aria-hidden="true" />
            Niet opgeslagen: deze combinaties halen het minimale contrast van 4,5:1 (WCAG AA) niet.
          </p>
          <ul class="m-0 pl-5 flex flex-col gap-1">
            <li v-for="f in failures" :key="f.pair">
              {{ pairLabel(f.pair) }}: {{ f.ratio.toFixed(2).replace('.', ',') }}:1
              <span class="text-govnl-grijs-700">({{ f.foreground }} op {{ f.background }})</span>
            </li>
          </ul>
        </div>

        <form class="grid grid-cols-1 sm:grid-cols-2 gap-5" novalidate @submit.prevent="save()">
          <div v-for="field in COLOR_FIELDS" :key="field.key">
            <label :for="`brand-${field.key}`" class="brand-label">
              {{ field.label }}
            </label>
            <div class="flex items-center gap-2">
              <input
                :value="invalid(field.key) ? '#000000' : form[field.key].toLowerCase()"
                type="color"
                :aria-label="`${field.label}: kleurkiezer`"
                class="w-11 h-11 shrink-0 p-0.5 border border-govnl-grijs-500 rounded-card bg-white cursor-pointer"
                @input="form[field.key] = ($event.target as HTMLInputElement).value"
              />
              <input
                :id="`brand-${field.key}`"
                v-model.trim="form[field.key]"
                type="text"
                inputmode="text"
                maxlength="7"
                spellcheck="false"
                autocomplete="off"
                :aria-describedby="`brand-${field.key}-hint`"
                :aria-invalid="invalid(field.key) ? 'true' : undefined"
                class="brand-input flex-1 font-mono"
                :class="invalid(field.key) && form[field.key] && '!border-govnl-rood-500'"
              />
            </div>
            <p :id="`brand-${field.key}-hint`" class="brand-help">
              {{ invalid(field.key) && form[field.key] ? 'Gebruik de vorm #RRGGBB.' : field.hint }}
            </p>
          </div>

          <div class="sm:col-span-2 flex items-center gap-3 flex-wrap">
            <GovnlButton :icon="IconCheck" :disabled="!formValid || saving || loading" @click="save()">
              {{ saving ? 'Opslaan…' : 'Kleuren opslaan' }}
            </GovnlButton>
            <GovnlButton variant="secondary" :icon="IconRefresh" :disabled="resetting || loading" @click="resetColors()">
              Standaardkleuren herstellen
            </GovnlButton>
          </div>
          <p v-if="colorError || resetError" role="alert" class="sm:col-span-2 m-0 text-sm text-govnl-rood-700">
            {{ colorError || resetError }}
          </p>
        </form>

        <!-- Preview of the unsaved colours. It shows the form values, not the
             live brand layer, so it reads the --preview-* properties. -->
        <div>
          <h3 class="text-body font-bold text-brand-body m-0 mb-2">Voorbeeld</h3>
          <div
            class="rounded-card p-5 flex flex-col gap-3 border bg-[color:var(--preview-surface)] border-[color:var(--preview-border)]"
            :style="previewStyle"
          >
            <p class="m-0 text-[color:var(--preview-body)]">Lopende tekst in de tekstkleur op de achtergrondkleur.</p>
            <a href="#colors-heading" class="underline text-[color:var(--preview-primary)]">Een link in de primaire kleur</a>
            <div class="flex gap-3 flex-wrap">
              <span class="inline-block px-4 py-2 rounded-card font-semibold text-white bg-[color:var(--preview-cta)]">Actieknop</span>
              <span class="inline-block px-4 py-2 rounded-card font-semibold text-white bg-[color:var(--preview-primary)]">Primaire knop</span>
            </div>
            <p class="m-0 px-3 py-2 rounded-card bg-[color:var(--preview-secondary)] text-[color:var(--preview-primary)]">Primaire tekst op de secundaire kleur.</p>
          </div>
        </div>

        <!-- Derived shades of the saved palette -->
        <div v-if="tokens.cta_hover">
          <h3 class="text-body font-bold text-brand-body m-0 mb-1">Afgeleide tinten</h3>
          <p class="brand-help mt-0 mb-3">Berekend uit het opgeslagen palet. Niet apart in te stellen.</p>
          <dl class="grid grid-cols-2 sm:grid-cols-3 gap-3 m-0">
            <div v-for="d in DERIVED" :key="d.key">
              <dt class="text-sm text-govnl-grijs-700">{{ d.label }}</dt>
              <dd class="m-0 flex items-center gap-2 font-mono text-sm text-brand-body">
                <span
                  class="w-6 h-6 rounded-card border border-govnl-grijs-300 shrink-0"
                  :style="{ backgroundColor: tokens[d.key] }"
                  aria-hidden="true"
                />
                {{ tokens[d.key] }}
              </dd>
            </div>
          </dl>
        </div>
      </section>

      <!-- Logo -->
      <section aria-labelledby="logo-heading" class="brand-card p-6 flex flex-col gap-4">
        <h2 id="logo-heading" class="brand-h3">Logo</h2>
        <p class="m-0 text-sm text-govnl-grijs-600">
          Een eigen logo vervangt het Rijkslint-beeldmerk in de kopbalk. PNG, maximaal 512 KB,
          16×16 tot 2000×800 pixels. Gebruik op een publieke productie-installatie vraagt akkoord
          op afwijken van de Rijkshuisstijl.
        </p>

        <div v-if="logo" class="flex items-center gap-4 flex-wrap">
          <img
            :src="brandLogoSrc(logo)"
            :width="logo.width"
            :height="logo.height"
            :alt="logo.alt"
            class="block h-14 w-auto max-w-[280px] object-contain border border-brand-border rounded-card p-1"
          />
          <GovnlButton variant="danger" size="sm" :icon="IconTrash" :disabled="deleting" @click="deleteLogo()">
            Logo verwijderen
          </GovnlButton>
        </div>
        <p v-else class="m-0 text-sm text-govnl-grijs-700">Er is geen eigen logo ingesteld.</p>

        <div class="flex flex-col gap-3">
          <!-- The visible button opens this input, so the input itself is no
               separate tab stop and is hidden from assistive tech; it keeps
               an accessible name for tools that inspect it (#47). -->
          <input
            ref="fileInput"
            type="file"
            accept="image/png"
            class="sr-only"
            tabindex="-1"
            aria-hidden="true"
            aria-label="Logobestand (PNG)"
            @change="onLogoSelected"
          />
          <div class="flex items-center gap-3 flex-wrap">
            <GovnlButton variant="secondary" :icon="IconPhoto" @click="chooseLogo">Logobestand kiezen</GovnlButton>
            <span class="text-sm text-govnl-grijs-700" aria-live="polite">
              {{ logoFile ? logoFile.name : 'Geen bestand gekozen' }}
            </span>
          </div>
          <p v-if="logoFileError" role="alert" class="m-0 text-sm text-govnl-rood-700">{{ logoFileError }}</p>

          <div>
            <label for="brand-logo-alt" class="brand-label">Alternatieve tekst</label>
            <input
              id="brand-logo-alt"
              v-model="logoAlt"
              type="text"
              maxlength="255"
              aria-describedby="brand-logo-alt-hint"
              class="brand-input"
            />
            <p id="brand-logo-alt-hint" class="brand-help">
              Wat er op het logo staat, bijvoorbeeld de naam van de organisatie.
            </p>
          </div>

          <div>
            <!-- Navy, not the CTA: the colour save is this page's main action. -->
            <GovnlButton variant="navy" :icon="IconUpload" :disabled="!logoValid || uploading" @click="uploadLogo()">
              {{ uploading ? 'Uploaden…' : 'Logo opslaan' }}
            </GovnlButton>
          </div>
          <p v-if="uploadError || deleteError" role="alert" class="m-0 text-sm text-govnl-rood-700">
            {{ uploadError || deleteError }}
          </p>
        </div>
      </section>
    </template>

  </div>
</template>
