// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, type Component } from 'vue'
import { createI18n } from 'vue-i18n'
import BotAppCard from './bot-app-card.vue'
import AppDetailPanel from './app-detail-panel.vue'
import type { AppItem } from '@/composables/api/useApps'
import en from '@/i18n/locales/en.json'

const diagnostic = Array.from({ length: 80 }, (_, i) => `Download failed at step ${i}: ${'long-token-'.repeat(30)}`).join('\n')
let app: ReturnType<typeof createApp>
let root: HTMLDivElement

beforeEach(() => {
  vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
})

afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.unstubAllGlobals()
})

async function mount(component: Component, item: AppItem, onAction?: (action: string) => void) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(component, { item, onAction }) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  await nextTick()
}

describe('App error presentation', () => {
  it.each(['failed', 'partial'] as const)('keeps %s cards compact and opens the app detail', async status => {
    const actions: string[] = []
    await mount(BotAppCard, { app_id: 'bun', installation_id: 'bun-install', status, last_error: diagnostic }, action => actions.push(action))
    expect(root.textContent).not.toContain(diagnostic)
    const details = Array.from(root.querySelectorAll('button')).find(button => button.textContent?.trim() === 'View details')
    expect(details).toBeUndefined()
    root.querySelector<HTMLElement>('[role="button"]')!.click()
    await nextTick()
    expect(actions).toEqual(['open'])
  })

  it('renders errors.<code> in the detail panel and prefers it over recorded text', async () => {
    const item = reactive<AppItem>({ app_id: 'bun', installation_id: 'bun-install', status: 'failed', last_error: diagnostic, last_error_code: 'app.operation_failed' })
    await mount(AppDetailPanel, item)
    root.querySelector<HTMLButtonElement>('button[aria-expanded]:not([aria-haspopup])')!.click()
    await nextTick()
    expect(root.querySelector('pre')?.textContent).toBe(en.errors.app.operation_failed)
  })

  it('discloses the full diagnostic only on demand and resets when the app changes', async () => {
    const item = reactive<AppItem>({ app_id: 'bun', installation_id: 'bun-install', status: 'failed', last_error: diagnostic })
    await mount(AppDetailPanel, item)
    expect(root.textContent).not.toContain(diagnostic)
    const toggle = root.querySelector<HTMLButtonElement>('button[aria-expanded]:not([aria-haspopup])')
    expect(toggle).not.toBeNull()
    toggle!.click()
    await nextTick()
    expect(root.querySelector('pre')?.textContent).toBe(diagnostic)
    expect(root.querySelector('[role="region"]')?.getAttribute('tabindex')).toBe('0')
    const copy = Array.from(root.querySelectorAll('button')).find(button => button.textContent?.trim() === 'Copy error details')!
    copy.click()
    await nextTick()
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(diagnostic)
    item.app_id = 'go'
    item.installation_id = 'go-install'
    await nextTick()
    expect(root.querySelector('pre')).toBeNull()
    expect(toggle!.getAttribute('aria-expanded')).toBe('false')
  })

  it('shows attention for partial setup without inventing diagnostic information', async () => {
    await mount(AppDetailPanel, { app_id: 'bun', status: 'partial' })
    expect(root.textContent).toContain('Needs attention')
    expect(root.querySelector('button[aria-expanded]:not([aria-haspopup])')).toBeNull()
  })
})
