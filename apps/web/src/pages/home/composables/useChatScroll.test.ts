// @vitest-environment jsdom
import type { App, Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { ChatMessage } from '@/store/chat-list'
import { animateScrollTo, useChatScroll } from './useChatScroll'

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
  content: HTMLElement
  geometry: ScrollGeometry
  scrollTo: ReturnType<typeof vi.fn>
  messages: Ref<ChatMessage[]>
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

function provisionalSteerMessage(id: string, text = id): ChatMessage {
  return {
    ...userMessage(id, text),
    turnId: `queue-steer:${id}`,
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

  const messages = ref<ChatMessage[]>(initialMessages)
  const lastTurnEl = ref<HTMLElement | null>(null)
  let scroll!: ChatScroll
  const app = createApp(defineComponent({
    setup() {
      scroll = useChatScroll({
        scrollEl: ref(viewport),
        contentEl: ref(content),
        lastTurnEl,
        messages,
        isActive: ref(true),
        sessionId: ref('session-1'),
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
    content,
    geometry,
    scrollTo,
    messages,
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

describe('animateScrollTo', () => {
  function manualClock() {
    let now = 0
    let nextId = 1
    const frames = new Map<number, FrameRequestCallback>()
    return {
      now: () => now,
      raf: (callback: FrameRequestCallback) => {
        const id = nextId++
        frames.set(id, callback)
        return id
      },
      caf: (id: number) => frames.delete(id),
      step: (elapsed: number) => {
        now += elapsed
        const current = [...frames.values()]
        frames.clear()
        for (const callback of current) callback(now)
      },
      pending: () => frames.size,
    }
  }

  it('lands exactly on the target', () => {
    const clock = manualClock()
    const el = { scrollTop: 20 }

    animateScrollTo(el, () => 220, {
      duration: 400,
      now: clock.now,
      raf: clock.raf,
      caf: clock.caf,
    })
    clock.step(400)

    expect(el.scrollTop).toBe(220)
    expect(clock.pending()).toBe(0)
  })

  it('re-reads a moving target during the tween', () => {
    const clock = manualClock()
    const el = { scrollTop: 0 }
    let target = 100

    animateScrollTo(el, () => target, {
      duration: 400,
      now: clock.now,
      raf: clock.raf,
      caf: clock.caf,
    })
    clock.step(200)
    target = 240
    clock.step(200)

    expect(el.scrollTop).toBe(240)
  })

  it('stops writing after cancellation', () => {
    const clock = manualClock()
    const el = { scrollTop: 0 }
    const cancel = animateScrollTo(el, () => 100, {
      duration: 400,
      now: clock.now,
      raf: clock.raf,
      caf: clock.caf,
    })

    cancel()
    clock.step(400)

    expect(el.scrollTop).toBe(0)
    expect(clock.pending()).toBe(0)
  })
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

  it('hands the pin reserve from the active turn to a provisional steer', async () => {
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
    secondPrompt.dataset.messageId = 'user-2'
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
    harness.messages.value.push(userMessage('user-2'), assistantMessage('assistant-2'))
    harness.content.append(secondTurn)
    harness.lastTurnEl.value = secondTurn
    await flushDom()
    expect(harness.scroll.turnReserveStyle('user-2')).toBeDefined()

    const steerTurn = document.createElement('div')
    const steerPrompt = document.createElement('div')
    steerPrompt.dataset.messageId = 'steer-user'
    steerTurn.append(steerPrompt)
    steerTurn.getBoundingClientRect = () => rect(120, 60)
    steerPrompt.getBoundingClientRect = () => rect(120, 40)
    Object.defineProperty(steerTurn, 'offsetHeight', {
      configurable: true,
      get: () => Math.max(60, Number.parseFloat(steerTurn.style.minHeight) || 0),
    })
    Object.defineProperty(steerPrompt, 'offsetHeight', {
      configurable: true,
      get: () => 40,
    })
    harness.messages.value.push(provisionalSteerMessage('steer-user'))
    harness.content.append(steerTurn)
    harness.lastTurnEl.value = steerTurn

    harness.scroll.pinAfterSteer('user-2')
    await flushDom()

    expect(harness.scroll.turnReserveStyle('user-2')).toBeUndefined()
    expect(harness.scroll.turnReserveStyle('steer-user')).toBeDefined()
  })
})
