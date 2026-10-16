// @vitest-environment jsdom
import { createApp, h, nextTick, reactive, ref, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import FileViewer from './file-viewer.vue'

const sdk = vi.hoisted(() => ({ read: vi.fn(), write: vi.fn() }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdContainerFsRead: sdk.read,
  postBotsByBotIdContainerFsWrite: sdk.write,
  getBotsByBotIdContainerFs: vi.fn(),
  getBotsByBotIdContainerFsDownload: vi.fn(),
}))
vi.mock('@felinic/ui', () => ({
  toast, Button: 'button', DiffTitleBar: 'div', PanePlaceholder: 'div',
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/lib/api-client', () => ({ sdkApiUrl: vi.fn(), sdkAuthQuery: vi.fn() }))
vi.mock('@/composables/useKeyboardCommand', () => ({ useKeyboardCommand: vi.fn() }))
vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({
    fsChangedAt: ref(0), currentBotId: ref('bot-a'), bots: ref([]),
    affectsPath: () => false, fsEventForPath: () => null,
  }),
}))
vi.mock('@/components/monaco-editor/diff.vue', () => ({ default: 'div' }))
vi.mock('@/components/monaco-editor/index.vue', () => ({
  default: (props: { modelValue: string; 'onUpdate:modelValue': (value: string) => void }) => h('textarea', {
    value: props.modelValue,
    onInput: (event: Event) => props['onUpdate:modelValue']((event.target as HTMLTextAreaElement).value),
  }),
}))

