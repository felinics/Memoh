// @vitest-environment jsdom
import type { App, Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { ChatMessage } from '@/store/chat-list'
import { useChatScroll } from './useChatScroll'

vi.mock('@vueuse/core', async () => {
  const vue = await import('vue')
  return {
    useScroll: () => ({ isScrolling: vue.ref(false) }),
  }
})

type ChatScroll = ReturnType<typeof useChatScroll>

interface ScrollGeometry {
  scrollHeight: number
  clientHeight: number
}

interface Harness {
  app: App
  host: HTMLElement
  viewport: HTMLElement
  scrollEl: Ref<HTMLElement | null>
  content: HTMLElement
  geometry: ScrollGeometry
  scrollTo: ReturnType<typeof vi.fn>
  messages: Ref<ChatMessage[]>
  sessionId: Ref<string>
  lastTurnEl: Ref<HTMLElement | null>
  scroll: ChatScroll
}

class ResizeObserverMock {
  static instances: ResizeObserverMock[] = []

  readonly observe = vi.fn()
  readonly unobserve = vi.fn()
  readonly disconnect = vi.fn()

  constructor(private readonly callback: ResizeObserverCallback) {
    ResizeObserverMock.instances.push(this)
  }

  trigger(target: Element) {
    this.callback([{ target } as ResizeObserverEntry], this as unknown as ResizeObserver)
  }
}

const harnesses: Harness[] = []
let nextAnimationFrameId = 1
const animationFrameTimers = new Map<number, ReturnType<typeof setTimeout>>()

function userMessage(id: string, text = id): ChatMessage {
  return {
    id,
    role: 'user',
    text,
    attachments: [],
    timestamp: '2026-07-10T00:00:00.000Z',
    streaming: false,
    isSelf: true,
  }
}

function assistantMessage(id: string): ChatMessage {
  return {
    id,
    role: 'assistant',
    messages: [],
    timestamp: '2026-07-10T00:00:01.000Z',
    streaming: false,
  }
}

function rect(top: number, height: number): DOMRect {
  return {
    x: 0,
    y: top,
    top,
    right: 600,
    bottom: top + height,
    left: 0,
    width: 600,
    height,
    toJSON: () => ({}),
  }
}

function mountHarness(initialMessages: ChatMessage[] = []): Harness {
  const host = document.createElement('div')
  const viewport = document.createElement('div')
  const content = document.createElement('div')
  viewport.append(content)
  document.body.append(host, viewport)

  const geometry: ScrollGeometry = {
    scrollHeight: 1_000,
    clientHeight: 200,
  }
  viewport.scrollTop = 800
  Object.defineProperties(viewport, {
    scrollHeight: {
      configurable: true,
      get: () => geometry.scrollHeight,
    },
    clientHeight: {
      configurable: true,
      get: () => geometry.clientHeight,
    },
  })
  viewport.getBoundingClientRect = () => rect(0, geometry.clientHeight)

  const scrollTo = vi.fn((options: ScrollToOptions | number) => {
    viewport.scrollTop = typeof options === 'number' ? options : (options.top ?? viewport.scrollTop)
    viewport.dispatchEvent(new Event('scroll'))
  })
  Object.defineProperty(viewport, 'scrollTo', {
    configurable: true,
    value: scrollTo,
  })

  const sessionId = ref('session-1')
  const scrollEl = ref<HTMLElement | null>(viewport)
  const messages = ref<ChatMessage[]>(initialMessages)
  const lastTurnEl = ref<HTMLElement | null>(null)
  let scroll!: ChatScroll
  const app = createApp(defineComponent({
    setup() {
      scroll = useChatScroll({
        scrollEl,
        contentEl: ref(content),
        lastTurnEl,
        messages,
        isActive: ref(true),
        sessionId,
      })
      return () => h('div')
    },
  }))
  app.mount(host)
  scroll.lockScroll.value = false

  const harness = {
    app,
    host,
    viewport,
    scrollEl,
    content,
    geometry,
    scrollTo,
    messages,
    sessionId,
    lastTurnEl,
    scroll,
  }
  harnesses.push(harness)
  return harness
}

async function flushDom() {
  await Promise.resolve()
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

function startBottomJump(): Harness {
  const reply = assistantMessage('assistant-1')
  reply.streaming = true
  const harness = mountHarness([userMessage('user-1'), reply])
  harness.scroll.markEscaped()
  harness.viewport.scrollTop = 400
  harness.viewport.dispatchEvent(new Event('scroll'))
  Object.defineProperty(harness.viewport, 'onscrollend', { configurable: true, value: null })
  // A native smooth scroll remains in flight until the browser reports its
  // completion; unlike instant writes, it does not synchronously reach top.
  harness.scrollTo.mockImplementation((options: ScrollToOptions | number) => {
    if (typeof options !== 'number' && options.behavior === 'smooth') return
    harness.viewport.scrollTop = typeof options === 'number' ? options : (options.top ?? harness.viewport.scrollTop)
    harness.viewport.dispatchEvent(new Event('scroll'))
  })
  harness.scrollTo.mockClear()
  harness.scroll.scrollToBottom()
  expect(harness.scrollTo).toHaveBeenCalledExactlyOnceWith({ top: 800, behavior: 'smooth' })
  return harness
}

beforeEach(() => {
  ResizeObserverMock.instances = []
  vi.stubGlobal('ResizeObserver', ResizeObserverMock)
  vi.stubGlobal('CSS', {
    ...(globalThis.CSS ?? {}),
    escape: (value: string) => value.replaceAll('"', '\\"'),
  })
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    const id = nextAnimationFrameId++
    const timer = setTimeout(() => {
      animationFrameTimers.delete(id)
      callback(performance.now())
    }, 16)
    animationFrameTimers.set(id, timer)
    return id
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => {
    const timer = animationFrameTimers.get(id)
    if (timer) clearTimeout(timer)
    animationFrameTimers.delete(id)
  })
})

afterEach(() => {
  for (const harness of harnesses.splice(0)) {
    harness.app.unmount()
    harness.host.remove()
    harness.viewport.remove()
  }
  for (const timer of animationFrameTimers.values()) clearTimeout(timer)
  animationFrameTimers.clear()
  vi.unstubAllGlobals()
})

describe('useChatScroll gesture and layout handling', () => {
  it('keeps touch escape latched after pointercancel', () => {
    const harness = mountHarness([userMessage('user-1')])
    harness.scrollTo.mockClear()

    harness.viewport.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    harness.viewport.dispatchEvent(new Event('touchstart', { bubbles: true }))
    window.dispatchEvent(new Event('pointercancel'))
    window.dispatchEvent(new Event('touchcancel'))
    harness.viewport.scrollTop = 680
    harness.viewport.dispatchEvent(new Event('scroll'))

    expect(harness.viewport.scrollTop).toBe(680)
    expect(harness.scrollTo).not.toHaveBeenCalled()

    harness.geometry.scrollHeight = 1_100
    ResizeObserverMock.instances[0]?.trigger(harness.content)
    expect(harness.scrollTo).not.toHaveBeenCalled()
  })

  it('re-arms follow after a downward wheel reaches the bottom', async () => {
    const harness = mountHarness([userMessage('user-1')])
    harness.scroll.markEscaped()
    harness.viewport.scrollTop = 700
    harness.scrollTo.mockClear()

    harness.viewport.dispatchEvent(new WheelEvent('wheel', { deltaY: 100, bubbles: true }))
    harness.viewport.scrollTop = 800
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.scrollTo.mockClear()

    harness.geometry.scrollHeight = 1_100
    harness.content.append(document.createTextNode('stream growth'))
    await flushDom()

    expect(harness.scrollTo).toHaveBeenCalledWith({ top: 900, behavior: 'auto' })
  })

  it('follows layout-only content growth reported by ResizeObserver', () => {
    const harness = mountHarness([userMessage('user-1')])
    harness.scrollTo.mockClear()

    harness.geometry.scrollHeight = 1_400
    ResizeObserverMock.instances[0]?.trigger(harness.content)

    expect(harness.scrollTo).toHaveBeenCalledWith({ top: 1_200, behavior: 'auto' })
  })

  it('restores the previous follow mode when a send pin is rolled back', async () => {
    const harness = mountHarness([userMessage('user-1')])
    const rollback = harness.scroll.pinAfterSend()

    rollback()
    await nextTick()
    harness.scrollTo.mockClear()
    harness.geometry.scrollHeight = 1_200
    ResizeObserverMock.instances[0]?.trigger(harness.content)

    expect(harness.scrollTo).toHaveBeenCalledWith({ top: 1_000, behavior: 'auto' })
  })

  it.each(['before render', 'after render'])('preserves the first send across draft promotion %s', async (timing) => {
    const harness = mountHarness()
    harness.sessionId.value = 'draft:chat:1'
    await flushDom()
    harness.scroll.pinAfterSend()
    if (timing === 'before render') {
      harness.sessionId.value = 'created-session'
      await nextTick()
    }
    const turn = document.createElement('div')
    const prompt = document.createElement('div')
    prompt.dataset.messageId = 'first-user'
    turn.append(prompt)
    harness.messages.value.push(userMessage('first-user'))
    harness.lastTurnEl.value = turn
    harness.content.append(turn)
    await flushDom()
    const reserve = harness.scroll.turnReserveStyle('first-user')
    expect(reserve?.minHeight).toMatch(/^\d+px$/)
    if (timing === 'after render') {
      harness.sessionId.value = 'created-session'
      await nextTick()
    }
    expect(harness.scroll.turnReserveStyle('first-user')).toEqual(reserve)
    harness.sessionId.value = 'other-session'
    await nextTick()
    expect(harness.scroll.turnReserveStyle('first-user')).toBeUndefined()
  })

  it('migrates a pinned reserve when messages are replaced in place', async () => {
    const harness = mountHarness([
      userMessage('user-1'),
      assistantMessage('assistant-1'),
    ])
    harness.geometry.scrollHeight = 800
    harness.geometry.clientHeight = 300
    harness.viewport.scrollTop = 500

    const firstTurn = document.createElement('div')
    const firstPrompt = document.createElement('div')
    firstPrompt.dataset.messageId = 'user-1'
    firstTurn.append(firstPrompt)
    harness.content.append(firstTurn)
    harness.lastTurnEl.value = firstTurn
    await flushDom()

    harness.scroll.pinAfterSend()
    const secondTurn = document.createElement('div')
    const secondPrompt = document.createElement('div')
    secondPrompt.dataset.messageId = 'optimistic-user-2'
    secondTurn.append(secondPrompt)
    secondTurn.getBoundingClientRect = () => rect(120, 60)
    secondPrompt.getBoundingClientRect = () => rect(120, 40)
    Object.defineProperty(secondTurn, 'offsetHeight', {
      configurable: true,
      get: () => Math.max(60, Number.parseFloat(secondTurn.style.minHeight) || 0),
    })
    Object.defineProperty(secondPrompt, 'offsetHeight', {
      configurable: true,
      get: () => 40,
    })

    harness.messages.value.push(
      userMessage('optimistic-user-2'),
      assistantMessage('optimistic-assistant-2'),
    )
    harness.lastTurnEl.value = secondTurn
    harness.content.append(secondTurn)
    await flushDom()

    const reserve = harness.scroll.turnReserveStyle('optimistic-user-2')
    expect(reserve?.minHeight).toMatch(/^\d+px$/)

    harness.messages.value.splice(2, 1, userMessage('server-user-2'))
    await nextTick()

    expect(harness.scroll.turnReserveStyle('optimistic-user-2')).toBeUndefined()
    expect(harness.scroll.turnReserveStyle('server-user-2')).toEqual(reserve)
  })
})

describe('native bottom jump during streaming', () => {
  it('keeps the smooth flight intact through token mutations and layout growth', async () => {
    const harness = startBottomJump()
    harness.viewport.scrollTop = 600
    harness.viewport.dispatchEvent(new Event('scroll'))

    harness.content.append(document.createTextNode('next streamed token'))
    await flushDom()
    harness.geometry.scrollHeight = 1_200
    ResizeObserverMock.instances[0]?.trigger(harness.content)

    expect(harness.viewport.scrollTop).toBe(600)
    expect(harness.scrollTo).toHaveBeenCalledExactlyOnceWith({ top: 800, behavior: 'smooth' })
  })

  it('catches up to content added during the flight only after scrollend and keeps following', async () => {
    const harness = startBottomJump()
    harness.geometry.scrollHeight = 1_200
    harness.content.append(document.createTextNode('stream growth'))
    await flushDom()
    harness.viewport.scrollTop = 800
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.viewport.dispatchEvent(new Event('scrollend'))
    await flushDom()

    expect(harness.viewport.scrollTop).toBe(1_000)
    harness.scrollTo.mockClear()
    harness.geometry.scrollHeight = 1_300
    ResizeObserverMock.instances[0]?.trigger(harness.content)
    expect(harness.viewport.scrollTop).toBe(1_100)
    expect(harness.scrollTo).toHaveBeenCalledWith({ top: 1_100, behavior: 'auto' })
  })

  it.each(['wheel', 'touch', 'keyboard'] as const)('does not resume following after %s interruption', async (gesture) => {
    const harness = startBottomJump()
    harness.viewport.scrollTop = 600
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.geometry.scrollHeight = 1_200
    harness.scrollTo.mockClear()

    if (gesture === 'wheel') {
      harness.viewport.dispatchEvent(new WheelEvent('wheel', { deltaY: -100, bubbles: true }))
    } else if (gesture === 'touch') {
      harness.viewport.dispatchEvent(new Event('touchstart', { bubbles: true }))
    } else {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'PageUp' }))
    }
    expect(harness.viewport.scrollTop).toBe(600)
    harness.viewport.scrollTop = 500
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.viewport.dispatchEvent(new Event('scrollend'))
    harness.content.append(document.createTextNode('later token'))
    ResizeObserverMock.instances[0]?.trigger(harness.content)
    await flushDom()

    expect(harness.viewport.scrollTop).toBe(500)
    expect(harness.scrollTo.mock.calls.every(([options]) => typeof options !== 'number' && options.behavior === 'instant')).toBe(true)
  })

  it('hands a new send to its prompt without catching up the cancelled bottom jump', async () => {
    const harness = startBottomJump()
    harness.viewport.scrollTop = 600
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.geometry.scrollHeight = 2_400
    harness.scrollTo.mockClear()
    harness.scroll.pinAfterSend()

    const turn = document.createElement('div')
    turn.dataset.chatTurn = ''
    const prompt = document.createElement('div')
    prompt.dataset.messageId = 'user-2'
    turn.append(prompt)
    turn.getBoundingClientRect = () => rect(1_200 - harness.viewport.scrollTop, 100)
    prompt.getBoundingClientRect = () => rect(1_200 - harness.viewport.scrollTop, 40)
    Object.defineProperty(turn, 'offsetHeight', { get: () => Math.max(100, Number.parseFloat(turn.style.minHeight) || 0) })
    Object.defineProperty(prompt, 'offsetHeight', { value: 40 })
    harness.messages.value.push(userMessage('user-2'))
    harness.lastTurnEl.value = turn
    harness.content.append(turn)
    await flushDom()

    expect(harness.scrollTo.mock.calls.some(([options]) => typeof options !== 'number' && options.behavior === 'auto')).toBe(false)
    expect(harness.scrollTo).toHaveBeenLastCalledWith({ top: 1_060, behavior: 'smooth' })
    harness.viewport.scrollTop = 1_060
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.viewport.dispatchEvent(new Event('scrollend'))
    harness.geometry.scrollHeight = 2_500
    harness.content.append(document.createTextNode('new reply'))
    await flushDom()
    expect(harness.viewport.scrollTop).toBe(1_060)
  })

  it('does not catch up the old conversation when a session switch cancels its scroll', async () => {
    const harness = startBottomJump()
    harness.viewport.scrollTop = 600
    harness.viewport.dispatchEvent(new Event('scroll'))
    harness.geometry.scrollHeight = 1_200
    harness.scrollTo.mockClear()

    harness.sessionId.value = 'session-2'
    await nextTick()
    harness.viewport.dispatchEvent(new Event('scrollend'))
    await flushDom()

    expect(harness.viewport.scrollTop).toBe(600)
    expect(harness.scrollTo.mock.calls.every(([options]) => typeof options !== 'number' && options.behavior === 'instant')).toBe(true)
  })
})

