// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const mocks = vi.hoisted(() => ({
  putUsersMe: vi.fn(),
  toastError: vi.fn(),
}))

vi.mock('vue-router', () => ({ useRouter: () => ({ currentRoute: { value: { name: 'Home' } }, replace: vi.fn() }) }))
vi.mock('@memohai/sdk', () => ({ getUsersMe: vi.fn(), putUsersMe: mocks.putUsersMe }))
vi.mock('@felinic/ui', () => ({ toast: { error: mocks.toastError } }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))

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

async function setup() {
  vi.resetModules()
  setActivePinia(createPinia())
  const { useChatExamplesUi } = await import('./use-chat-examples-ui')
  const { useUserStore } = await import('@/store/user')
  return { ui: useChatExamplesUi(), userStore: useUserStore() }
}

async function settle() {
  await new Promise(resolve => setTimeout(resolve, 0))
}

describe('useChatExamplesUi sidebar entry', () => {
  beforeEach(() => {
    mocks.putUsersMe.mockReset()
    mocks.toastError.mockReset()
  })

  it('shows the sidebar entry by default', async () => {
    const { ui } = await setup()
    expect(ui.sidebarEntryHidden.value).toBe(false)
  })

  it('saves hiding and showing the entry on the account', async () => {
    mocks.putUsersMe.mockResolvedValue({ data: {} })
    const { ui } = await setup()

    ui.sidebarEntryHidden.value = true
    expect(ui.sidebarEntryHidden.value).toBe(true)
    await settle()
    expect(mocks.putUsersMe).toHaveBeenLastCalledWith({
      body: { metadata: { chat_examples_sidebar_entry_hidden: true } },
      throwOnError: true,
    })

    ui.sidebarEntryHidden.value = false
    await settle()
    expect(ui.sidebarEntryHidden.value).toBe(false)
    expect(mocks.putUsersMe).toHaveBeenLastCalledWith({
      body: { metadata: { chat_examples_sidebar_entry_hidden: false } },
      throwOnError: true,
    })
    expect(mocks.toastError).not.toHaveBeenCalled()
  })

  it('puts the entry back and reports when the save fails', async () => {
    mocks.putUsersMe.mockRejectedValue(new Error('offline'))
    const { ui } = await setup()

    ui.sidebarEntryHidden.value = true
    await settle()

    expect(ui.sidebarEntryHidden.value).toBe(false)
    expect(mocks.toastError).toHaveBeenCalledWith('chatExamples.sidebarEntrySaveFailed')
  })

  it('keeps the newer choice when an older save fails late', async () => {
    let rejectFirst!: (error: Error) => void
    mocks.putUsersMe
      .mockImplementationOnce(() => new Promise((_, reject) => { rejectFirst = reject }))
      .mockResolvedValueOnce({ data: {} })
    const { ui, userStore } = await setup()

    ui.sidebarEntryHidden.value = true
    ui.sidebarEntryHidden.value = false
    ui.sidebarEntryHidden.value = true
    await settle()
    rejectFirst(new Error('late'))
    await settle()

    expect(userStore.chatExamplesSidebarEntryHidden).toBe(true)
  })
})
