// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, Fragment, h, nextTick, reactive, type App, type Slots } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import FolderCreateDialog from './folder-create-dialog.vue'

const api = vi.hoisted(() => ({ targets: vi.fn(), mkdir: vi.fn(), create: vi.fn(), refresh: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdWorkspaceTargets: api.targets,
  postBotsByBotIdContainerFsMkdir: api.mkdir,
}))
vi.mock('@/composables/api/useWorkdirs', () => ({ createWorkdir: api.create }))
vi.mock('@/store/workdirs', () => ({ useWorkdirsStore: () => ({ refreshWorkdirs: api.refresh }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/file-manager/directory-picker.vue', () => ({ default: () => h('div') }))
vi.mock('@felinic/ui', () => {
  const Wrapper = (_props: unknown, { slots }: { slots: Slots }) => h(Fragment, slots.default?.())
  return {
    FormDialogShell: defineComponent({
      props: { open: Boolean, submitDisabled: Boolean, loading: Boolean },
      emits: ['submit', 'update:open'],
      setup: (props, { slots, emit }) => () => props.open ? h('div', [
        slots.body?.(),
        h('button', { disabled: props.submitDisabled || props.loading, onClick: () => emit('submit') }, 'Create'),
      ]) : null,
    }),
    FormStack: Wrapper, FieldStack: Wrapper, SelectContent: Wrapper,
    SelectTrigger: () => null, SelectValue: () => null,
    Input: defineComponent({
      props: { modelValue: { type: String, default: '' } },
      emits: ['update:modelValue'],
      setup: (props, { attrs, emit }) => () => h('input', {
        ...attrs, value: props.modelValue,
        onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
      }),
    }),
    Select: defineComponent({
      props: { modelValue: { type: String, default: '' } },
      emits: ['update:modelValue'],
      setup: (props, { slots, emit }) => () => h('select', {
        value: props.modelValue,
        onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value),
      }, slots.default?.()),
    }),
    SelectItem: defineComponent({
      props: { value: { type: String, default: '' } },
      setup: (props, { slots }) => () => h('option', { value: props.value }, slots.default?.()),
    }),
    toast: { success: vi.fn(), error: vi.fn() },
  }
})

let app: App | undefined
let root: HTMLDivElement
async function flush() {
  await vi.advanceTimersByTimeAsync(0)
  await nextTick()
}
async function mount() {
  const props = reactive({ botId: 'bot-1', open: true })
  root = document.createElement('div')
  document.body.appendChild(root)
  app = createApp(() => h(FolderCreateDialog, {
    ...props, 'onUpdate:open': (value: boolean) => { props.open = value },
  }))
  app.use(createPinia()).use(PiniaColada)
  app.mount(root)
  await flush()
  return props
}
function input(id: string, value: string) {
  const field = root.querySelector<HTMLInputElement>(`#${id}`)!
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
}
function selectTarget(value: string) {
  const select = root.querySelector('select')!
  select.value = value
  select.dispatchEvent(new Event('change', { bubbles: true }))
}
function submit() {
  root.querySelector('button')!.click()
}
beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  api.targets.mockResolvedValue({ data: { targets: [
    { target_id: 'native', kind: 'native', primary: false, online: true },
    { target_id: 'remote-1', kind: 'remote', primary: true, online: true },
  ] } })
  api.mkdir.mockResolvedValue({ data: { ok: true } })
  api.create.mockResolvedValue({ id: 'workdir-1' })
  api.refresh.mockResolvedValue(undefined)
})
afterEach(() => {
  app?.unmount()
  root?.remove()
  app = undefined
  vi.useRealTimers()
})

describe('folder creation target', () => {
  it('creates a native directory explicitly even when the Primary is remote', async () => {
    await mount()
    input('folder-name', 'project')
    await flush()
    submit()
    await flush()
    expect(api.mkdir).toHaveBeenCalledWith({
      path: { bot_id: 'bot-1' },
      body: { path: '/data/project', workspace_target_id: 'native' },
      throwOnError: true,
    })
    expect(api.create).toHaveBeenCalledWith('bot-1', { name: 'project', path: '/data/project', workspaceTargetId: 'native' })
  })

  it('keeps the submitted bot, target, name and path while mkdir is pending', async () => {
    let completeMkdir!: () => void
    api.mkdir.mockImplementation(() => new Promise<void>((resolve) => { completeMkdir = resolve }))
    const props = await mount()
    input('folder-name', 'original')
    await flush()
    submit()
    await flush()
    selectTarget('remote-1')
    input('folder-name', 'changed')
    await flush()
    input('folder-path', '/home/other')
    props.botId = 'bot-2'
    await flush()
    completeMkdir()
    await flush()
    expect(api.create).toHaveBeenCalledWith('bot-1', { name: 'original', path: '/data/original', workspaceTargetId: 'native' })
    expect(api.refresh).toHaveBeenCalledWith('bot-1')
  })

  it('registers an existing remote directory without calling mkdir', async () => {
    await mount()
    selectTarget('remote-1')
    await flush()
    input('folder-name', 'Remote project')
    input('folder-path', '/home/project')
    await flush()
    submit()
    await flush()
    expect(api.mkdir).not.toHaveBeenCalled()
    expect(api.create).toHaveBeenCalledWith('bot-1', { name: 'Remote project', path: '/home/project', workspaceTargetId: 'remote-1' })
  })

  it('does not register a workdir when directory creation fails', async () => {
    api.mkdir.mockRejectedValue(new Error('workspace unreachable'))
    const props = await mount()
    input('folder-name', 'project')
    await flush()
    submit()
    await flush()
    expect(api.create).not.toHaveBeenCalled()
    expect(api.refresh).not.toHaveBeenCalled()
    expect(props.open).toBe(true)
    expect(root.querySelector('button')!.disabled).toBe(false)
  })
})