describe('message destination during native scrolling', () => {
  it('excludes the entrance translation from message coordinates', () => {
    const harness = mountHarness([userMessage('target')])
    const motion = document.createElement('div')
    motion.dataset.turnMotion = ''
    motion.style.transform = 'matrix(1, 0, 0, 1, 0, 80)'
    const target = document.createElement('div')
    target.dataset.messageId = 'target'
    target.getBoundingClientRect = () => rect(600 + 80 - harness.viewport.scrollTop, 40)
    motion.append(target)
    harness.content.append(motion)
    vi.stubGlobal('DOMMatrixReadOnly', class { m42 = 80 })
    expect(harness.scroll.messageJumpTarget(harness.viewport, 'target')).toBe(460)
    motion.style.transform = ''
    target.getBoundingClientRect = () => rect(600 - harness.viewport.scrollTop, 40)
    expect(harness.scroll.messageJumpTarget(harness.viewport, 'target')).toBe(460)
  })

  it.each(['wheel', 'touch', 'keyboard', 'session', 'deactivate', 'viewport'] as const)('corrects reflow and relinquishes the target on %s interruption', async (interruption) => {
    const harness = mountHarness([userMessage('target')])
    harness.scroll.markEscaped()
    harness.viewport.scrollTop = 100
    Object.defineProperty(harness.viewport, 'onscrollend', { configurable: true, value: null })
    let targetTop = 600
    const target = document.createElement('div')
    target.dataset.messageId = 'target'
    target.getBoundingClientRect = () => rect(targetTop - harness.viewport.scrollTop, 40)
    harness.content.append(target)
    harness.scrollTo.mockImplementation(() => {})
    await harness.scroll.scrollToMessage('target')
    expect(harness.scrollTo).toHaveBeenLastCalledWith({ top: 460, behavior: 'smooth' })

    targetTop += 320
    harness.geometry.scrollHeight += 320
    ResizeObserverMock.instances[0]?.trigger(harness.content)
    // Reflow must not restart the flight on every content update.
    expect(harness.scrollTo).toHaveBeenCalledTimes(1)
    harness.viewport.scrollTop = 460
    harness.viewport.dispatchEvent(new Event('scrollend'))
    expect(harness.scrollTo).toHaveBeenLastCalledWith({ top: 780, behavior: 'smooth' })

    // Cancelling the correction relinquishes the destination as well.
    harness.viewport.scrollTop = 550
    if (interruption === 'wheel') {
      harness.viewport.dispatchEvent(new WheelEvent('wheel', { deltaY: -100 }))
    } else if (interruption === 'touch') {
      harness.viewport.dispatchEvent(new Event('touchstart'))
    } else if (interruption === 'keyboard') {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'PageUp' }))
    } else if (interruption === 'session') {
      harness.sessionId.value = 'other-session'
      await nextTick()
    } else if (interruption === 'deactivate') {
      harness.scroll.onDeactivatedResetScroll()
    } else {
      harness.scrollEl.value = document.createElement('div')
      await nextTick()
    }
    const callsAfterCancel = harness.scrollTo.mock.calls.length
    targetTop += 100
    harness.viewport.dispatchEvent(new Event('scrollend'))
    // A new session may follow its own content; a late old scrollend must
    // never issue another leg toward the old message.
    expect(harness.scrollTo).toHaveBeenCalledTimes(callsAfterCancel)
  })

  it('re-resolves a send pin after its optimistic prompt is replaced', async () => {
    const harness = mountHarness([userMessage('previous')])
    harness.scroll.markEscaped()
    harness.geometry.clientHeight = 600
    harness.geometry.scrollHeight = 1600
    harness.viewport.scrollTop = 0
    Object.defineProperty(harness.viewport, 'onscrollend', { configurable: true, value: null })
    harness.scrollTo.mockImplementation(() => {})
    const makeTurn = (id: string, top: number, promptOffset: number) => {
      const turn = document.createElement('div')
      turn.dataset.chatTurn = ''
      turn.getBoundingClientRect = () => rect(top - harness.viewport.scrollTop, 500)
      const prompt = document.createElement('div')
      prompt.dataset.messageId = id
      prompt.getBoundingClientRect = () => rect(top + promptOffset - harness.viewport.scrollTop, 40)
      turn.append(prompt)
      return turn
    }
    harness.scroll.pinAfterSend()
    const optimistic = makeTurn('optimistic', 500, 0)
    harness.messages.value.push(userMessage('optimistic'))
    harness.lastTurnEl.value = optimistic
    harness.content.append(optimistic)
    await flushDom()
    expect(harness.scrollTo).toHaveBeenLastCalledWith({ top: 360, behavior: 'smooth' })

    const persisted = makeTurn('persisted', 820, 24)
    harness.messages.value.splice(1, 1, userMessage('persisted'))
    harness.lastTurnEl.value = persisted
    optimistic.replaceWith(persisted)
    await flushDom()
    harness.viewport.scrollTop = 360
    harness.viewport.dispatchEvent(new Event('scrollend'))
    expect(harness.scrollTo).toHaveBeenLastCalledWith({ top: 704, behavior: 'smooth' })
    expect(harness.scroll.turnReserveStyle('persisted')).toBeDefined()
  })
})
