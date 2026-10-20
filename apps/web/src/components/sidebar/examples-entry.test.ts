// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'

const mocks = vi.hoisted(() => ({ toast: vi.fn() }))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', async (importOriginal) => ({
  ...await importOriginal<typeof import('@felinic/ui')>(),
  toast: mocks.toast,
}))

const storage = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
    clear: () => storage.clear(),
  },
  configurable: true,
})

const { default: ExamplesEntry } = await import('./examples-entry.vue')
const { useChatExamplesUi } = await import('@/components/chat-examples/use-chat-examples-ui')

let app: App | undefined

afterEach(() => {
  app?.unmount()
  mocks.toast.mockReset()
  useChatExamplesUi().sidebarEntryHidden.value = false
})

it('hides the entry from its × and offers an undo that brings it back', async () => {
  const root = document.createElement('div')
  app = createApp(ExamplesEntry)
  app.mount(root)
  const { sidebarEntryHidden } = useChatExamplesUi()

  const dismiss = root.querySelector<HTMLButtonElement>('[aria-label="chatExamples.hideSidebarEntry"]')
  expect(dismiss).not.toBeNull()
  dismiss!.click()
  await nextTick()

  expect(sidebarEntryHidden.value).toBe(true)
  expect(mocks.toast).toHaveBeenCalledWith('chatExamples.sidebarEntryHidden', expect.objectContaining({
    action: expect.objectContaining({ label: 'chatExamples.undoHideSidebarEntry' }),
  }))

  mocks.toast.mock.calls[0]![1].action.onClick()
  expect(sidebarEntryHidden.value).toBe(false)
})
