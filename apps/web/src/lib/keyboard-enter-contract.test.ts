// @vitest-environment jsdom
import { readdirSync, readFileSync } from 'node:fs'
import { createApp, h, nextTick } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import HeaderRow from '@/pages/home/components/tool-detail/header-row.vue'

const workspaceDirs = ['../pages/home/', '../components/sidebar/', '../components/file-manager/']

function vueFiles(dir: URL): URL[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => entry.isDirectory()
    ? vueFiles(new URL(`${entry.name}/`, dir))
    : entry.name.endsWith('.vue') ? [new URL(entry.name, dir)] : [])
}

describe('Enter in the chat workspace', () => {
  it('acts only on a plain Enter so modified Enter reaches the shortcut dispatcher', () => {
    const offenders: string[] = []
    for (const file of workspaceDirs.flatMap(dir => vueFiles(new URL(dir, import.meta.url)))) {
      readFileSync(file, 'utf8').split('\n').forEach((line, index) => {
        const enter = [...line.matchAll(/@keydown\.enter((?:\.[a-z]+)*)/g)].some(match => !match[1]!.split('.').includes('exact'))
        if (enter || /@keydown\.stop\b/.test(line)) offenders.push(`${file.pathname.split('/src/')[1]}:${index + 1}`)
      })
    }
    expect(offenders).toEqual([])
  })

  it('toggles a nested tool row on a plain Enter only', async () => {
    const toggle = vi.fn()
    const root = document.createElement('div')
    const app = createApp(() => h(HeaderRow, { nested: true, onToggle: toggle }))
    app.mount(root)
    await nextTick()
    const row = root.querySelector<HTMLElement>('[role="button"]')!
    const modified = new KeyboardEvent('keydown', { key: 'Enter', altKey: true, shiftKey: true, bubbles: true, cancelable: true })
    row.dispatchEvent(modified)
    expect(toggle).not.toHaveBeenCalled()
    expect(modified.defaultPrevented).toBe(false)
    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(toggle).toHaveBeenCalledOnce()
    app.unmount()
  })
})
