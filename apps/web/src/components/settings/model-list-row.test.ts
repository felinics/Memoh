// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'

vi.mock('lucide-vue-next', () => ({
  Settings: () => h('span', { 'data-testid': 'settings-icon' }),
}))

describe('ModelListRow', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  it('shows a template model as normal read-only content', async () => {
    const onClick = vi.fn()
    // The shim import pulls the whole @felinic/ui barrel through the transform
    // pipeline — under full-suite load that first touch can exceed the default
    // 5s test timeout, so this test gets a wider budget.
    const { ModelListRow } = await import('@felinic/ui')
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(ModelListRow, {
      label: 'Claude Sonnet 4.6',
      meta: 'claude-sonnet-4-6',
      readonly: true,
      onClick,
    })
    app.mount(root)
    await nextTick()

    const button = root.querySelector('button') as HTMLButtonElement
    expect(button.disabled).toBe(true)
    expect(button.classList).not.toContain('disabled:opacity-40')
    expect(root.textContent).toContain('Claude Sonnet 4.6')
    expect(root.querySelector('[data-testid="settings-icon"]')).toBeNull()

    button.click()
    expect(onClick).not.toHaveBeenCalled()
  }, 15000)
})
