// @vitest-environment jsdom
import { createApp, h, nextTick, reactive, ref, type App } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
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
  toast, Button: 'button', PanePlaceholder: 'div',
  DiffTitleBar: (_props: object, { slots }: { slots: Record<string, (() => unknown) | undefined> }) =>
    h('div', [slots.default?.(), slots.actions?.()] as never),
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

// A file read goes through the shared query, whose promise chain settles over
// more microtasks than a bare SDK call. Timers are not used so this also works
// under fake timers.
async function settleRead() {
  for (let i = 0; i < 30; i++) await nextTick()
}

let app: App
let host: HTMLDivElement
async function mountViewer(initialVisible = true) {
  const viewer = ref<InstanceType<typeof FileViewer>>()
  const props = reactive({ botId: 'bot-a', file: { path: '/a.txt', name: 'a.txt' }, visible: initialVisible })
  const dirty = vi.fn()
  const saved = vi.fn()
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({
    setup: () => () => h(FileViewer, { ...props, ref: viewer, 'onUpdate:dirty': dirty, onSaved: saved }),
  })
  app.use(createPinia()).use(PiniaColada)
  app.mount(host)
  await settleRead()
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
  sdk.read.mockResolvedValue({ data: { content: 'initial', revision: 'r0', size: 7 } })
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
    vi.useFakeTimers({ now: 600_000 })
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const view = await mountViewer()
    // Changed on disk since the load. The first poll only notes the new stat
    // (the content was read moments ago); the read follows once it has settled.
    sdk.fs.mockResolvedValue({
      data: { path: '/a.txt', name: 'a.txt', isDir: false, size: 99, mode: '-rw-r--r--', modTime: '1970-01-01T00:09:58Z' },
    })
    const pending = deferred()
    sdk.read.mockReturnValueOnce(pending.promise)
    await vi.advanceTimersByTimeAsync(4_000)
    expect(sdk.read).toHaveBeenCalledTimes(2)
    const pollSignal: AbortSignal = sdk.read.mock.calls[1]![0].signal
    sdk.read.mockResolvedValue({ data: { content: 'other', revision: 'other-r0' } })
    if (change === 'bot') view.props.botId = 'bot-b'
    else view.props.file = { path: '/b.txt', name: 'b.txt' }
    await settleRead()
    expect(pollSignal.aborted).toBe(true)
    pending.resolve({ data: { content: 'old polled text', revision: 'old-r1' } })
    await settleRead()
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
    await settleRead()
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
    await settleRead()
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
    await settleRead()
    if (change === 'return') {
      view.props.file = { path: '/a.txt', name: 'a.txt' }
      await settleRead()
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

  it('polls via lightweight stat without re-reading file bytes when unchanged', async () => {
    vi.useFakeTimers({ now: 600_000 })
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    // Same size as the loaded content and last modified long before the load.
    sdk.fs.mockResolvedValue({
      data: { path: '/a.txt', name: 'a.txt', isDir: false, size: 7, mode: '-rw-r--r--', modTime: '1970-01-01T00:00:01Z' },
    })
    await mountViewer()
    expect(sdk.read).toHaveBeenCalledTimes(1)

    // One check per 2s interval; none right after the load, which just read
    // the file.
    await vi.advanceTimersByTimeAsync(6_000)
    expect(sdk.fs).toHaveBeenCalledTimes(3)
    expect(sdk.read).toHaveBeenCalledTimes(1)
  })

  it('keeps dirty editor mounted when hidden so undo and background save remain intact', async () => {
    const view = await mountViewer(true)
    await view.edit('unsaved draft')
    view.props.visible = false
    await nextTick()
    expect(host.querySelector('textarea')).not.toBeNull()
    expect(host.querySelector('textarea')!.value).toBe('unsaved draft')
    expect(await view.save()).toBe(true)
  })
})

