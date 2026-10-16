// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import SearxngSettings from './searxng-settings.vue'

// Plain stand-ins keep the test on this component's own v-model wiring.
vi.mock('@felinic/ui', async () => {
  const { defineComponent, h } = await import('vue')
  const passthrough = (tag: string) => defineComponent({
    inheritAttrs: false,
    setup: (_, { attrs, slots }) => () => h(tag, attrs, slots.default?.()),
  })
  const Input = defineComponent({
    inheritAttrs: false,
    props: { modelValue: { type: [String, Number], default: '' } },
    emits: ['update:modelValue'],
    setup: (props, { attrs, emit }) => () => h('input', {
      ...attrs,
      value: props.modelValue,
      onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
    }),
  })
  return { Button: passthrough('button'), Input, NumberField: Input, SettingsRow: passthrough('div') }
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

function mount(initial: Record<string, unknown>) {
  const config = ref<Record<string, unknown>>(initial)
  const Parent = defineComponent({
    setup: () => () => h(SearxngSettings, {
      'modelValue': config.value,
      'onUpdate:modelValue': (value: Record<string, unknown>) => { config.value = value },
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
  return config
}

afterEach(() => {
  app?.unmount()
  root?.remove()
  app = undefined
})

function headerNames(): string[] {
  return [...root.querySelectorAll<HTMLInputElement>('input[aria-label="webSearch.headerName"]')].map(input => input.value)
}

it('settles a v-model round trip instead of re-emitting forever', async () => {
  const config = mount({})
  await flush()
  expect(headerNames()).toEqual(['Authorization'])

  const value = root.querySelector<HTMLInputElement>('input[aria-label="webSearch.headerValue"]')!
  value.value = 'Bearer typed'
  value.dispatchEvent(new Event('input'))
  await flush()

  expect(errors).toEqual([])
  expect(headerNames()).toEqual(['Authorization'])
  expect(config.value.headers).toEqual({ Authorization: 'Bearer typed' })
})

it('adds a header row through the parent v-model', async () => {
  const config = mount({ headers: { Authorization: 'Bearer saved' } })
  await flush()

  const add = [...root.querySelectorAll('button')].find(button => button.textContent?.includes('webSearch.addHeader'))
  expect(add).toBeDefined()
  add!.click()
  await flush()

  expect(errors).toEqual([])
  expect(headerNames()).toEqual(['Authorization', 'X-Custom-Header-2'])
  expect(config.value.headers).toEqual({ 'Authorization': 'Bearer saved', 'X-Custom-Header-2': '' })
})
