// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, type App } from 'vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdSettings: vi.fn().mockResolvedValue({ data: {} }),
  postBotsByBotIdSchedule: vi.fn().mockResolvedValue({}),
  putBotsByBotIdScheduleById: vi.fn().mockResolvedValue({}),
}))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: () => 'Save failed' }))
vi.mock('./schedule-execution-fields.vue', () => ({
  default: defineComponent({ setup(_, { expose }) { expose({ modelSatisfied: true }); return () => null } }),
}))
vi.mock('@felinic/ui', () => {
  const slot = defineComponent({ setup(_, { slots }) { return () => h('div', slots.default?.()) } })
  const field = (tag: string) => defineComponent({
    props: { modelValue: [String, Number] }, emits: ['update:modelValue'],
    setup(props, { emit, attrs }) {
      return () => h(tag, { ...attrs, value: props.modelValue, onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value) })
    },
  })
  return {
    Button: defineComponent({ setup(_, { attrs, slots }) { return () => h('button', attrs, slots.default?.()) } }),
    Input: field('input'), Textarea: field('textarea'),
    NumberField: defineComponent({
      props: { modelValue: { type: Number, default: 60 } }, emits: ['update:modelValue'],
      setup(props, { emit, attrs }) { return () => h('input', { ...attrs, value: props.modelValue, onInput: (event: Event) => emit('update:modelValue', Number((event.target as HTMLInputElement).value)) }) },
    }),
    DialogFooter: slot, SectionGroup: slot, Select: slot, SelectContent: slot, SelectItem: slot,
    SelectTrigger: slot, SelectValue: slot, SettingsRow: slot, SettingsSection: slot, Switch: slot, TimeInput: slot,
  }
})

import ScheduleEditor from './schedule-editor.vue'
import { postBotsByBotIdSchedule, putBotsByBotIdScheduleById } from '@memohai/sdk'
let app: App
let root: HTMLDivElement
afterEach(() => { app?.unmount(); root?.remove(); vi.clearAllMocks() })
async function mount(schedule?: Record<string, unknown>) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(ScheduleEditor, { botId: 'bot', mode: schedule ? 'edit' : 'create', schedule })
  app.mount(root)
  await nextTick()
}
async function fill(selector: string, value: string) {
  const field = root.querySelector<HTMLInputElement>(selector)!
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
}
async function submit() {
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await nextTick()
}
it('marks empty required fields only after submit and allows an empty description', async () => {
  await mount()
  expect(root.querySelector('[aria-invalid="true"]')).toBeNull()
  expect(root.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled).toBe(false)
  await submit()
  expect(root.querySelector('#sched-name')!.getAttribute('aria-invalid')).toBe('true')
  expect(root.querySelector('#sched-command')!.getAttribute('aria-invalid')).toBe('true')
  expect(root.querySelector('#sched-desc')!.getAttribute('aria-invalid')).toBeNull()
  expect(postBotsByBotIdSchedule).not.toHaveBeenCalled()
  await fill('#sched-name', 'Daily report')
  await fill('#sched-command', 'Summarize work')
  expect(root.querySelector('[aria-invalid="true"]')).toBeNull()
  await submit()
  await vi.waitFor(() => expect(postBotsByBotIdSchedule).toHaveBeenCalledWith(expect.objectContaining({ body: expect.objectContaining({ description: '', name: 'Daily report', max_run_seconds: 3600 }) })))
})
it('validates the current advanced cron after clearing a previously valid value', async () => {
  await mount({ id: 'schedule', name: 'Report', command: 'Summarize', pattern: '@daily' })
  await fill('input[placeholder="0 9 * * *"]', '')
  await submit()
  expect(root.querySelector('input[placeholder="0 9 * * *"]')!.getAttribute('aria-invalid')).toBe('true')
  expect(putBotsByBotIdScheduleById).not.toHaveBeenCalled()
})

it('hydrates the execution budget and sends it with updates', async () => {
  await mount({ id: 'schedule', name: 'Report', command: 'Summarize', pattern: '0 9 * * *', max_run_seconds: 7200 })
  expect(root.querySelector<HTMLInputElement>('#sched-max-run-minutes')!.value).toBe('120')
  await fill('#sched-max-run-minutes', '90')
  await submit()
  await vi.waitFor(() => expect(putBotsByBotIdScheduleById).toHaveBeenCalledWith(expect.objectContaining({ body: expect.objectContaining({ execution: expect.objectContaining({ max_run_seconds: 5400 }) }) })))
})
it('rejects an out-of-range execution budget', async () => {
  await mount({ id: 'schedule', name: 'Report', command: 'Summarize', pattern: '0 9 * * *' })
  await fill('#sched-max-run-minutes', '1441')
  await submit()
  await vi.waitFor(() => expect(root.textContent).toContain('bots.schedule.form.invalidMaxRunMinutes'))
  expect(putBotsByBotIdScheduleById).not.toHaveBeenCalled()
})
