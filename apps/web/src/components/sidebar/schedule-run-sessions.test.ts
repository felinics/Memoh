// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ScheduleRunSessions from './schedule-run-sessions.vue'

const mocks = vi.hoisted(() => ({
  fetchRuns: vi.fn(),
  openSessionChat: vi.fn(),
  unreadKinds: {} as Record<string, string>,
}))

vi.mock('@/composables/api/useScheduleRuns', () => ({
  fetchScheduleRunSessions: mocks.fetchRuns,
}))
vi.mock('@/store/chat-list', () => ({
  useChatStore: () => reactive({ sessionId: ref('session-2') }),
}))
vi.mock('@/store/unread', () => ({
  useUnreadStore: () => ({
    entries: {},
    kindOf: (sessionId: string) => mocks.unreadKinds[sessionId] ?? null,
    isFresh: () => false,
  }),
}))
vi.mock('@/store/workspace-tabs', () => ({
  useWorkspaceTabsStore: () => ({ openSessionChat: mocks.openSessionChat }),
}))

let app: ReturnType<typeof createApp> | undefined
let root: HTMLDivElement
let props: { botId: string, scheduleId: string, scheduleName: string, revision: number }

function run(id: number, extra: Record<string, unknown> = {}) {
  return {
    log_id: `log-${id}`,
    session_id: `session-${id}`,
    title: '',
    status: 'ok',
    started_at: new Date(2026, 9, 16, 14 - id, 0).toISOString(),
    completed_at: new Date(2026, 9, 16, 14 - id, 1).toISOString(),
    ...extra,
  }
}

async function flush() {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

function rows(): HTMLButtonElement[] {
  return Array.from(root.querySelectorAll<HTMLButtonElement>('[data-slot="schedule-run-row"]'))
}

function button(label: string): HTMLButtonElement {
  const el = Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.trim() === label)
  expect(el).toBeDefined()
  return el!
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 16, 15, 0))
  root = document.createElement('div')
  document.body.append(root)
  props = reactive({ botId: 'bot-1', scheduleId: 'sched-1', scheduleName: 'Daily AI news', revision: 0 })
})

afterEach(() => {
  app?.unmount()
  app = undefined
  root.remove()
  vi.useRealTimers()
})

async function mount() {
  app = createApp({ render: () => h(ScheduleRunSessions, props) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  await flush()
}

it('lists each run with its session title, falling back to the schedule name, and a 24-hour time', async () => {
  mocks.fetchRuns.mockResolvedValue({ items: [run(0, { title: 'Launch recap' }), run(1)], nextCursor: null })
  await mount()

  expect(mocks.fetchRuns).toHaveBeenCalledWith('bot-1', 'sched-1', { limit: 4 })
  const [first, second] = rows()
  expect(first!.textContent).toContain('Launch recap')
  expect(first!.textContent).toContain('Today 14:00')
  expect(second!.textContent).toContain('Daily AI news')
  expect(second!.textContent).toContain('Today 13:00')
})

it('opens the run session in the workspace and marks the active one as selected', async () => {
  mocks.fetchRuns.mockResolvedValue({ items: [run(1), run(2)], nextCursor: null })
  await mount()

  expect(rows()[0]!.hasAttribute('data-ui-selected')).toBe(false)
  expect(rows()[1]!.hasAttribute('data-ui-selected')).toBe(true)
  rows()[0]!.click()
  expect(mocks.openSessionChat).toHaveBeenCalledWith({ sessionId: 'session-1', title: 'Daily AI news' })
})

it('shows a run that never created a session as a disabled row', async () => {
  mocks.fetchRuns.mockResolvedValue({
    items: [run(1, { session_id: '', status: 'error', completed_at: new Date(2026, 9, 16, 13, 0).toISOString() })],
    nextCursor: null,
  })
  await mount()

  const [row] = rows()
  expect(row!.disabled).toBe(true)
  expect(row!.getAttribute('title')).toBe(en.bots.schedule.sessions.notStarted)
  row!.click()
  expect(mocks.openSessionChat).not.toHaveBeenCalled()
})

it('marks a run without completed_at as running', async () => {
  mocks.fetchRuns.mockResolvedValue({ items: [run(0, { completed_at: null })], nextCursor: null })
  await mount()

  expect(rows()[0]!.querySelector(`[aria-label="${en.chat.sessionStreaming}"]`)).not.toBeNull()
})

it('pages with the cursor and drops rows it already shows', async () => {
  mocks.fetchRuns
    .mockResolvedValueOnce({ items: [run(0), run(1), run(2), run(3)], nextCursor: 'c1' })
    .mockResolvedValueOnce({ items: [run(3), run(4)], nextCursor: null })
  await mount()

  button(en.chat.showMoreSessions).click()
  await flush()

  expect(mocks.fetchRuns).toHaveBeenLastCalledWith('bot-1', 'sched-1', { limit: 8, cursor: 'c1' })
  expect(rows().map(r => r.textContent?.includes('Today 10:00'))).toEqual([false, false, false, false, true])
  expect(rows()).toHaveLength(5)
  expect(Array.from(root.querySelectorAll('button')).some(b => b.textContent?.trim() === en.chat.showMoreSessions)).toBe(false)
})

it('reloads from the newest run when the schedule revision changes, keeping the revealed window', async () => {
  mocks.fetchRuns
    .mockResolvedValueOnce({ items: [run(1), run(2), run(3), run(4)], nextCursor: 'c1' })
    .mockResolvedValueOnce({ items: [run(5), run(6)], nextCursor: null })
    .mockResolvedValueOnce({ items: [run(0), run(1), run(2), run(3), run(4), run(5), run(6)], nextCursor: null })
  await mount()
  button(en.chat.showMoreSessions).click()
  await flush()

  props.revision = 1
  await flush()

  expect(mocks.fetchRuns).toHaveBeenLastCalledWith('bot-1', 'sched-1', { limit: 6 })
  expect(rows()).toHaveLength(7)
  expect(rows()[0]!.textContent).toContain('Today 14:00')
})

it('says so when the schedule has never run', async () => {
  mocks.fetchRuns.mockResolvedValue({ items: [], nextCursor: null })
  await mount()

  expect(root.textContent).toContain(en.bots.schedule.sessions.empty)
})

it('marks a run whose session is unread with its kind and a bold title', async () => {
  mocks.unreadKinds = { 'session-1': 'update' }
  mocks.fetchRuns.mockResolvedValue({ items: [run(1), run(2)], nextCursor: null })
  await mount()

  const [unreadRow, readRow] = rows()
  expect(unreadRow!.querySelector('[data-slot="attention-dot"]')?.getAttribute('data-kind')).toBe('update')
  expect(unreadRow!.querySelector('.font-semibold')).not.toBeNull()
  expect(readRow!.querySelector('[data-slot="attention-dot"]')).toBeNull()
  expect(readRow!.querySelector('.font-semibold')).toBeNull()
  mocks.unreadKinds = {}
})
