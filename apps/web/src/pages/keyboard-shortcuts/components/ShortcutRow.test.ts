// @vitest-environment jsdom
import { createApp, h, nextTick, type App } from 'vue'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, expect, it } from 'vitest'
import ShortcutRow from './ShortcutRow.vue'
import i18n from '@/i18n'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { useKeyboardShortcutsStore } from '@/store/keyboard-shortcuts'

let app: App | undefined
let host: HTMLElement | undefined
beforeEach(() => { localStorage.clear() })
afterEach(() => { app?.unmount(); host?.remove() })

async function mountRow() {
  host = document.createElement('div')
  document.body.append(host)
  const pinia = createPinia()
  const store = useKeyboardShortcutsStore(pinia)
  const binding = store.effectiveBindings.find(b => b.command === appKeyboardCommands.newTerminal)!
  app = createApp({ setup: () => () => h(ShortcutRow, { binding }) }).use(pinia).use(i18n)
  app.mount(host)
  await nextTick()
  return { host, store }
}

it('explains a saved shortcut that no longer passes the checks and offers the reset', async () => {
  localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newTerminal]: 'Mod+c' }))
  const { host, store } = await mountRow()
  expect(host.querySelector('[data-slot="field-error"]')?.textContent).toBe(
    'Saved shortcut Ctrl+C isn\'t available here, so the default is used. This combination is used for text editing (copy, paste, undo…).',
  )
  expect([...host.querySelectorAll('kbd kbd')].map(kbd => kbd.textContent?.trim())).toEqual(['Alt', 'Shift', 'X'])
  host.querySelector<HTMLButtonElement>('button[aria-label="Reset to default"]')!.click()
  await nextTick()
  expect(store.overrides).toEqual({})
  expect(host.querySelector('[data-slot="field-error"]')).toBeNull()
})

it('shows a valid saved shortcut without a notice', async () => {
  localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newTerminal]: 'Mod+Alt+Shift+F9' }))
  const { host } = await mountRow()
  expect(host.querySelector('[data-slot="field-error"]')).toBeNull()
})
