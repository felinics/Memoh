// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import SchemaField, { type SchemaFieldSpec } from './schema-field.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (k: string) => k }) }))
vi.mock('@felinic/ui', async (importOriginal) => {
  const { defineComponent, h: rh } = await import('vue')
  return {
    ...await importOriginal<object>(),
    Switch: defineComponent({
      props: { modelValue: Boolean, disabled: Boolean },
      emits: ['update:modelValue'],
      setup: (p, { emit }) => () => rh('button', { type: 'button', role: 'switch', 'aria-checked': String(p.modelValue), disabled: p.disabled, onClick: () => emit('update:modelValue', !p.modelValue) }),
    }),
  }
})

let unmount = () => {}
afterEach(() => {
  unmount()
  document.body.innerHTML = ''
})

describe('SchemaField boolean default and explicit false handling', () => {
  it.each([
    { name: 'defaults to true when undefined and example === true, then emits false on toggle', field: { type: 'bool', title: 'Stream', example: true } as SchemaFieldSpec, initial: undefined, want: 'true', toggleTo: false },
    { name: 'preserves explicit false even when example === true', field: { type: 'bool', title: 'Stream', example: true } as SchemaFieldSpec, initial: false, want: 'false', toggleTo: undefined },
    { name: 'defaults to false when undefined and example is omitted', field: { type: 'bool', title: 'Auto Join' } as SchemaFieldSpec, initial: undefined, want: 'false', toggleTo: undefined },
  ])('$name', async ({ field, initial, want, toggleTo }) => {
    const valueRef = ref<unknown>(initial)
    const emitted: unknown[] = []
    const root = document.body.appendChild(document.createElement('div'))
    const app = createApp({
      render: () => h(SchemaField, { field, fieldKey: 'enableStreaming', idPrefix: 'f', modelValue: valueRef.value, 'onUpdate:modelValue': (v: unknown) => {
        emitted.push(v)
        valueRef.value = v
      } }),
    })
    app.mount(root)
    unmount = () => app.unmount()
    await nextTick()

    const toggle = root.querySelector<HTMLButtonElement>('[role="switch"]')!
    expect(toggle.getAttribute('aria-checked')).toBe(want)
    if (toggleTo !== undefined) {
      toggle.click()
      await nextTick()
      expect(emitted).toEqual([toggleTo])
      expect(toggle.getAttribute('aria-checked')).toBe(String(toggleTo))
    }
  })
})
