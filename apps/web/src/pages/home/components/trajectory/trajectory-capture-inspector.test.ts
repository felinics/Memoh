// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { computed, createApp, defineComponent, h, nextTick, reactive, shallowRef } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { HandlersContextTrajectoryEventResponse } from '@memohai/sdk'
import en from '@/i18n/locales/en.json'
import TrajectoryCaptureInspector from './trajectory-capture-inspector.vue'

const copyText = vi.fn().mockResolvedValue(true)
const current = shallowRef<HandlersContextTrajectoryEventResponse>({})
const previous = shallowRef<HandlersContextTrajectoryEventResponse>({})
const status = shallowRef('success')
const error = shallowRef<unknown>(null)

vi.mock('@felinic/ui', () => ({
  Button: defineComponent({ setup: (_, { slots, attrs }) => () => h('button', { type: 'button', ...attrs }, slots.default?.()) }),
  Skeleton: defineComponent({ setup: () => () => h('div') }),
  useClipboard: () => ({ copyText }),
  toast: { error: vi.fn() },
}))
vi.mock('../../composables/useContextTrajectoryEvent', () => ({
  useContextTrajectoryEvent: (id: { value: string }) => ({
    data: computed(() => id.value === '1' ? previous.value : current.value), status, error, refresh: vi.fn(),
  }),
}))

const mounted: { app: ReturnType<typeof createApp>, root: HTMLElement }[] = []
function mount(event = reactive({ id: '2', run_id: 'run', capture_id: 'capture', sequence: 2, stage: 'wire_request' })) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(defineComponent({ setup: () => () => h(TrajectoryCaptureInspector, {
    event, previousEventId: '1',
  }) }))
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounted.push({ app, root })
  return root
}
beforeEach(() => {
  HTMLElement.prototype.scrollIntoView = vi.fn()
  copyText.mockClear()
  status.value = 'success'
  error.value = null
  current.value = { event: { id: '2' }, complete: true, blocks: [] }
  previous.value = { event: { id: '1' }, complete: true, blocks: [] }
})
afterEach(() => {
  for (const { app, root } of mounted.splice(0)) { app.unmount(); root.remove() }
})

