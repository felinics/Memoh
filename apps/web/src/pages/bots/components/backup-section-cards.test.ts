// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import BackupSectionCards from './backup-section-cards.vue'

describe('backup section selection', () => {
  let app: ReturnType<typeof createApp>
  let root: HTMLDivElement

  afterEach(() => {
    app?.unmount()
    root?.remove()
  })

  async function mount(options: { disabled?: boolean, count?: number, mode?: 'include' | 'strategy' } = {}) {
    const selection = ref<Record<string, 'skip' | 'merge' | 'replace'>>({ models: 'merge' })
    root = document.createElement('div')
    document.body.append(root)
    app = createApp({
      render: () => h(BackupSectionCards, {
        mode: options.mode ?? 'include',
        sections: [{ key: 'models', count: options.count ?? 1, items: ['Example model'] }],
        disabled: options.disabled,
        modelValue: selection.value,
        'onUpdate:modelValue': value => { selection.value = value },
      }),
    })
    app.use(createI18n({
      legacy: false,
      locale: 'en',
      messages: { en: { bots: { backup: {
        sections: { models: 'Models' },
        toggleDetails: 'Toggle details',
        notInBackup: 'No data',
        countBackup: '{n} in backup',
        strategy: { skip: 'Skip', merge: 'Merge', replace: 'Replace' },
      } } } },
    }))
    app.mount(root)
    await nextTick()
    return selection
  }

  it('toggles the section by clicking its right-hand check control', async () => {
    const selection = await mount()
    const check = root.querySelector<HTMLElement>('[role="checkbox"][aria-label="Models"]')!
    expect(check).not.toBeNull()
    expect(check.getAttribute('aria-checked')).toBe('true')
    check.click()
    await nextTick()
    expect(selection.value.models).toBe('skip')
    expect(check.getAttribute('aria-checked')).toBe('false')
    check.click()
    await nextTick()
    expect(selection.value.models).toBe('merge')
    expect(check.getAttribute('aria-checked')).toBe('true')
  })

  it('keeps label selection and detail expansion independent', async () => {
    const selection = await mount()
    const label = Array.from(root.querySelectorAll('span')).find(el => el.textContent === 'Models')!
    label.click()
    await nextTick()
    expect(selection.value.models).toBe('skip')
    root.querySelector<HTMLButtonElement>('[aria-label="Toggle details"]')!.click()
    await nextTick()
    expect(root.textContent).toContain('Example model')
    expect(selection.value.models).toBe('skip')
  })

  it('does not change a disabled section', async () => {
    const selection = await mount({ disabled: true })
    const check = root.querySelector<HTMLButtonElement>('[role="checkbox"]')!
    expect(check.disabled).toBe(true)
    check.click()
    Array.from(root.querySelectorAll('span')).find(el => el.textContent === 'Models')!.click()
    await nextTick()
    expect(selection.value.models).toBe('merge')
  })

  it('does not offer a checkbox for a section with no data', async () => {
    const selection = await mount({ count: 0 })
    expect(root.querySelector('[role="checkbox"]')).toBeNull()
    expect(root.textContent).toContain('No data')
    Array.from(root.querySelectorAll('span')).find(el => el.textContent === 'Models')!.click()
    await nextTick()
    expect(selection.value.models).toBe('merge')
  })

  it('preserves overwrite strategy selection', async () => {
    const selection = await mount({ mode: 'strategy' })
    expect(root.querySelector('[role="checkbox"]')).toBeNull()
    Array.from(root.querySelectorAll('button')).find(el => el.textContent?.trim() === 'Replace')!.click()
    await nextTick()
    expect(selection.value.models).toBe('replace')
  })
})
