// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
// Failure Modes Covered:
// 1. Deep `watch(modelValue)` on `<SchemaField>` headers re-emitting infinitely
//    or clobbering an in-progress row whose value is still `""`.
// 2. Clicking "Add header" appends `{ key: "", value: "" }`; `pairsToRecord`
//    strips blank keys from the emitted `Record<string, string>`, and the parent's
//    v-model echo must NOT wipe the newly added blank row before the user types a key.
// 3. Header CRUD (rename key, edit password-masked value, delete row) and external
//    parent resets (`headers.value = ...`) must stay synchronized in both directions.
// 4. Schema-driven `required` field with `example` renders `example` as placeholder
//    with an empty value, `*_seconds` passes `min="1"`, and `enum` renders options
//    and emits selected values.

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import SchemaField, { type SchemaFieldSpec } from '@/components/config-schema-form/schema-field.vue'
import { normalizeProviderConfigFields, providerConfigDefaults } from '@/utils/provider-template'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

// Lightweight interactive stand-ins keep assertions on SchemaField + KeyValueEditor behavior.
vi.mock('@felinic/ui', async () => {
  const { defineComponent, h, inject, provide } = await import('vue')
  const SELECT_EMIT = Symbol('select-emit')
  const passthrough = (tag: string) => defineComponent({
    inheritAttrs: false,
    setup: (_, { attrs, slots }) => () => h(tag, attrs, [slots.content?.(), slots.default?.()]),
  })
  const Input = defineComponent({
    inheritAttrs: false,
    props: { modelValue: { type: [String, Number], default: '' } },
    emits: ['update:modelValue'],
    setup: (props, { attrs, emit }) => () => h('input', {
      ...attrs,
      value: props.modelValue ?? '',
      onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
    }),
  })
  const NumberField = defineComponent({
    inheritAttrs: false,
    props: {
      modelValue: { type: Number, default: undefined },
      min: { type: Number, default: undefined },
      max: { type: Number, default: undefined },
      step: { type: Number, default: undefined },
    },
    emits: ['update:modelValue'],
    setup: (props, { attrs, emit }) => () => h('input', {
      ...attrs,
      'type': 'number',
      'data-min': props.min,
      'data-max': props.max,
      'data-step': props.step,
      'value': props.modelValue ?? '',
      'onInput': (event: Event) => {
        const raw = (event.target as HTMLInputElement).value
        emit('update:modelValue', raw === '' ? undefined : Number(raw))
      },
    }),
  })
  const Select = defineComponent({
    inheritAttrs: false,
    props: {
      modelValue: { type: String, default: '' },
      disabled: { type: Boolean, default: false },
    },
    emits: ['update:modelValue'],
    setup: (props, { slots, emit }) => {
      provide(SELECT_EMIT, (val: string) => emit('update:modelValue', val))
      return () => h('div', {
        'data-select-value': props.modelValue,
        'data-disabled': props.disabled,
      }, slots.default?.())
    },
  })
  const SelectItem = defineComponent({
    inheritAttrs: false,
    props: { value: { type: String, required: true } },
    setup: (props, { slots }) => {
      const onSelect = inject<(val: string) => void>(SELECT_EMIT)
      return () => h('button', {
        'type': 'button',
        'data-enum-option': props.value,
        'onClick': () => onSelect?.(props.value),
      }, slots.default?.())
    },
  })
  return {
    Button: passthrough('button'),
    Input,
    InputGroup: passthrough('div'),
    InputGroupAddon: passthrough('div'),
    InputGroupButton: passthrough('button'),
    InputGroupInput: Input,
    Label: passthrough('label'),
    NumberField,
    Select,
    SelectContent: passthrough('div'),
    SelectItem,
    SelectTrigger: passthrough('button'),
    SelectValue: passthrough('span'),
    SettingsRow: passthrough('div'),
    Switch: passthrough('button'),
    Textarea: passthrough('textarea'),
  }
})

let app: App | undefined
let root: HTMLDivElement
let errors: unknown[] = []