describe('captured context inspector', () => {
  it('hides cached content when the session becomes unavailable', () => {
    current.value.blocks = [{ kind: 'system', content: 'PRIVATE_BODY', available: true }]
    status.value = 'error'
    error.value = { code: 'context_lifecycle.not_found', status: 404 }
    const root = mount()
    expect(root.textContent).not.toContain('PRIVATE_BODY')
    expect(root.querySelector('[data-testid="capture-copy"]')).toBeNull()
  })

  it('recovers from a denied event when a new event loads successfully', async () => {
    const event = reactive({ id: '2', run_id: 'run', capture_id: 'capture', sequence: 2, stage: 'wire_request' })
    status.value = 'error'
    error.value = { code: 'context_lifecycle.access_denied', status: 403 }
    const root = mount(event)
    expect(root.querySelector('[data-testid="capture-forbidden"]')).not.toBeNull()
    status.value = 'success'
    error.value = null
    current.value = { event: { id: '3' }, complete: true, blocks: [{ kind: 'system', content: 'NEW_AUTHORIZED_BODY', available: true }] }
    event.id = '3'
    await nextTick()
    expect(root.querySelector('[data-testid="capture-forbidden"]')).toBeNull()
    expect(root.textContent).toContain('NEW_AUTHORIZED_BODY')
  })

  it('does not claim a content change when either side is unavailable', async () => {
    previous.value.blocks = [{ kind: 'system', label: 'system', hash: 'same', available: false }]
    current.value.blocks = [{ kind: 'system', label: 'system', hash: 'same', available: false }]
    const root = mount()
    root.querySelector<HTMLButtonElement>('[data-testid="capture-compare"]')!.click()
    await nextTick()
    expect(root.textContent).toContain('Cannot compare')
    expect(root.textContent).not.toContain('Changed')
  })
  it('copies the full input and reads every page beyond the preview', async () => {
    const content = `${'context '.repeat(2000)}FINAL_TAIL`
    current.value.blocks = [{ kind: 'request_body', label: 'request', content, hash: 'full', bytes: content.length, available: true }]
    const root = mount()
    expect(root.textContent).not.toContain('FINAL_TAIL')
    root.querySelector<HTMLButtonElement>('[data-testid="capture-copy"]')!.click()
    await nextTick()
    expect(copyText).toHaveBeenCalledWith(content)
    root.querySelector<HTMLButtonElement>('[data-testid="capture-expand"]')!.click()
    await nextTick()
    const page = root.querySelector<HTMLInputElement>('[data-testid="capture-page"]')!
    expect(page).not.toBeNull()
    let restored = ''
    for (let index = 1; index <= Number(page.max); index++) {
      page.value = String(index)
      page.dispatchEvent(new Event('change', { bubbles: true }))
      await nextTick()
      const visible = root.querySelector('pre')!.textContent!
      expect(visible.length).toBeLessThanOrEqual(8192)
      restored += visible
    }
    expect(restored).toBe(content)
    expect(root.textContent).toContain('FINAL_TAIL')
    root.querySelector<HTMLButtonElement>('[data-testid="capture-copy"]')!.click()
    await nextTick()
    expect(copyText).toHaveBeenLastCalledWith(content)
  })

  it('finds a literal across page boundaries and makes the final page reachable', async () => {
    const content = `${'a'.repeat(8190)}跨页🙂${'b'.repeat(18000)}FINAL_TAIL`
    current.value.blocks = [{ kind: 'request_body', label: 'request', content, available: true }]
    const root = mount()
    root.querySelector<HTMLButtonElement>('[data-testid="capture-expand"]')!.click()
    await nextTick()
    const search = root.querySelector<HTMLInputElement>('[data-testid="capture-search"]')!
    search.value = '跨页🙂'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    expect(root.querySelector('mark')?.textContent).toBe('跨页')
    root.querySelector<HTMLButtonElement>('[data-testid="capture-next-page"]')!.click()
    await nextTick()
    expect(root.querySelector('mark')?.textContent).toBe('🙂')
    search.value = 'FINAL_TAIL'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    expect(root.querySelector('mark')?.textContent).toBe('FINAL_TAIL')
    const page = root.querySelector<HTMLInputElement>('[data-testid="capture-page"]')!
    expect(page.value).toBe(page.max)
  })

  it('keeps a cross-page search match scrolled into view', async () => {
    HTMLElement.prototype.scrollIntoView = function () { this.closest('pre')!.scrollTop = 100 }
    current.value.blocks = [{ kind: 'request_body', label: 'request', content: `${'a'.repeat(12500)}SEARCH_NEEDLE${'b'.repeat(10000)}`, available: true }]
    const root = mount()
    root.querySelector<HTMLButtonElement>('[data-testid="capture-expand"]')!.click()
    await nextTick()
    const search = root.querySelector<HTMLInputElement>('[data-testid="capture-search"]')!
    search.value = 'SEARCH_NEEDLE'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    await nextTick()
    expect(root.querySelector('pre')!.scrollTop).toBe(100)
  })

  it('shows changed and removed content when comparing the preceding stage', async () => {
    previous.value.blocks = [
      { kind: 'system', label: 'system', content: 'OLD_SYSTEM', hash: 'old', available: true },
      { kind: 'context', label: 'removed', content: 'REMOVED_HISTORY', hash: 'removed', available: true },
    ]
    current.value.blocks = [{ kind: 'system', label: 'system', content: 'NEW_SYSTEM', hash: 'new', available: true }]
    const root = mount()
    root.querySelector<HTMLButtonElement>('[data-testid="capture-compare"]')!.click()
    await nextTick()
    expect(root.textContent).toContain('OLD_SYSTEM')
    expect(root.textContent).toContain('NEW_SYSTEM')
    expect(root.textContent).toContain('REMOVED_HISTORY')
  })

  it('reports unavailable content and renders captured markup as text', () => {
    current.value = { event: { id: '2' }, complete: false, blocks: [
      { kind: 'system', content: 'UNAVAILABLE_BODY', available: false },
      { kind: 'context', content: '<img src=x onerror="alert(1)">', available: true },
    ] }
    const root = mount()
    expect(root.querySelector('[data-testid="capture-incomplete"]')).not.toBeNull()
    expect(root.textContent).not.toContain('UNAVAILABLE_BODY')
    expect(root.querySelector('img')).toBeNull()
    expect(root.textContent).toContain('<img src=x')
  })

  it('hides cached content when workspace access is denied', () => {
    current.value.blocks = [{ kind: 'system', content: 'WORKSPACE_BODY', available: true }]
    status.value = 'error'
    error.value = { code: 'context_lifecycle.access_denied', status: 403 }
    const root = mount()
    expect(root.querySelector('[data-testid="capture-forbidden"]')).not.toBeNull()
    expect(root.textContent).not.toContain('WORKSPACE_BODY')
  })
})
