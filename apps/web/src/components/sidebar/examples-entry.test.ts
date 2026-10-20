// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createPinia, setActivePinia } from 'pinia'

const mocks = vi.hoisted(() => ({ toast: vi.fn(), putUsersMe: vi.fn() }))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ currentRoute: { value: { name: 'Home' } }, replace: vi.fn() }) }))
vi.mock('@memohai/sdk', () => ({ getUsersMe: vi.fn(), putUsersMe: mocks.putUsersMe }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
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

const pinia = createPinia()
setActivePinia(pinia)

const { default: ExamplesEntry } = await import('./examples-entry.vue')
const { useChatExamplesUi } = await import('@/components/chat-examples/use-chat-examples-ui')

let app: App | undefined

afterEach(() => {
  app?.unmount()
  mocks.toast.mockReset()
  mocks.putUsersMe.mockReset()
})

it('hides the entry from its × and offers an undo that brings it back', async () => {
  const root = document.createElement('div')
  mocks.putUsersMe.mockResolvedValue({ data: {} })
  app = createApp(ExamplesEntry)
  app.use(pinia)
  app.mount(root)
  const { sidebarEntryHidden } = useChatExamplesUi()

  const dismiss = root.querySelector<HTMLButtonElement>('[aria-label="chatExamples.hideSidebarEntry"]')
  expect(dismiss).not.toBeNull()
  dismiss!.click()
  await nextTick()

  expect(sidebarEntryHidden.value).toBe(true)
  expect(mocks.putUsersMe).toHaveBeenLastCalledWith(expect.objectContaining({
    body: { metadata: { chat_examples_sidebar_entry_hidden: true } },
  }))
  expect(mocks.toast).toHaveBeenCalledWith('chatExamples.sidebarEntryHidden', expect.objectContaining({
    action: expect.objectContaining({ label: 'chatExamples.undoHideSidebarEntry' }),
  }))

  mocks.toast.mock.calls[0]![1].action.onClick()
  expect(sidebarEntryHidden.value).toBe(false)
  expect(mocks.putUsersMe).toHaveBeenLastCalledWith(expect.objectContaining({
    body: { metadata: { chat_examples_sidebar_entry_hidden: false } },
  }))
})
