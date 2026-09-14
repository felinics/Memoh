// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App } from 'vue'
import GroupHeader from './group-header.vue'

let app: App | undefined
let root: HTMLDivElement

async function flush() {
  await Promise.resolve()
  await nextTick()
}

function mount(props: { label: string; expanded: boolean; onToggle?: () => void }) {
  root = document.createElement('div')
  document.body.appendChild(root)
  app = createApp(GroupHeader, props)
  app.mount(root)
  return app
}

afterEach(() => {
  app?.unmount()
  root?.remove()
  app = undefined
})

function row(): HTMLElement {
  const el = root.querySelector<HTMLElement>('[role="button"]')
  expect(el).not.toBeNull()
  return el!
}

it('renders the label and aria-expanded from props', async () => {
  mount({ label: 'work', expanded: true })
  await flush()
  expect(root.textContent).toContain('work')
  expect(row().getAttribute('aria-expanded')).toBe('true')
})

it('emits toggle on click and on Enter/Space keys', async () => {
  const onToggle = vi.fn()
  mount({ label: 'work', expanded: false, onToggle })
  await flush()
  const el = row()
  el.click()
  el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  el.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
  await flush()
  expect(onToggle).toHaveBeenCalledTimes(3)
})

it('reveals the chevron only on hover/focus and rotates it when expanded', async () => {
  mount({ label: 'work', expanded: true })
  await flush()
  const chevron = row().querySelector(':scope > svg')
  expect(chevron).not.toBeNull()
  const cls = chevron!.getAttribute('class') ?? ''
  expect(cls).toContain('opacity-0')
  expect(cls).toContain('group-hover/group-header:opacity-100')
  expect(cls).toContain('rotate-90')
})

it('does not toggle or swallow native activation when a nested button receives Enter/Space', async () => {
  const onToggle = vi.fn()
  const onInnerClick = vi.fn()
  root = document.createElement('div')
  document.body.appendChild(root)
  app = createApp({
    setup: () => () => h(GroupHeader, { label: 'work', expanded: false, onToggle }, {
      default: () => h('button', { class: 'trailing-action', onClick: onInnerClick }, '+'),
    }),
  })
  app.mount(root)
  await flush()
  const inner = root.querySelector<HTMLElement>('.trailing-action')!
  inner.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
  inner.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
  await flush()
  expect(onToggle).not.toHaveBeenCalled()
  inner.click()
  expect(onInnerClick).toHaveBeenCalledTimes(1)
})

it('renders trailing slot content at the far right', async () => {
  root = document.createElement('div')
  document.body.appendChild(root)
  app = createApp({
    render: () => h(GroupHeader, { label: 'work', expanded: false }, {
      default: () => h('button', { class: 'trailing-action' }, '+'),
    }),
  })
  app.mount(root)
  await flush()
  const action = root.querySelector('.trailing-action')
  expect(action).not.toBeNull()
  expect(action!.parentElement?.className).toContain('ml-auto')
})
