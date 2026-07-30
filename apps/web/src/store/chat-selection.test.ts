import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

describe('chat-selection store', () => {
  let storage: Map<string, string>
  let session: Map<string, string>

  beforeEach(() => {
    vi.resetModules()
    storage = new Map<string, string>()
    session = new Map<string, string>()
    const makeMock = (map: Map<string, string>) => ({
      getItem: (key: string) => map.get(key) ?? null,
      setItem: (key: string, value: string) => map.set(key, value),
      removeItem: (key: string) => map.delete(key),
      clear: () => map.clear(),
    })
    const localStorageMock = makeMock(storage)
    // Per-Tab focus (bot/session) now reads from sessionStorage; seed here.
    const sessionStorageMock = makeMock(session)
    class StorageMock {}
    vi.stubGlobal('Storage', StorageMock)
    vi.stubGlobal('localStorage', localStorageMock)
    vi.stubGlobal('sessionStorage', sessionStorageMock)
    vi.stubGlobal('document', {})
    vi.stubGlobal('window', {
      localStorage: localStorageMock,
      sessionStorage: sessionStorageMock,
      Storage: StorageMock,
      document: {},
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('does not migrate a legacy stored session id into an explicit selection', async () => {
    const { useChatSelectionStore } = await import('./chat-selection')
    sessionStorage.setItem('chat-session-id', 'history-session-1')

    const selection = useChatSelectionStore()

    expect(selection.sessionId).toBe('history-session-1')
    expect(selection.explicitSelection).toBe(false)
  })

  it('preserves an existing explicit selection flag', async () => {
    const { useChatSelectionStore } = await import('./chat-selection')
    sessionStorage.setItem('chat-session-id', 'manual-session-1')
    sessionStorage.setItem('chat-explicit-selection', 'true')

    const selection = useChatSelectionStore()

    expect(selection.sessionId).toBe('manual-session-1')
    expect(selection.explicitSelection).toBe(true)
  })
})
