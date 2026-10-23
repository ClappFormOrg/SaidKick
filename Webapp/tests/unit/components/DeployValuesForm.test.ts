import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DeployValuesForm from '~/components/DeployValuesForm.vue'

const SCHEMA = {
  type: 'object',
  required: ['name'],
  properties: {
    name:     { type: 'string', title: 'Naam' },
    replicas: { type: 'integer', title: 'Replicas', default: 1, minimum: 1, maximum: 5 },
    enabled:  { type: 'boolean', title: 'Ingeschakeld' },
    mode:     { type: 'string', title: 'Modus', enum: ['a', 'b'] },
    apiKey:   { type: 'string', format: 'secret', title: 'API-sleutel' },
    image: {
      type: 'object', title: 'Image',
      properties: { tag: { type: 'string', title: 'Tag', default: 'latest' } },
    },
  },
}

function mountForm(props: Record<string, unknown> = {}) {
  return mount(DeployValuesForm, {
    props: { schema: SCHEMA, modelValues: {}, modelSecretInputs: {}, ...props },
  })
}

describe('DeployValuesForm — empty / unhappy schema', () => {
  it('shows the empty-state message when schema is null', () => {
    const w = mount(DeployValuesForm, {
      props: { schema: null, modelValues: {}, modelSecretInputs: {} },
    })
    expect(w.text()).toContain('Geen instelbare waarden')
  })

  it('shows the empty-state message when schema has no properties', () => {
    const w = mount(DeployValuesForm, {
      props: { schema: { type: 'object' }, modelValues: {}, modelSecretInputs: {} },
    })
    expect(w.text()).toContain('Geen instelbare waarden')
  })
})

describe('DeployValuesForm — field rendering (happy path)', () => {
  it('renders a text input for string fields', () => {
    const w = mountForm()
    expect(w.find('#field-name').exists()).toBe(true)
  })

  it('marks required fields with an asterisk', () => {
    const w = mountForm()
    const nameLabel = w.get('label[for="field-name"]')
    expect(nameLabel.text()).toContain('*')
  })

  it('renders a <select> for enum fields with all options', () => {
    const w = mountForm()
    const select = w.get('select#field-mode')
    expect(select.findAll('option')).toHaveLength(2)
  })

  it('renders a checkbox for boolean fields', () => {
    const w = mountForm()
    expect(w.get('#field-enabled').attributes('type')).toBe('checkbox')
  })

  it('renders a number input for integer fields with min/max', () => {
    const w = mountForm()
    const input = w.get('#field-replicas')
    expect(input.attributes('type')).toBe('number')
    expect(input.attributes('min')).toBe('1')
    expect(input.attributes('max')).toBe('5')
  })

  it('groups nested object fields under their title and uses the dotted id', () => {
    const w = mountForm()
    expect(w.text()).toContain('Image')
    expect(w.find('#field-image\\.tag').exists()).toBe(true)
  })

  it('renders secret fields separately as password inputs', () => {
    const w = mountForm()
    const secret = w.get('#secret-apiKey')
    expect(secret.attributes('type')).toBe('password')
    expect(w.text()).toContain('Secrets')
  })

  it('shows the default value as the initial input value when no model value set', () => {
    const w = mountForm()
    expect((w.get('#field-replicas').element as HTMLInputElement).value).toBe('1')
    expect((w.get('#field-image\\.tag').element as HTMLInputElement).value).toBe('latest')
  })

  it('renders a field description as helper text', () => {
    const schema = {
      type: 'object',
      properties: {
        host: { type: 'string', title: 'Host', description: 'De volledige hostnaam van de dienst.' },
      },
    }
    const w = mount(DeployValuesForm, { props: { schema, modelValues: {}, modelSecretInputs: {} } })
    expect(w.text()).toContain('De volledige hostnaam van de dienst.')
  })
})

describe('DeployValuesForm — value emission (happy + type parsing)', () => {
  it('emits a string value on text input', async () => {
    const w = mountForm()
    await w.get('#field-name').setValue('mijn-app')
    const ev = w.emitted('update:modelValues')
    expect(ev?.at(-1)?.[0]).toEqual({ name: 'mijn-app' })
  })

  it('parses integer fields to numbers', async () => {
    const w = mountForm()
    await w.get('#field-replicas').setValue('3')
    expect(w.emitted('update:modelValues')?.at(-1)?.[0]).toEqual({ replicas: 3 })
  })

  it('parses an empty numeric field to undefined (unhappy: cleared input)', async () => {
    const w = mountForm()
    await w.get('#field-replicas').setValue('')
    expect(w.emitted('update:modelValues')?.at(-1)?.[0]).toEqual({ replicas: undefined })
  })

  it('parses boolean checkboxes', async () => {
    const w = mountForm()
    const box = w.get('#field-enabled')
    ;(box.element as HTMLInputElement).checked = true
    await box.trigger('change')
    expect(w.emitted('update:modelValues')?.at(-1)?.[0]).toEqual({ enabled: true })
  })

  it('sets nested object values immutably at the dotted path', async () => {
    const w = mountForm()
    await w.get('#field-image\\.tag').setValue('v2')
    expect(w.emitted('update:modelValues')?.at(-1)?.[0]).toEqual({ image: { tag: 'v2' } })
  })

  it('emits secret inputs on the secret channel', async () => {
    const w = mountForm()
    await w.get('#secret-apiKey').setValue('s3cr3t')
    expect(w.emitted('update:modelSecretInputs')?.at(-1)?.[0]).toEqual({ apiKey: 's3cr3t' })
  })
})

describe('DeployValuesForm — reconfigure mode', () => {
  it('drops the required asterisk on secrets and shows a keep-existing hint', () => {
    const schema = {
      type: 'object', required: ['apiKey'],
      properties: { apiKey: { type: 'string', format: 'secret', title: 'API-sleutel' } },
    }
    const w = mount(DeployValuesForm, {
      props: { schema, modelValues: {}, modelSecretInputs: {}, reconfigureMode: true },
    })
    expect(w.text()).toContain('leeg laten om bestaande waarde te behouden')
    expect(w.get('label[for="secret-apiKey"]').text()).not.toContain('*')
  })
})
