// @vitest-environment jsdom
import { KeepAlive, createApp, h, nextTick, reactive, ref, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import FileViewer from './file-viewer.vue'

const sdk = vi.hoisted(() => ({ read: vi.fn(), write: vi.fn(), fs: vi.fn() }))
const bounded = vi.hoisted(() => ({ fetchBoundedBlob: vi.fn() }))
const chatState = vi.hoisted(() => ({
  fsChangedAt: null as unknown as ReturnType<typeof ref<number>>,
  affectsPath: (_path: string): boolean => false,
}))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdContainerFsRead: sdk.read,
  postBotsByBotIdContainerFsWrite: sdk.write,
  getBotsByBotIdContainerFs: sdk.fs,
  getBotsByBotIdContainerFsDownload: vi.fn(),
}))
vi.mock('@/lib/bounded-fetch', () => ({
  fetchBoundedBlob: bounded.fetchBoundedBlob,
}))
vi.mock('@felinic/ui', () => ({
  toast, Button: 'button', DiffTitleBar: 'div', PanePlaceholder: 'div',
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, args?: Record<string, unknown>) =>
      args?.size ? `${key}:${String(args.size)}` : key,
  }),
}))
vi.mock('@/lib/api-client', () => ({ sdkApiUrl: vi.fn(), sdkAuthQuery: vi.fn() }))
vi.mock('@/composables/useKeyboardCommand', () => ({ useKeyboardCommand: vi.fn() }))
vi.mock('@/store/chat-list', () => ({
  useChatStore: () => {
    if (!chatState.fsChangedAt) chatState.fsChangedAt = ref(0)
    return {
      fsChangedAt: chatState.fsChangedAt,
      currentBotId: ref('bot-a'),
      bots: ref([]),
      affectsPath: (path: string) => chatState.affectsPath(path),
      fsEventForPath: () => null,
    }
  },
}))
vi.mock('@/components/monaco-editor/diff.vue', () => ({ default: 'div' }))
vi.mock('@/components/monaco-editor/index.vue', () => ({
  default: (props: { modelValue: string; 'onUpdate:modelValue': (value: string) => void }) => h('textarea', {
    value: props.modelValue,
    onInput: (event: Event) => props['onUpdate:modelValue']((event.target as HTMLTextAreaElement).value),
  }),
}))
vi.mock('./document-preview.vue', () => ({
  default: (props: { kind?: string, filename?: string, blob?: Blob }) => h('div', {
    'data-testid': 'doc-preview',
    'data-kind': props.kind,
    'data-size': String(props.blob?.size ?? 0),
  }, props.filename),
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
  chatState.fsChangedAt = ref(0)
  chatState.affectsPath = () => false
  sdk.read.mockResolvedValue({ data: { content: 'initial', revision: 'r0' } })
  sdk.write.mockResolvedValue({ data: { revision: 'r1' } })
  sdk.fs.mockResolvedValue({ data: { path: '/a.pdf', isDir: false, size: 100, modTime: '2026-01-01T00:00:00Z' } })
  bounded.fetchBoundedBlob.mockResolvedValue({ ok: true, blob: new Blob(['%PDF-1.4']) })
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

describe('file viewer binary document preview', () => {
  const settle = async () => {
    await nextTick()
    await Promise.resolve()
    await nextTick()
  }

  const mountDoc = async (fileProps: Record<string, unknown> | (() => ReturnType<typeof h>)) => {
    host = document.createElement('div')
    document.body.append(host)
    app = createApp(typeof fileProps === 'function' ? fileProps : () => h(FileViewer, fileProps as never))
    app.mount(host)
    await settle()
  }

  it('renders oversized fallback and isolates document fingerprints across aborted mid-flight polls', async () => {
    bounded.fetchBoundedBlob.mockResolvedValueOnce({ ok: false, reason: 'too-large', maxBytes: 20 * 1024 * 1024 })
    await mountDoc({ botId: 'bot-a', file: { path: '/huge.docx', name: 'huge.docx' } })
    expect(host.querySelector('[data-testid="doc-preview"]')).toBeNull()
    expect(host.textContent).toContain('bots.files.previewTooLarge:20.0 MB')
    app.unmount()
    host.remove()

    vi.useFakeTimers()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    let resolveReloadA!: (res: { ok: true, blob: Blob }) => void
    bounded.fetchBoundedBlob
      .mockResolvedValueOnce({ ok: true, blob: new Blob(['pdf-a-v1']) })
      .mockReturnValueOnce(new Promise((yes) => { resolveReloadA = yes }))
      .mockResolvedValueOnce({ ok: true, blob: new Blob(['pdf-b-v1']) })
      .mockResolvedValueOnce({ ok: true, blob: new Blob(['pdf-b-v2']) })

    const props = reactive({ botId: 'bot-a', file: { path: '/a.pdf', name: 'a.pdf', modTime: 't1' } })
    await mountDoc(props)

    const sharedNextMeta = { path: '/b.pdf', isDir: false, size: 777, modTime: 't-next' }
    sdk.fs.mockResolvedValueOnce({ data: sharedNextMeta })
    await vi.advanceTimersByTimeAsync(2_000)
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(3)

    props.file = { path: '/b.pdf', name: 'b.pdf', modTime: 't-b1' }
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(4)

    resolveReloadA({ ok: true, blob: new Blob(['pdf-a-v2']) })
    await settle()
    sdk.fs.mockResolvedValueOnce({ data: sharedNextMeta })
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(5)
  })

  it('defers fsChangedAt document reloads while deactivated in KeepAlive and reloads upon reactivation', async () => {
    const active = ref(true)
    chatState.affectsPath = path => path === '/spec.docx'
    await mountDoc(() => h(KeepAlive, null, [
      active.value ? h(FileViewer, { key: 'doc-tab', botId: 'bot-a', file: { path: '/spec.docx', name: 'spec.docx' } }) : null,
    ]))
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(1)

    active.value = false
    await nextTick()
    chatState.fsChangedAt.value = Date.now()
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(1)

    active.value = true
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(2)
  })

  it('aligns polling baseline without modTime and recovers automatically after 404 deleted or initial 503 stale', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    chatState.affectsPath = path => path === '/report.pdf'

    const v1 = new Blob(['%PDF-1.4'])
    const v2 = new Blob(['%PDF-1.4-updated'])
    const v3 = new Blob(['%PDF-1.4-agent'])
    bounded.fetchBoundedBlob
      .mockResolvedValueOnce({ ok: true, blob: v1 })
      .mockResolvedValueOnce({ ok: true, blob: v2 })
      .mockResolvedValueOnce({ ok: true, blob: v3 })
    sdk.fs.mockResolvedValue({ data: { path: '/report.pdf', isDir: false, size: v1.size, modTime: '2026-11-13T07:00:00Z' } })

    await mountDoc({ botId: 'bot-a', file: { path: '/report.pdf', name: 'report.pdf', isDir: false } })
    await vi.advanceTimersByTimeAsync(4_000)
    await settle()
    expect(sdk.fs).toHaveBeenCalledTimes(2)
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(1)

    sdk.fs.mockResolvedValue({ data: { path: '/report.pdf', isDir: false, size: v2.size, modTime: '2026-11-13T07:05:00Z' } })
    await vi.advanceTimersByTimeAsync(4_000)
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(2)

    sdk.fs.mockResolvedValue({ data: { path: '/report.pdf', isDir: false, size: v3.size, modTime: '2026-11-13T07:10:00Z' } })
    chatState.fsChangedAt.value = Date.now()
    await settle()
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(3)

    // Poll hits 404 -> deleted placeholder, then 200 -> restores preview
    const restored = new Blob(['%PDF-1.4-restored'])
    bounded.fetchBoundedBlob.mockResolvedValueOnce({ ok: true, blob: restored })
    sdk.fs
      .mockRejectedValueOnce({ status: 404 })
      .mockResolvedValue({ data: { path: '/report.pdf', isDir: false, size: restored.size, modTime: '2026-11-13T09:00:00Z' } })
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()
    expect(host.textContent).toContain('bots.files.documentDeleted')
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()
    expect(host.querySelector('[data-testid="doc-preview"]')?.getAttribute('data-size')).toBe(String(restored.size))

    // Initial 503 stale -> recovers on next 2s poll
    app.unmount()
    host.remove()
    const recovered503 = new Blob(['%PDF-1.4-after-503'])
    bounded.fetchBoundedBlob
      .mockRejectedValueOnce({ status: 503 })
      .mockResolvedValueOnce({ ok: true, blob: recovered503 })
    sdk.fs.mockResolvedValue({ data: { path: '/s.pdf', isDir: false, size: recovered503.size, modTime: '2026-11-13T09:10:00Z' } })
    await mountDoc({ botId: 'bot-a', file: { path: '/s.pdf', name: 's.pdf', isDir: false } })
    expect(host.querySelector('[data-testid="doc-preview"]')).toBeNull()
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()
    expect(host.querySelector('[data-testid="doc-preview"]')?.getAttribute('data-size')).toBe(String(recovered503.size))
  })
})
