// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

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

async function loadFresh() {
  vi.resetModules()
  const { useChatExamplesUi } = await import('./use-chat-examples-ui')
  return useChatExamplesUi()
}

describe('useChatExamplesUi sidebar entry', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('shows the sidebar entry by default', async () => {
    const { sidebarEntryHidden } = await loadFresh()
    expect(sidebarEntryHidden.value).toBe(false)
  })

  it('remembers hiding the sidebar entry across reloads', async () => {
    const first = await loadFresh()
    first.sidebarEntryHidden.value = true
    await nextTick()

    const reloaded = await loadFresh()
    expect(reloaded.sidebarEntryHidden.value).toBe(true)

    reloaded.sidebarEntryHidden.value = false
    await nextTick()
    expect((await loadFresh()).sidebarEntryHidden.value).toBe(false)
  })
})
