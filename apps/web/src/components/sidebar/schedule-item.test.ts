// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ScheduleItem from './schedule-item.vue'

// reka-ui primitives resolve their own Vue copy under vitest; the card only
// needs the menu as a passthrough and the switch as a plain toggle button.
vi.mock('@felinic/ui', async (importOriginal) => {
  const { defineComponent, h: render } = await import('vue')
  const passthrough = defineComponent({ setup: (_, { slots }) => () => slots.default?.() })
  return {
    ...await importOriginal<object>(),
    DropdownMenu: passthrough,
    DropdownMenuTrigger: passthrough,
    DropdownMenuContent: passthrough,
    DropdownMenuItem: defineComponent({
      emits: ['select'],
      setup: (_, { slots, emit }) => () => render('button', { 'data-menu-item': '', onClick: () => emit('select') }, slots.default?.()),
    }),
    DropdownMenuSeparator: passthrough,
    Switch: defineComponent({
      props: { modelValue: Boolean },
      emits: ['update:modelValue'],
      setup: (props, { emit }) => () => render('button', {
        role: 'switch',
        'aria-checked': String(props.modelValue),
        onClick: () => emit('update:modelValue', !props.modelValue),
      }),
    }),
  }
})

let app: ReturnType<typeof createApp> | undefined
let root: HTMLDivElement

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  root = document.createElement('div')
  document.body.append(root)
})

afterEach(() => {
  app?.unmount()
  app = undefined
  root.remove()
  vi.unstubAllGlobals()
})

interface CardProps {
  expandable?: boolean
  expanded?: boolean
  pinnedSession?: boolean
  unreadCount?: number
  onReadAll?: () => void
  onOpen?: () => void
  onToggle?: (enabled: boolean) => void
}

async function mount(props: CardProps) {
  const state = reactive({
    item: { id: 'sched-1', name: 'Daily AI news', enabled: true },
    description: 'Collect the day’s AI news',
    timeLabel: '08:00',
    ...props,
  })
  app = createApp({
    render: () => h(ScheduleItem, state, { default: () => h('div', { 'data-runs': '' }, 'runs') }),
  })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  await nextTick()
  return state
}

function header(): HTMLElement {
  const el = root.querySelector<HTMLElement>('[role="button"]')
  expect(el).not.toBeNull()
  return el!
}

it('renders the expanded slot below the header only while expanded', async () => {
  const state = await mount({ expandable: true, expanded: false })
  expect(header().getAttribute('aria-expanded')).toBe('false')
  expect(root.querySelector('[data-runs]')).toBeNull()

  state.expanded = true
  await nextTick()
  expect(header().getAttribute('aria-expanded')).toBe('true')
  expect(root.querySelector('[data-runs]')).not.toBeNull()
})

it('marks a pinned-session schedule and never announces it as expandable', async () => {
  await mount({ pinnedSession: true })
  expect(header().hasAttribute('aria-expanded')).toBe(false)
  expect(header().getAttribute('title')).toBe(en.bots.schedule.sessions.openPinned)
  expect(root.querySelector(`[aria-label="${en.bots.schedule.sessions.pinned}"]`)).not.toBeNull()
  expect(root.querySelector('[data-runs]')).toBeNull()
})

it('emits open from the header and keeps the switch from opening the card', async () => {
  const onOpen = vi.fn()
  const onToggle = vi.fn()
  await mount({ expandable: true, onOpen, onToggle })
  header().click()
  expect(onOpen).toHaveBeenCalledTimes(1)

  root.querySelector<HTMLButtonElement>('[role="switch"]')!.click()
  await nextTick()
  expect(onToggle).toHaveBeenCalledWith(false)
  expect(onOpen).toHaveBeenCalledTimes(1)
})

function menuItem(label: string): HTMLButtonElement | undefined {
  return Array.from(root.querySelectorAll<HTMLButtonElement>('[data-menu-item]')).find(b => b.textContent?.trim() === label)
}

it('offers to read all runs only while some are unread', async () => {
  const onReadAll = vi.fn()
  const state = await mount({ expandable: true, unreadCount: 0, onReadAll })
  expect(menuItem(en.notifications.clearAll)).toBeUndefined()

  state.unreadCount = 3
  await nextTick()
  menuItem(en.notifications.clearAll)!.click()
  expect(onReadAll).toHaveBeenCalledTimes(1)
})

it('has no read-all for a pinned-session schedule', async () => {
  await mount({ pinnedSession: true, unreadCount: 2 })
  expect(menuItem(en.notifications.clearAll)).toBeUndefined()
})
