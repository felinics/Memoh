// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { Slots } from 'vue'

function translate(key: string) {
  return key
}

async function flushPromises() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: translate,
  }),
}))

vi.mock('vue-sonner', () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}))

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  return {
    useMutation: () => ({ mutateAsync: vi.fn(), isLoading: ref(false) }),
    useQueryCache: () => ({ invalidateQueries: vi.fn() }),
  }
})

vi.mock('@memohai/sdk', () => ({
  postProviders: vi.fn(),
  postProvidersByIdImportModels: vi.fn(),
}))

vi.mock('@/composables/useDialogMutation', () => ({
  useDialogMutation: () => ({ run: vi.fn() }),
}))

vi.mock('lucide-vue-next', () => ({
  Plus: () => h('span'),
}))

vi.mock('@/components/provider-icon/index.vue', () => ({
  default: () => h('span'),
}))

vi.mock('@/components/searchable-select-popover/index.vue', () => ({
  default: {
    props: ['modelValue', 'options', 'placeholder', 'searchPlaceholder', 'emptyText'],
    emits: ['update:modelValue'],
    setup(
      props: { modelValue?: string, options?: Array<{ value: string, label: string }> },
      { emit }: { emit: (event: 'update:modelValue', value: string) => void },
    ) {
      return () => h('select', {
        value: props.modelValue,
        onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value),
      }, (props.options ?? []).map(option => h('option', { value: option.value }, option.label)))
    },
  },
}))

vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const { Field } = await import('vee-validate')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const Input = Object.assign((
    props: { modelValue?: string },
    { attrs, emit }: { attrs: Record<string, unknown>, emit: (event: 'update:modelValue', value: string) => void },
  ) =>
    h('input', {
      ...attrs,
      value: props.modelValue ?? '',
      onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
    }), {
    emits: ['update:modelValue'],
  })
  const FormDialogShell = {
    props: ['open', 'title', 'cancelText', 'submitText', 'submitDisabled', 'loading'],
    emits: ['update:open', 'submit'],
    setup(props: { submitDisabled?: boolean }, { slots }: { slots: Slots }) {
      return () => h('div', [
        slots.body?.(),
        h('button', { 'type': 'submit', 'disabled': props.submitDisabled, 'data-testid': 'submit' }),
      ])
    },
  }
  return {
    Button: Passthrough,
    FormDialogShell,
    FORM_ITEM_INJECTION_KEY: Symbol('form-item'),
    // FieldStack moved into @felinic/ui (host file is a re-export shim) — the
    // mock must define it or the shim's import fails.
    FieldStack: Passthrough,
    Input,
    FormField: Field,
    FormControl: Passthrough,
    FormItem: Passthrough,
    Label: Passthrough,
    Switch: () => h('span'),
    Separator: () => h('hr'),
  }
})

describe('add provider dialog', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('enables submit after picking a preset and filling the api key', async () => {
    const AddProvider = (await import('./index.vue')).default
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(AddProvider, { open: true })
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    const submit = root.querySelector('[data-testid="submit"]') as HTMLButtonElement
    await vi.waitFor(() => {
      expect(submit.disabled).toBe(true)
    })

    const presetSelect = root.querySelector('select') as HTMLSelectElement
    presetSelect.value = 'openai'
    presetSelect.dispatchEvent(new Event('change', { bubbles: true }))
    await flushPromises()

    const apiKeyInput = root.querySelector('#provider-create-api-key') as HTMLInputElement
    apiKeyInput.value = 'sk-test'
    apiKeyInput.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => {
      expect(submit.disabled).toBe(false)
    })

    app.unmount()
    root.remove()
  })
})
