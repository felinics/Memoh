// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, effectScope, h, KeepAlive, nextTick, reactive, ref } from 'vue'
import { isSessionViewed, useSessionViewing } from './useSessionViewing'

const unread = reactive({ entries: {} as Record<string, { attentionAt: string }>, markRead: vi.fn() })
vi.mock('@/store/unread', () => ({ useUnreadStore: () => unread }))

afterEach(() => {
  vi.restoreAllMocks()
  unread.entries = {}
  unread.markRead.mockClear()
})

describe('session viewing', () => {
  it('reads only a visible, focused, loaded active pane and follows new attention', async () => {
    let focused = true
    vi.spyOn(document, 'hasFocus').mockImplementation(() => focused)
    const visible = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const sessionId = ref('s1')
    const active = ref(false)
    const scope = effectScope()
    scope.run(() => useSessionViewing(sessionId, active))
    unread.entries.s1 = { attentionAt: 'first' }
    await nextTick()
    expect(unread.markRead).not.toHaveBeenCalled()

    active.value = true
    await nextTick()
    expect(unread.markRead).toHaveBeenLastCalledWith('s1')
    expect(isSessionViewed('s1')).toBe(true)

    focused = false
    window.dispatchEvent(new Event('blur'))
    unread.markRead.mockClear()
    unread.entries.s1 = { attentionAt: 'second' }
    await nextTick()
    expect(isSessionViewed('s1')).toBe(false)
    expect(unread.markRead).not.toHaveBeenCalled()

    focused = true
    window.dispatchEvent(new Event('focus'))
    await nextTick()
    expect(unread.markRead).toHaveBeenCalledWith('s1')
    unread.markRead.mockClear()
    visible.mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    unread.entries.s1 = { attentionAt: 'third' }
    await nextTick()
    expect(unread.markRead).not.toHaveBeenCalled()
    scope.stop()
    expect(isSessionViewed('s1')).toBe(false)
  })

  it('checks the actual pane synchronously before notification delivery', () => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const active = ref(true)
    const id = ref('s1')
    const scope = effectScope()
    scope.run(() => useSessionViewing(id, active))
    expect(isSessionViewed('s1')).toBe(true)
    id.value = 's2'
    expect(isSessionViewed('s1')).toBe(false)
    expect(isSessionViewed('s2')).toBe(true)
    active.value = false
    expect(isSessionViewed('s2')).toBe(false)
    scope.stop()
  })

  it('stops reading while its pane is cached by KeepAlive with stale props', async () => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const Pane = defineComponent({
      setup() {
        useSessionViewing(ref('s1'), ref(true))
        return () => null
      },
    })
    const shown = ref(true)
    const root = document.createElement('div')
    const app = createApp({ render: () => h(KeepAlive, null, shown.value ? [h(Pane)] : []) })
    app.mount(root)
    expect(isSessionViewed('s1')).toBe(true)

    shown.value = false
    await nextTick()
    unread.entries.s1 = { attentionAt: 'while-hidden' }
    await nextTick()
    expect(isSessionViewed('s1')).toBe(false)
    expect(unread.markRead).not.toHaveBeenCalled()

    shown.value = true
    await nextTick()
    await nextTick()
    expect(unread.markRead).toHaveBeenCalledWith('s1')
    app.unmount()
  })
})
