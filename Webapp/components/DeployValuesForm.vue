<script setup lang="ts">
interface SchemaProp {
  type?: string
  format?: string
  title?: string
  description?: string
  default?: unknown
  minimum?: number
  maximum?: number
  enum?: string[]
  properties?: Record<string, SchemaProp>
  required?: string[]
}

interface JsonSchema {
  type?: string
  required?: string[]
  properties?: Record<string, SchemaProp>
}

const props = defineProps<{
  schema: JsonSchema | null | undefined
  modelValues: Record<string, unknown>
  modelSecretInputs: Record<string, string>
  reconfigureMode?: boolean
}>()

const emit = defineEmits<{
  'update:modelValues': [v: Record<string, unknown>]
  'update:modelSecretInputs': [v: Record<string, string>]
}>()

// ─── Flat field descriptor ────────────────────────────────────────────────────

interface Field {
  path:        string[]   // e.g. ['image', 'tag']
  key:         string     // dot-joined path, e.g. 'image.tag'
  title:       string
  description: string
  type:        string
  isSecret:    boolean
  isRequired:  boolean
  default?:    unknown
  minimum?:    number
  maximum?:    number
  options:     string[]
  group?:      string     // parent label when nested, e.g. 'Image'
}

// Recursively flatten schema properties into a list of Field descriptors.
// Object-typed nodes become group separators; leaf nodes become inputs.
function flattenSchema(
  node: SchemaProp,
  path: string[] = [],
  required: string[] = [],
  group?: string,
): Field[] {
  if (!node.properties) return []
  return Object.entries(node.properties).flatMap(([key, prop]) => {
    const fullPath = [...path, key]
    const isRequired = required.includes(key)

    if (prop.type === 'object' && prop.properties) {
      // Recurse into nested object — pass its title as the group label
      return flattenSchema(prop, fullPath, prop.required ?? [], prop.title || key)
    }

    return [{
      path:        fullPath,
      key:         fullPath.join('.'),
      title:       prop.title || key,
      description: prop.description ?? '',
      type:        prop.type || 'string',
      isSecret:    prop.format === 'secret',
      isRequired,
      default:     prop.default,
      minimum:     prop.minimum,
      maximum:     prop.maximum,
      options:     prop.enum ?? [],
      group,
    }]
  })
}

const fields = computed(() => {
  if (!props.schema?.properties) return []
  return flattenSchema(props.schema as SchemaProp, [], props.schema.required ?? [])
})

const valueFields  = computed(() => fields.value.filter(f => !f.isSecret))
const secretFields = computed(() => fields.value.filter(f => f.isSecret))

// ─── Grouped rendering ────────────────────────────────────────────────────────

interface FieldGroup { label?: string; fields: Field[] }

const fieldGroups = computed((): FieldGroup[] => {
  const groups: Map<string, Field[]> = new Map()
  for (const f of valueFields.value) {
    const key = f.group ?? ''
    if (!groups.has(key)) groups.set(key, [])
    groups.get(key)!.push(f)
  }
  return Array.from(groups.entries()).map(([label, fields]) => ({
    label: label || undefined,
    fields,
  }))
})

// ─── Value helpers (nested path get/set) ─────────────────────────────────────

function getNestedValue(obj: Record<string, unknown>, path: string[]): unknown {
  return path.reduce<unknown>((acc, k) => (acc as Record<string, unknown>)?.[k], obj)
}

function setNestedValue(
  obj: Record<string, unknown>,
  path: string[],
  value: unknown,
): Record<string, unknown> {
  const [head, ...tail] = path
  if (tail.length === 0) return { ...obj, [head]: value }
  return {
    ...obj,
    [head]: setNestedValue((obj[head] as Record<string, unknown>) ?? {}, tail, value),
  }
}

function getValue(field: Field): string {
  const v = getNestedValue(props.modelValues, field.path)
  if (v !== undefined && v !== null) return String(v)
  return field.default !== undefined ? String(field.default) : ''
}