describe('file viewer text disk state', () => {
  const stat = (size: number, modTime: string) => ({
    data: { path: '/a.txt', name: 'a.txt', isDir: false, size, modTime },
  })
  const clickButton = async (label: string) => {
    const button = [...host.querySelectorAll('button')].find(el => el.textContent?.includes(label))
    expect(button, `button "${label}"`).toBeTruthy()
    button!.click()
    await settleRead()
  }
  beforeEach(() => {
    vi.useFakeTimers({ now: 600_000 })
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    // Same size as the loaded content and last modified long before the load.
    sdk.fs.mockResolvedValue(stat(7, '1970-01-01T00:00:01Z'))
  })

  it('reads the file once when an agent tool reports a change to it', async () => {
    chatState.affectsPath = path => path === '/a.txt'
    await mountViewer()
    expect(sdk.read).toHaveBeenCalledTimes(1)
    sdk.read.mockResolvedValue({ data: { content: 'agent text', revision: 'r1', size: 10 } })
    chatState.fsChangedAt.value = Date.now()
    await settleRead()
    expect(sdk.read).toHaveBeenCalledTimes(2)
    expect(host.querySelector('textarea')!.value).toBe('agent text')
  })

  it('clears the deleted notice when the file comes back with the content the editor holds', async () => {
    await mountViewer()
    sdk.fs.mockRejectedValue({ status: 404 })
    sdk.read.mockRejectedValue({ status: 404 })
    await vi.advanceTimersByTimeAsync(2_000)
    await settleRead()
    expect(host.textContent).toContain('bots.files.externalChange.deleted')

    sdk.fs.mockResolvedValue(stat(7, '1970-01-01T00:10:01Z'))
    sdk.read.mockResolvedValue({ data: { content: 'initial', revision: 'r0', size: 7 } })
    await vi.advanceTimersByTimeAsync(2_000)
    await settleRead()
    expect(host.querySelector('[role="status"]')).toBeNull()
    expect(host.querySelector('textarea')!.value).toBe('initial')
  })

  it('clears the reload-failed notice once a failed check succeeds again, without reading the file', async () => {
    await mountViewer()
    sdk.fs.mockRejectedValueOnce({ status: 503 })
    await vi.advanceTimersByTimeAsync(2_000)
    await settleRead()
    expect(host.textContent).toContain('bots.files.externalChange.reloadFailed')

    await vi.advanceTimersByTimeAsync(2_000)
    await settleRead()
    expect(host.querySelector('[role="status"]')).toBeNull()
    expect(sdk.read).toHaveBeenCalledTimes(1)
  })

  it('compares against what the disk holds now and does not call that diff stale', async () => {
    const view = await mountViewer()
    await view.edit('my draft')
    sdk.fs.mockResolvedValue(stat(99, '1970-01-01T00:09:58Z'))
    sdk.read.mockResolvedValue({ data: { content: 'agent v2', revision: 'r2', size: 99 } })
    await vi.advanceTimersByTimeAsync(4_000)
    await settleRead()
    expect(host.querySelector('textarea')!.value).toBe('my draft')

    // The disk changed once more before Compare is opened.
    sdk.read.mockResolvedValue({ data: { content: 'agent v3', revision: 'r3', size: 99 } })
    await clickButton('bots.files.externalChange.compare')
    expect(host.querySelector('[original]')?.getAttribute('original')).toBe('agent v3')
    expect(host.querySelector('[original]')?.getAttribute('modified')).toBe('my draft')
    expect(host.textContent).not.toContain('bots.files.compare.staleNotice')

    await vi.advanceTimersByTimeAsync(6_000)
    await settleRead()
    expect(host.textContent).not.toContain('bots.files.compare.staleNotice')

    // A further change does mark it stale, and Refresh brings the new content in.
    sdk.fs.mockResolvedValue(stat(120, '1970-01-01T00:10:30Z'))
    sdk.read.mockResolvedValue({ data: { content: 'agent v4', revision: 'r4', size: 120 } })
    await vi.advanceTimersByTimeAsync(4_000)
    await settleRead()
    expect(host.textContent).toContain('bots.files.compare.staleNotice')
    expect(host.querySelector('[original]')?.getAttribute('original')).toBe('agent v3')
    await clickButton('bots.files.compare.refresh')
    expect(host.querySelector('[original]')?.getAttribute('original')).toBe('agent v4')
    expect(host.textContent).not.toContain('bots.files.compare.staleNotice')

    // Unsaved edits are still blocked from silently overwriting that change.
    expect(await view.save()).toBe(false)
    expect(sdk.write).not.toHaveBeenCalled()
  })
})

describe('file viewer binary document preview', () => {
  const settle = async () => {
    await nextTick()
    await Promise.resolve()
    await nextTick()
  }

  const mountDoc = async (fileProps: Record<string, unknown>) => {
    host = document.createElement('div')
    document.body.append(host)
    app = createApp(() => h(FileViewer, fileProps as never))
    app.use(createPinia()).use(PiniaColada)
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

  it('defers fsChangedAt document reloads while the panel is hidden and reloads when it is shown again', async () => {
    chatState.affectsPath = path => path === '/spec.docx'
    const props = reactive({ botId: 'bot-a', file: { path: '/spec.docx', name: 'spec.docx' }, visible: true })
    await mountDoc(props)
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(1)

    props.visible = false
    await nextTick()
    chatState.fsChangedAt.value = Date.now()
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(1)

    props.visible = true
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(2)
  })

  it('stops a document download when the panel is hidden and starts it over when shown again', async () => {
    bounded.fetchBoundedBlob.mockReset()
    bounded.fetchBoundedBlob
      .mockReturnValueOnce(new Promise(() => {}))
      .mockResolvedValueOnce({ ok: true, blob: new Blob(['%PDF-1.4']) })
    const props = reactive({ botId: 'bot-a', file: { path: '/big.pdf', name: 'big.pdf' }, visible: true })
    await mountDoc(props)
    const firstSignal: AbortSignal = bounded.fetchBoundedBlob.mock.calls[0]![0].signal
    expect(firstSignal.aborted).toBe(false)

    props.visible = false
    await settle()
    expect(firstSignal.aborted).toBe(true)
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(1)

    props.visible = true
    await settle()
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(2)
    expect(host.querySelector('[data-testid="doc-preview"]')).not.toBeNull()
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
