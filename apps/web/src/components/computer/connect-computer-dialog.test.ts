// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, ref, type App, type Slots } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import type { UserruntimeRuntime } from '@memohai/sdk'
import ConnectComputerDialog from './connect-computer-dialog.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), remove: vi.fn(), grant: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getUsersMeRuntimes: api.list,
  deleteUsersMeRuntimesById: api.remove,
  putBotsByBotIdWorkspaceTargetsRemotesByRuntimeId: api.grant,
}))
vi.mock('@memohai/sdk/colada', () => ({
  getBotsQuery: () => ({ key: ['bots'], query: async () => ({ items: [{ id: 'bot-1' }] }) }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/lib/api-client', () => ({ sdkApiBaseUrl: () => '/api' }))
vi.mock('@/pages/runtimes/command', () => ({ buildRuntimeConnectCommand: () => 'runtime command' }))
vi.mock('./computer-access-list.vue', () => ({ default: () => h('div', 'access-list') }))
vi.mock('@felinic/ui', () => {
  const Wrapper = (_props: unknown, { slots }: { slots: Slots }) => h('div', slots.default?.())
  return {
    Button: (_props: unknown, { slots }: { slots: Slots }) => h('button', slots.default?.()),
    Dialog: Wrapper, DialogContent: Wrapper, DialogDescription: Wrapper,
    DialogFooter: Wrapper, DialogHeader: Wrapper, DialogTitle: Wrapper,
    toast: { success: vi.fn(), error: vi.fn() },
    useClipboard: () => ({ copyText: vi.fn() }),
  }
})

let app: App | undefined
let root: HTMLDivElement
const credential = { id: 'runtime-1', key: 'test-key' }
let items = ref<UserruntimeRuntime[]>([])

async function flush() {
  await vi.advanceTimersByTimeAsync(0)
  await nextTick()
}

async function mount(open = true) {
  const props = reactive({ open, credential })
  root = document.createElement('div')
  app = createApp(() => h(ConnectComputerDialog, {
    ...props, 'onUpdate:open': (value: boolean) => { props.open = value },
  }))
  app.use(createPinia()).use(PiniaColada)
  app.mount(root)
  await flush()
  return props
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  items = ref([])
  api.list.mockImplementation(async () => ({ data: items.value }))
  api.grant.mockResolvedValue({ data: {} })
  api.remove.mockResolvedValue({})
})
afterEach(() => {
  app?.unmount()
  app = undefined
  vi.useRealTimers()
})

describe('connect computer polling', () => {
  it('detects a connection without visibility events and stops after success', async () => {
    await mount()
    expect(root.textContent).toContain('computerConnect.waiting')
    items.value = [{ ...credential, online: true, name: 'Computer' }]
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(root.textContent).toContain('access-list')
    expect(api.grant).toHaveBeenCalledOnce()
    const calls = api.list.mock.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.list).toHaveBeenCalledTimes(calls)
  })

  it('does not overlap slow requests and recovers after a failed query', async () => {
    let reject!: (reason: Error) => void
    api.list.mockImplementationOnce(() => new Promise((_resolve, rejectRequest) => { reject = rejectRequest }))
    await mount()
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.list).toHaveBeenCalledOnce()
    reject(new Error('temporary outage'))
    await flush()
    items.value = [{ ...credential, online: true }]
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(root.textContent).toContain('access-list')
  })

  it('does not poll while closed and starts immediately when opened', async () => {
    const props = await mount(false)
    const calls = api.list.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(api.list).toHaveBeenCalledTimes(calls)
    props.open = true
    await flush()
    expect(api.list).toHaveBeenCalledTimes(calls + 1)
    props.open = false
    await flush()
    const afterClose = api.list.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(api.list).toHaveBeenCalledTimes(afterClose)
  })

  it('does not grant access or restart polling when a request resolves after close', async () => {
    let resolve!: (value: { data: UserruntimeRuntime[] }) => void
    api.list.mockImplementationOnce(() => new Promise(resolveRequest => { resolve = resolveRequest }))
    const props = await mount()
    props.open = false
    await flush()
    resolve({ data: [{ ...credential, online: true }] })
    await flush()
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.grant).not.toHaveBeenCalled()
    expect(api.list).toHaveBeenCalledOnce()
  })

  it('serializes grants and resumes a new connection after the previous grant finishes', async () => {
    let finishGrant!: (value: { data: object }) => void
    api.grant.mockImplementationOnce(() => new Promise(resolve => { finishGrant = resolve }))
    const props = await mount()
    items.value = [{ ...credential, online: true }]
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(api.grant).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(6000)
    items.value = [{ ...credential, online: true }]
    document.dispatchEvent(new Event('visibilitychange'))
    await flush()
    expect(api.grant).toHaveBeenCalledOnce()
    expect(root.textContent).not.toContain('access-list')
    props.open = false
    await flush()
    props.credential = { id: 'runtime-2', key: 'second-key' }
    items.value = [{ ...props.credential, online: true }]
    props.open = true
    await flush()
    expect(api.grant).toHaveBeenCalledOnce()
    finishGrant({ data: {} })
    await flush()
    expect(api.grant).toHaveBeenCalledTimes(2)
    expect(api.grant.mock.lastCall?.[0].path.runtime_id).toBe('runtime-2')
    expect(root.textContent).toContain('access-list')
  })

  it('stops polling on unmount', async () => {
    await mount()
    app?.unmount()
    app = undefined
    const calls = api.list.mock.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.list).toHaveBeenCalledTimes(calls)
  })
})
