// @vitest-environment jsdom
import { createApp, nextTick, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, expect, it, vi } from 'vitest'
import SessionInfoRing from './session-info-ring.vue'
import en from '@/i18n/locales/en.json'

vi.mock('../composables/useSessionInfo', () => ({
  useSessionInfo: () => ({
    info: ref({ message_count: 7, skills: [] }),
    usedTokens: ref(22400),
    composition: ref(null),
    outputReserve: ref(0),
    autoCompactTokens: ref(null),
    compactionAvailable: ref(false),
    isCompacting: ref(false),
    triggerCompact: vi.fn(),
    contextPercent: ref(9),
    contextWindow: ref(256000),
    contextTokens: ref(22400),
    sessionId: ref('session-1'),
  }),
}))

// The panel's usage grid only renders while the popover is open.
const panelOpen = () => document.querySelector('.grid-cols-20') != null

let app: ReturnType<typeof createApp> | undefined
let root: HTMLDivElement | undefined

afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.unstubAllGlobals()
})

it('shows usage on hover, opens details on click, and keeps them open after the pointer leaves', async () => {
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(SessionInfoRing)
  app.use(createI18n({
    legacy: false,
    locale: 'en',
    messages: { en },
  }))
  app.mount(root)
  await nextTick()
  const trigger = root.querySelector('button')!
  expect(trigger.disabled).toBe(false)
  expect(panelOpen()).toBe(false)
  trigger.dispatchEvent(new MouseEvent('pointermove'))
  await new Promise(resolve => setTimeout(resolve, 250))
  await nextTick()
  expect(document.body.textContent).toContain('9% context used')
  expect(document.body.textContent).toContain('22.4K / 256.0K tokens')
  expect(panelOpen()).toBe(false)
  trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  trigger.focus()
  trigger.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
  trigger.click()
  await nextTick()
  await nextTick()
  expect(panelOpen()).toBe(true)
  trigger.dispatchEvent(new MouseEvent('mouseleave'))
  await new Promise(resolve => setTimeout(resolve, 250))
  expect(panelOpen()).toBe(true)
  trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  trigger.focus()
  trigger.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
  trigger.click()
  await nextTick()
  expect(trigger.getAttribute('aria-expanded')).toBe('false')
})