function setValue(field: Field, raw: string) {
  let parsed: unknown = raw
  if (field.type === 'integer')      parsed = raw === '' ? undefined : parseInt(raw, 10)
  else if (field.type === 'number')  parsed = raw === '' ? undefined : parseFloat(raw)
  else if (field.type === 'boolean') parsed = raw === 'true'
  emit('update:modelValues', setNestedValue(props.modelValues, field.path, parsed))
}

function getSecret(key: string): string {
  return props.modelSecretInputs[key] ?? ''
}

function setSecret(key: string, value: string) {
  emit('update:modelSecretInputs', { ...props.modelSecretInputs, [key]: value })
}
</script>

<template>
  <div v-if="!fields.length" class="rounded-card border border-brand-border bg-brand-surface px-4 py-3 text-center text-sm text-govnl-grijs-600">
    Geen instelbare waarden voor deze applicatie.
  </div>

  <div v-else class="flex flex-col gap-6">

    <!-- Non-secret value fields, grouped by parent object -->
    <template v-for="group in fieldGroups" :key="group.label ?? '__root__'">
      <div>
        <p v-if="group.label" class="mb-3 mt-0 text-sm font-bold uppercase tracking-[0.06em] text-govnl-grijs-600">
          {{ group.label }}
        </p>
        <div class="flex flex-col gap-4">
          <div v-for="field in group.fields" :key="field.key">
            <label :for="`field-${field.key}`" class="brand-label">
              {{ field.title }}
              <span v-if="field.isRequired" class="text-govnl-rood-600"> *</span>
            </label>

            <!-- Enum → select -->
            <select
              v-if="field.options.length"
              :id="`field-${field.key}`"
              :value="getValue(field)"
              class="brand-input"
              @change="setValue(field, ($event.target as HTMLSelectElement).value)"
            >
              <option v-for="opt in field.options" :key="opt" :value="opt">{{ opt }}</option>
            </select>

            <!-- Boolean → checkbox -->
            <div v-else-if="field.type === 'boolean'" class="flex items-center gap-2">
              <input
                :id="`field-${field.key}`"
                type="checkbox"
                :checked="getNestedValue(modelValues, field.path) === true"
                class="h-4 w-4 accent-[var(--brand-primary)]"
                @change="setValue(field, ($event.target as HTMLInputElement).checked ? 'true' : 'false')"
              />
              <span class="text-base text-brand-body">{{ field.description || field.title }}</span>
            </div>

            <!-- Number / text → input -->
            <input
              v-else
              :id="`field-${field.key}`"
              :type="field.type === 'integer' || field.type === 'number' ? 'number' : 'text'"
              :value="getValue(field)"
              :min="field.minimum"
              :max="field.maximum"
              :placeholder="field.default !== undefined ? String(field.default) : ''"
              class="brand-input"
              @input="setValue(field, ($event.target as HTMLInputElement).value)"
            />

            <p v-if="field.description && field.type !== 'boolean'" class="brand-help">
              {{ field.description }}
            </p>
          </div>
        </div>
      </div>
    </template>

    <!-- Secret fields -->
    <div v-if="secretFields.length" class="border-t border-brand-border pt-5">
      <p class="mb-4 mt-0 text-sm font-bold uppercase tracking-[0.06em] text-govnl-grijs-600">
        Secrets
        <span v-if="reconfigureMode" class="ml-1 font-normal normal-case text-govnl-grijs-600">— leeg laten om bestaande waarde te behouden</span>
      </p>
      <div v-for="field in secretFields" :key="field.key" class="mb-5 last:mb-0">
        <label :for="`secret-${field.key}`" class="brand-label">
          {{ field.title }}
          <span v-if="field.isRequired && !reconfigureMode" class="text-govnl-rood-600"> *</span>
        </label>
        <input
          :id="`secret-${field.key}`"
          type="password"
          autocomplete="new-password"
          :value="getSecret(field.key)"
          :placeholder="reconfigureMode ? '••••••••' : ''"
          class="brand-input"
          @input="setSecret(field.key, ($event.target as HTMLInputElement).value)"
        />
        <p v-if="field.description" class="brand-help">{{ field.description }}</p>
      </div>
    </div>

  </div>
</template>