async function flush() {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

function mountField<T>(fieldKey: string, field: SchemaFieldSpec, initial: T) {
  const model = ref<unknown>(initial)
  const Parent = defineComponent({
    setup: () => () => h(SchemaField, {
      field,
      fieldKey,
      'idPrefix': 'searxng',
      'modelValue': model.value,
      'onUpdate:modelValue': (value: unknown) => { model.value = value },
    }),
  })
  root = document.createElement('div')
  document.body.appendChild(root)
  errors = []
  app = createApp(Parent)
  app.config.globalProperties.$t = (key: string) => key
  app.config.errorHandler = error => { errors.push(error) }
  app.config.warnHandler = message => { if (message.includes('recursive')) errors.push(message) }
  app.mount(root)
  return model as ReturnType<typeof ref<T>>
}

afterEach(() => {
  app?.unmount()
  root?.remove()
  app = undefined
})

function headerNames(): string[] {
  return [...root.querySelectorAll<HTMLInputElement>('input[aria-label="webSearch.headerName"]')].map(input => input.value)
}

function headerValueInputs(): HTMLInputElement[] {
  return [...root.querySelectorAll<HTMLInputElement>('input[aria-label="webSearch.headerValue"]')]
}

describe('SchemaField dynamic provider controls & SearXNG headers', () => {
  it('settles a v-model round trip instead of re-emitting forever and masks header values as password', async () => {
    const headers = mountField('headers', { type: 'object', title: 'HTTP headers' }, { Authorization: '' })
    await flush()
    expect(headerNames()).toEqual(['Authorization'])

    const [valueInput] = headerValueInputs()
    expect(valueInput?.getAttribute('type')).toBe('password')
    valueInput!.value = 'Bearer typed'
    valueInput!.dispatchEvent(new Event('input'))
    await flush()

    expect(errors).toEqual([])
    expect(headerNames()).toEqual(['Authorization'])
    expect(headers.value).toEqual({ Authorization: 'Bearer typed' })
  })

  it('preserves newly added blank header rows across v-model echo, supports editing & deleting rows, and syncs external resets', async () => {
    const headers = mountField<Record<string, string>>('headers', { type: 'object', title: 'HTTP headers' }, { Authorization: 'Bearer saved' })
    await flush()

    const add = [...root.querySelectorAll('button')].find(button => button.textContent?.includes('webSearch.addHeader'))
    expect(add).toBeDefined()
    add!.click()
    await flush()

    // Blank draft row stays mounted while omitted from emitted Record<string, string>
    expect(errors).toEqual([])
    expect(headerNames()).toEqual(['Authorization', ''])
    expect(headers.value).toEqual({ Authorization: 'Bearer saved' })

    const nameInputs = root.querySelectorAll<HTMLInputElement>('input[aria-label="webSearch.headerName"]')
    nameInputs[1]!.value = 'X-Custom-Header-2'
    nameInputs[1]!.dispatchEvent(new Event('input'))
    await flush()
    expect(headers.value).toEqual({ 'Authorization': 'Bearer saved', 'X-Custom-Header-2': '' })

    headerValueInputs()[1]!.value = 'val-2'
    headerValueInputs()[1]!.dispatchEvent(new Event('input'))
    await flush()
    expect(headers.value).toEqual({ 'Authorization': 'Bearer saved', 'X-Custom-Header-2': 'val-2' })

    // Delete first row -> only second header remains
    const deleteButtons = root.querySelectorAll<HTMLButtonElement>('button[aria-label="common.delete"]')
    expect(deleteButtons).toHaveLength(2)
    deleteButtons[0]!.click()
    await flush()
    expect(headerNames()).toEqual(['X-Custom-Header-2'])
    expect(headers.value).toEqual({ 'X-Custom-Header-2': 'val-2' })

    // External reset from parent (e.g. switching provider or reverting draft) replaces rows
    headers.value = { 'X-Reset': 'ok' }
    await flush()
    expect(headerNames()).toEqual(['X-Reset'])
  })

  it('renders required base_url example only as placeholder, enforces min=1 on timeout_seconds, and binds enum options', async () => {
    const rawSchema = {
      fields: {
        base_url: { type: 'string', required: true, title: 'Base URL', example: 'http://localhost:8080' },
        safesearch: { type: 'string', title: 'SafeSearch', enum: ['0', '1', '2'], example: '1' },
        timeout_seconds: { type: 'number', title: 'Timeout', example: 15 },
      },
    }
    const fields = normalizeProviderConfigFields(rawSchema)
    const defaults = providerConfigDefaults(rawSchema)

    // 1) Required base_url: empty value, placeholder uses example
    const baseUrlField = fields.find(f => f.key === 'base_url')!
    const baseUrlVal = mountField('base_url', baseUrlField, defaults.base_url)
    await flush()
    const urlInput = root.querySelector<HTMLInputElement>('input#searxng-base_url')!
    expect(urlInput.value).toBe('')
    expect(urlInput.getAttribute('placeholder')).toBe('http://localhost:8080')
    expect(baseUrlVal.value).toBeUndefined()
    app?.unmount()
    root.remove()

    // 2) Enum safesearch: renders all options and emits chosen value
    const enumField = fields.find(f => f.key === 'safesearch')!
    const enumVal = mountField('safesearch', enumField, defaults.safesearch)
    await flush()
    const options = [...root.querySelectorAll<HTMLButtonElement>('[data-enum-option]')]
    expect(options.map(el => el.dataset.enumOption)).toEqual(['0', '1', '2'])
    options[2]!.click()
    await flush()
    expect(enumVal.value).toBe('2')
    app?.unmount()
    root.remove()

    // 3) timeout_seconds: hydrated to 15 with min=1 constraint
    const timeoutField = fields.find(f => f.key === 'timeout_seconds')!
    const timeoutVal = mountField('timeout_seconds', timeoutField, defaults.timeout_seconds)
    await flush()
    const numInput = root.querySelector<HTMLInputElement>('input#searxng-timeout_seconds')!
    expect(numInput.value).toBe('15')
    expect(numInput.dataset.min).toBe('1')
    expect(timeoutVal.value).toBe(15)
  })
})