function deferred() {
  let resolve!: (value: { data: { revision: string; content?: string } }) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<{ data: { revision: string; content?: string } }>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

let app: App
let host: HTMLDivElement
async function mountViewer() {
  const viewer = ref<InstanceType<typeof FileViewer>>()
  const props = reactive({ botId: 'bot-a', file: { path: '/a.txt', name: 'a.txt' } })
  const dirty = vi.fn()
  const saved = vi.fn()
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({
    setup: () => () => h(FileViewer, { ...props, ref: viewer, 'onUpdate:dirty': dirty, onSaved: saved }),
  })
  app.mount(host)
  await nextTick()
  await nextTick()
  return {
    props, dirty, saved,
    save: () => viewer.value!.save(),
    edit: async (value: string) => {
      const editor = host.querySelector('textarea')!
      editor.value = value
      editor.dispatchEvent(new Event('input', { bubbles: true }))
      await nextTick()
    },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  sdk.read.mockResolvedValue({ data: { content: 'initial', revision: 'r0' } })
  sdk.write.mockResolvedValue({ data: { revision: 'r1' } })
})
afterEach(() => {
  app?.unmount()
  host?.remove()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('file viewer save snapshot', () => {
  it('keeps edits during save dirty and saves their latest text with the confirmed revision', async () => {
    const view = await mountViewer()
    await view.edit('A')
    const pending = deferred()
    sdk.write.mockReturnValueOnce(pending.promise)
    const saving = view.save()
    expect(sdk.write).toHaveBeenLastCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-a' }, body: { path: '/a.txt', content: 'A', expectedRevision: 'r0' },
    }))
    await view.edit('B')
    pending.resolve({ data: { revision: 'r1' } })
    expect(await saving).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    expect(host.querySelector('textarea')!.value).toBe('B')
    expect(await view.save()).toBe(true)
    expect(sdk.write).toHaveBeenLastCalledWith(expect.objectContaining({
      body: { path: '/a.txt', content: 'B', expectedRevision: 'r1' },
    }))
    expect(view.dirty).toHaveBeenLastCalledWith(false)
  })

  it('protects an undo to the old baseline from closing while the submitted save is pending', async () => {
    const view = await mountViewer()
    await view.edit('A')
    const pending = deferred()
    sdk.write.mockReturnValueOnce(pending.promise)
    const saving = view.save()
    expect(await view.save()).toBe(false)
    expect(sdk.write).toHaveBeenCalledOnce()
    await view.edit('initial')
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    expect(await view.save()).toBe(false)
    pending.resolve({ data: { revision: 'r1' } })
    expect(await saving).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    await view.edit('A')
    expect(view.dirty).toHaveBeenLastCalledWith(false)
  })

  it('becomes clean after undoing to the old baseline when the pending save fails', async () => {
    const view = await mountViewer()
    await view.edit('A')
    const pending = deferred()
    sdk.write.mockReturnValueOnce(pending.promise)
    const saving = view.save()
    await view.edit('initial')
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    pending.reject(new Error('offline'))
    expect(await saving).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(false)
    expect(await view.save()).toBe(true)
    expect(sdk.write).toHaveBeenCalledOnce()
  })

  it.each(['path', 'bot'] as const)('cancels a pending external poll when changing %s', async (change) => {
    vi.useFakeTimers()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const view = await mountViewer()
    const pending = deferred()
    sdk.read.mockReturnValueOnce(pending.promise)
    await vi.advanceTimersByTimeAsync(2_000)
    expect(sdk.read).toHaveBeenCalledTimes(2)
    const pollSignal: AbortSignal = sdk.read.mock.calls[1]![0].signal
    sdk.read.mockResolvedValue({ data: { content: 'other', revision: 'other-r0' } })
    if (change === 'bot') view.props.botId = 'bot-b'
    else view.props.file = { path: '/b.txt', name: 'b.txt' }
    await nextTick()
    await nextTick()
    expect(pollSignal.aborted).toBe(true)
    pending.resolve({ data: { content: 'old polled text', revision: 'old-r1' } })
    await nextTick()
    await nextTick()
    expect(host.querySelector('textarea')!.value).toBe('other')
    await view.edit('new draft')
    await view.save()
    expect(sdk.write).toHaveBeenLastCalledWith(expect.objectContaining({
      body: { path: view.props.file.path, content: 'new draft', expectedRevision: 'other-r0' },
    }))
  })

  it('preserves the conflict notice on a 409 response', async () => {
    const view = await mountViewer()
    await view.edit('A')
    sdk.write.mockRejectedValueOnce({ status: 409 })
    expect(await view.save()).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    expect(host.textContent).toContain('bots.files.externalChange.reloadFailed')
    expect(toast.error).toHaveBeenCalledWith('bots.files.externalChange.saveConflict')
  })

  it('ignores an old file conflict response after switching files', async () => {
    const view = await mountViewer()
    await view.edit('A')
    const pending = deferred()
    sdk.write.mockReturnValueOnce(pending.promise)
    const saving = view.save()
    view.props.file = { path: '/b.txt', name: 'b.txt' }
    await nextTick()
    await nextTick()
    await view.edit('B')
    pending.reject({ status: 409 })
    expect(await saving).toBe(false)
    expect(toast.error).not.toHaveBeenCalled()
    expect(host.querySelector('[role="status"]')).toBeNull()
    expect(await view.save()).toBe(true)
  })

  it('allows closing after an ordinary successful save', async () => {
    const view = await mountViewer()
    await view.edit('A')
    expect(await view.save()).toBe(true)
    expect(view.dirty).toHaveBeenLastCalledWith(false)
    expect(view.saved).toHaveBeenCalledOnce()
    expect(await view.save()).toBe(true)
    expect(sdk.write).toHaveBeenCalledOnce()
  })

  it('does not allow closing after a failed save or advance its revision', async () => {
    const view = await mountViewer()
    await view.edit('A')
    sdk.write.mockRejectedValueOnce(new Error('offline'))
    expect(await view.save()).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    expect(view.saved).not.toHaveBeenCalled()
    await view.save()
    expect(sdk.write).toHaveBeenLastCalledWith(expect.objectContaining({
      body: { path: '/a.txt', content: 'A', expectedRevision: 'r0' },
    }))
  })

  it.each(['success', 'failure'] as const)('keeps a new clean file clean when an old save settles with %s', async (outcome) => {
    const view = await mountViewer()
    await view.edit('A')
    const pending = deferred()
    sdk.write.mockReturnValueOnce(pending.promise)
    const saving = view.save()
    view.props.file = { path: '/b.txt', name: 'b.txt' }
    await nextTick()
    await nextTick()
    expect(view.dirty).toHaveBeenLastCalledWith(false)
    if (outcome === 'success') pending.resolve({ data: { revision: 'old-r1' } })
    else pending.reject(new Error('offline'))
    expect(await saving).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(false)
    expect(host.querySelector('textarea')!.value).toBe('initial')
    expect(await view.save()).toBe(true)
    expect(sdk.write).toHaveBeenCalledOnce()
  })

  it.each(['path', 'bot', 'return'] as const)('ignores a completed save after changing %s', async (change) => {
    const view = await mountViewer()
    await view.edit('A')
    const pending = deferred()
    sdk.write.mockReturnValueOnce(pending.promise)
    const saving = view.save()
    sdk.read.mockResolvedValue({ data: { content: 'other', revision: 'other-r0' } })
    if (change === 'bot') view.props.botId = 'bot-b'
    else view.props.file = { path: '/b.txt', name: 'b.txt' }
    await nextTick()
    await nextTick()
    if (change === 'return') {
      view.props.file = { path: '/a.txt', name: 'a.txt' }
      await nextTick()
      await nextTick()
    }
    await view.edit('new draft')
    pending.resolve({ data: { revision: 'old-r1' } })
    expect(await saving).toBe(false)
    expect(view.dirty).toHaveBeenLastCalledWith(true)
    expect(view.saved).not.toHaveBeenCalled()
    await view.save()
    expect(sdk.write).toHaveBeenLastCalledWith(expect.objectContaining({
      path: { bot_id: view.props.botId },
      body: { path: view.props.file.path, content: 'new draft', expectedRevision: 'other-r0' },
    }))
  })
})
