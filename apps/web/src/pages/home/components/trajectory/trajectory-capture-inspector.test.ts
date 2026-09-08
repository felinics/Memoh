// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { computed, createApp, defineComponent, h, nextTick, shallowRef } from 'vue'
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
function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(defineComponent({ setup: () => () => h(TrajectoryCaptureInspector, {
    event: { id: '2', run_id: 'run', capture_id: 'capture', sequence: 2, stage: 'wire_request' }, previousEventId: '1',
  }) }))
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounted.push({ app, root })
  return root
}
beforeEach(() => {
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
  it('copies the full input and lets the reader expand beyond the preview', async () => {
    const content = `${'context '.repeat(2000)}FINAL_TAIL`
    current.value.blocks = [{ kind: 'request_body', label: 'request', content, hash: 'full', bytes: content.length, available: true }]
    const root = mount()
    expect(root.textContent).not.toContain('FINAL_TAIL')
    root.querySelector<HTMLButtonElement>('[data-testid="capture-copy"]')!.click()
    await nextTick()
    expect(copyText).toHaveBeenCalledWith(content)
    root.querySelector<HTMLButtonElement>('[data-testid="capture-expand"]')!.click()
    await nextTick()
    expect(root.textContent).toContain('FINAL_TAIL')
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
