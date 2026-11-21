// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'
import type { WorkbenchNode } from '@/workbench/types'

const storeState = vi.hoisted(() => ({
  isMobile: false,
  panelDragging: false,
  setSplitRatio: vi.fn(),
  activateGroup: vi.fn(),
}))

let reactiveStore: typeof storeState | null = null

vi.mock('@/store/workspace-tabs', () => ({
  useWorkspaceTabsStore: () => {
    reactiveStore ??= reactive(storeState)
    return reactiveStore
  },
}))

vi.mock('./workbench-tab-bar.vue', () => ({
  default: { template: '<div data-mock-tab-bar />' },
}))

function pointerEvent(type: string, init: PointerEventInit = {}): PointerEvent {
  if (typeof PointerEvent !== 'undefined') {
    return new PointerEvent(type, { bubbles: true, cancelable: true, ...init })
  }
  const ev = new MouseEvent(type, { bubbles: true, cancelable: true, ...init }) as PointerEvent
  Object.defineProperty(ev, 'pointerId', { value: init.pointerId ?? 1 })
  return ev
}

describe('WorkbenchTree sash pointer cleanup', () => {
  let host: HTMLDivElement | null = null
  let app: ReturnType<typeof createApp> | null = null

  beforeEach(() => {
    if (reactiveStore) {
      reactiveStore.isMobile = false
      reactiveStore.panelDragging = false
    } else {
      storeState.isMobile = false
      storeState.panelDragging = false
    }
    document.body.style.cursor = ''
  })

  afterEach(() => {
    app?.unmount()
    app = null
    host?.remove()
    host = null
    document.body.style.cursor = ''
  })

  it('clears panelDragging and body cursor on lostpointercapture, window blur, and mobile switch', async () => {
    const WorkbenchTree = (await import('./workbench-tree.vue')).default
    const splitNode: WorkbenchNode = {
      kind: 'split',
      id: 'split:1',
      axis: 'row',
      ratio: 0.5,
      first: { kind: 'group', id: 'g1', tabIds: ['a'], activeTabId: 'a', previewTabId: null },
      second: { kind: 'group', id: 'g2', tabIds: ['b'], activeTabId: 'b', previewTabId: null },
    }

    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp({
      setup: () => () => h(WorkbenchTree, { node: splitNode }),
    })
    app.mount(host)
    await nextTick()

    const sash = host.querySelector<HTMLElement>('[data-workbench-sash]')!
    expect(sash).not.toBeNull()

    // 1. lostpointercapture releases panelDragging
    sash.dispatchEvent(pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 1 }))
    expect(reactiveStore!.panelDragging).toBe(true)
    expect(document.body.style.cursor).toBe('col-resize')

    sash.dispatchEvent(pointerEvent('lostpointercapture', { pointerId: 1 }))
    expect(reactiveStore!.panelDragging).toBe(false)
    expect(document.body.style.cursor).toBe('')

    // 2. window blur releases panelDragging
    sash.dispatchEvent(pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 2 }))
    expect(reactiveStore!.panelDragging).toBe(true)
    window.dispatchEvent(new Event('blur'))
    expect(reactiveStore!.panelDragging).toBe(false)
    expect(document.body.style.cursor).toBe('')

    // 3. Entering mobile breakpoint mid-drag releases panelDragging
    sash.dispatchEvent(pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 3 }))
    expect(reactiveStore!.panelDragging).toBe(true)
    reactiveStore!.isMobile = true
    await nextTick()
    expect(reactiveStore!.panelDragging).toBe(false)

    // 4. Secondary pointerup is ignored, while window contextmenu cancels even behind a pointer shield
    reactiveStore!.isMobile = false
    await nextTick()
    sash.dispatchEvent(pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 4 }))
    expect(reactiveStore!.panelDragging).toBe(true)
    window.dispatchEvent(pointerEvent('pointerup', { button: 0, pointerId: 99 }))
    expect(reactiveStore!.panelDragging).toBe(true)
    window.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(reactiveStore!.panelDragging).toBe(false)
    expect(document.body.style.cursor).toBe('')
  })
})
