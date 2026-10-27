// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import UpdateChip from './update-chip.vue'
import { DesktopUpdatesKey, type DesktopUpdateBridge, type DesktopUpdateState } from '@/lib/desktop-shell'
import { mockDesktopUpdates as mock } from '@/lib/desktop-updates-mock'

const { push } = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
let app: App | undefined
let root: HTMLDivElement
async function flush() {
  await Promise.resolve()
  await nextTick()
}
async function mount(bridge?: DesktopUpdateBridge) {
  root = document.createElement('div')
  app = createApp(UpdateChip)
  if (bridge) app.provide(DesktopUpdatesKey, bridge)
  app.mount(root)
  await flush()
}
afterEach(() => {
  app?.unmount()
  mock.disarm()
})
it('renders actionable mock states after mounting without a bridge', async () => {
  await mount()
  expect(root.querySelector('button')).toBeNull()
  mock.arm()
  for (const status of ['downloading', 'downloaded', 'installing', 'error'] as const) {
    mock.setStatus(status)
    await flush()
    expect(root.querySelector('button')).not.toBeNull()
  }
  for (const status of ['idle', 'checking', 'up-to-date', 'unavailable'] as const) {
    mock.setStatus(status)
    await flush()
    expect(root.querySelector('button')).toBeNull()
  }
  mock.disarm()
  await flush()
  expect(root.querySelector('button')).toBeNull()
})
it('ignores an old real bridge snapshot after arming the mock', async () => {
  let resolve!: (state: DesktopUpdateState) => void
  await mount({ ...mock.bridge, getState: () => new Promise(r => { resolve = r }), onStateChanged: () => () => {} })
  mock.arm()
  mock.setStatus('downloaded')
  await flush()
  expect(root.querySelector('button')).not.toBeNull()
  resolve({ ...mock.state.value, status: 'idle' })
  await flush()
  expect(root.querySelector('button')).not.toBeNull()
})

it('keeps a newer update event when the initial snapshot arrives late', async () => {
  let resolve!: (state: DesktopUpdateState) => void
  let emit!: (state: DesktopUpdateState) => void
  const stop = vi.fn()
  await mount({
    ...mock.bridge,
    getState: () => new Promise(r => { resolve = r }),
    onStateChanged: listener => { emit = listener; return stop },
  })
  emit({ ...mock.state.value, status: 'downloaded' })
  await flush()
  resolve({ ...mock.state.value, status: 'idle' })
  await flush()
  expect(root.querySelector('button')?.getAttribute('aria-label')).toBe('about.restartToUpdate')
  app?.unmount()
  app = undefined
  expect(stop).toHaveBeenCalledOnce()
})

it('locks restart immediately and renders errors without the brand fill', async () => {
  mock.arm()
  mock.setStatus('downloaded')
  await mount()
  root.querySelector('button')!.click()
  await flush()
  expect(mock.state.value.status).toBe('installing')
  expect(root.querySelector('button')!.disabled).toBe(true)
  mock.setStatus('error')
  await flush()
  expect(root.querySelector('button')!.className).toContain('bg-accent-gray-soft-active')
})

it('opens update details directly from the error chip', async () => {
  mock.arm()
  mock.setStatus('error')
  await mount()
  root.querySelector('button')!.click()
  await flush()
  expect(push).toHaveBeenCalledWith({ name: 'about', query: { updates: '1' } })
})
