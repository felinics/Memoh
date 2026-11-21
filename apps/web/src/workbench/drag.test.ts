// @vitest-environment jsdom
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  activeWorkbenchDrag,
  cancelActiveWorkbenchDrag,
  startWorkbenchExternalPointerDrag,
  startWorkbenchTabPointerGesture,
} from './drag'

function pointerEvent(type: string, init: PointerEventInit = {}): PointerEvent {
  if (typeof PointerEvent !== 'undefined') {
    return new PointerEvent(type, { bubbles: true, cancelable: true, ...init })
  }
  const ev = new MouseEvent(type, { bubbles: true, cancelable: true, ...init }) as PointerEvent
  Object.defineProperty(ev, 'pointerId', { value: init.pointerId ?? 1 })
  return ev
}

describe('workbench pointer drag gestures', () => {
  let slotEl: HTMLDivElement | null = null

  beforeEach(() => {
    cancelActiveWorkbenchDrag()
    slotEl = document.createElement('div')
    slotEl.dataset.workbenchSlotGroupId = 'group:2'
    slotEl.getBoundingClientRect = () => ({
      left: 200,
      top: 100,
      right: 500,
      bottom: 400,
      width: 300,
      height: 300,
      x: 200,
      y: 100,
      toJSON: () => ({}),
    })
    document.body.appendChild(slotEl)
  })

  afterEach(() => {
    cancelActiveWorkbenchDrag()
    slotEl?.remove()
    slotEl = null
  })

  it('ignores macOS Ctrl+Click without activating or dragging the tab', () => {
    const onActivate = vi.fn()
    const onDragStateChange = vi.fn()

    startWorkbenchTabPointerGesture({
      event: pointerEvent('pointerdown', { button: 0, buttons: 1, ctrlKey: true, clientX: 10, clientY: 10 }),
      tabId: 'chat:1',
      groupId: 'group:1',
      pinned: false,
      getTitle: () => 'Chat 1',
      isMobile: () => false,
      getGroupTabIds: () => ['chat:1'],
      isTabPinned: () => false,
      onActivate,
      onDragStateChange,
      onReorderInGroup: vi.fn(),
      onCommitDrop: vi.fn(),
    })

    window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 60, clientY: 60 }))
    expect(onActivate).not.toHaveBeenCalled()
    expect(onDragStateChange).not.toHaveBeenCalled()
    expect(activeWorkbenchDrag.value).toBeNull()
  })

  it('aborts ghost drags when pointermove reports released primary button', () => {
    const onDragStateChange = vi.fn()
    const onCommitDrop = vi.fn()

    startWorkbenchTabPointerGesture({
      event: pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 20, clientY: 20 }),
      tabId: 'chat:1',
      groupId: 'group:1',
      pinned: false,
      getTitle: () => 'Chat 1',
      isMobile: () => false,
      getGroupTabIds: () => ['chat:1'],
      isTabPinned: () => false,
      onActivate: vi.fn(),
      onDragStateChange,
      onReorderInGroup: vi.fn(),
      onCommitDrop,
    })

    // Cross drag threshold while held -> dragging starts.
    window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 350, clientY: 250 }))
    expect(onDragStateChange).toHaveBeenCalledWith(true)
    expect(activeWorkbenchDrag.value?.target?.groupId).toBe('group:2')

    // Simulated return from iframe/native menu with primary button already released (buttons: 0).
    window.dispatchEvent(pointerEvent('pointermove', { buttons: 0, clientX: 360, clientY: 255 }))
    expect(onDragStateChange).toHaveBeenLastCalledWith(false)
    expect(activeWorkbenchDrag.value).toBeNull()
    expect(onCommitDrop).not.toHaveBeenCalled()
  })

  it('does not fall back to stale drop target when pointerup lands outside valid drop zones', () => {
    const onCommitDrop = vi.fn()

    startWorkbenchTabPointerGesture({
      event: pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 20, clientY: 20 }),
      tabId: 'chat:1',
      groupId: 'group:1',
      pinned: false,
      getTitle: () => 'Chat 1',
      isMobile: () => false,
      getGroupTabIds: () => ['chat:1'],
      isTabPinned: () => false,
      onActivate: vi.fn(),
      onDragStateChange: vi.fn(),
      onReorderInGroup: vi.fn(),
      onCommitDrop,
    })

    // Hover over valid slot group:2 so activeWorkbenchDrag.target becomes non-null.
    window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 350, clientY: 250 }))
    expect(activeWorkbenchDrag.value?.target?.groupId).toBe('group:2')

    // Fast flick straight onto void (0, 0) on pointerup -> must cancel rather than committing group:2.
    window.dispatchEvent(pointerEvent('pointerup', { button: 0, clientX: 0, clientY: 0 }))
    expect(onCommitDrop).not.toHaveBeenCalled()
    expect(activeWorkbenchDrag.value).toBeNull()
  })

  it('cancels active external drag cleanly without swallowing unrelated later clicks', () => {
    const onDragStateChange = vi.fn()
    const onCommitDrop = vi.fn()
    const unrelatedButton = document.createElement('button')
    const clickSpy = vi.fn()
    unrelatedButton.addEventListener('click', clickSpy)
    document.body.appendChild(unrelatedButton)

    try {
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 10, clientY: 10 }),
        getTitle: () => 'a.md',
        isMobile: () => false,
        getGroupTabIds: () => [],
        isTabPinned: () => false,
        onDragStateChange,
        onCommitDrop,
      })

      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 350, clientY: 250 }))
      expect(onDragStateChange).toHaveBeenCalledWith(true)
      expect(activeWorkbenchDrag.value).not.toBeNull()

      cancelActiveWorkbenchDrag()
      expect(onDragStateChange).toHaveBeenLastCalledWith(false)
      expect(activeWorkbenchDrag.value).toBeNull()
      expect(onCommitDrop).not.toHaveBeenCalled()

      unrelatedButton.click()
      expect(clickSpy).toHaveBeenCalledOnce()
    } finally {
      unrelatedButton.remove()
    }
  })

  it('suppresses the trailing sidebar click after Escape cancels an active external drag', () => {
    const onCommitDrop = vi.fn()
    const rowButton = document.createElement('button')
    const clickSpy = vi.fn()
    rowButton.addEventListener('click', clickSpy)
    document.body.appendChild(rowButton)

    try {
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 3, clientX: 10, clientY: 10 }),
        getTitle: () => 'session',
        isMobile: () => false,
        getGroupTabIds: () => [],
        isTabPinned: () => false,
        onDragStateChange: vi.fn(),
        onCommitDrop,
      })

      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, pointerId: 3, clientX: 350, clientY: 250 }))
      expect(activeWorkbenchDrag.value).not.toBeNull()

      // Press Escape while primary button is still held -> drag aborts.
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      expect(activeWorkbenchDrag.value).toBeNull()
      expect(onCommitDrop).not.toHaveBeenCalled()

      // User releases mouse button -> browser fires pointerup then trailing click, which must be suppressed.
      window.dispatchEvent(pointerEvent('pointerup', { button: 0, pointerId: 3, clientX: 10, clientY: 10 }))
      rowButton.click()
      expect(clickSpy).not.toHaveBeenCalled()

      // A genuine next click goes through normally.
      rowButton.click()
      expect(clickSpy).toHaveBeenCalledOnce()

      // If pointerup was lost over an iframe after Escape, a brand-new pointerdown clears the release suppressor so the new click succeeds.
      clickSpy.mockClear()
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 1, clientX: 10, clientY: 10 }),
        getTitle: () => 'session',
        isMobile: () => false,
        getGroupTabIds: () => [],
        isTabPinned: () => false,
        onDragStateChange: vi.fn(),
        onCommitDrop,
      })
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, pointerId: 1, clientX: 350, clientY: 250 }))
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      // Simulate missed pointerup, then a genuine new click sequence (pointerdown -> pointerup -> click).
      window.dispatchEvent(pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 1, clientX: 20, clientY: 20 }))
      window.dispatchEvent(pointerEvent('pointerup', { button: 0, pointerId: 1, clientX: 20, clientY: 20 }))
      rowButton.click()
      expect(clickSpy).toHaveBeenCalledOnce()
    } finally {
      rowButton.remove()
    }
  })

  it('ignores hidden zero-size group headers at (0, 0) when resolving drops', () => {
    const hiddenHeader = document.createElement('header')
    hiddenHeader.dataset.workbenchHeaderGroupId = 'group:hidden'
    hiddenHeader.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
    document.body.appendChild(hiddenHeader)

    const onCommitDrop = vi.fn()
    try {
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 40, clientY: 40 }),
        getTitle: () => 'file.md',
        isMobile: () => false,
        getGroupTabIds: () => [],
        isTabPinned: () => false,
        onDragStateChange: vi.fn(),
        onCommitDrop,
      })

      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 0, clientY: 0 }))
      expect(activeWorkbenchDrag.value?.target).toBeNull()
      window.dispatchEvent(pointerEvent('pointerup', { button: 0, clientX: 0, clientY: 0 }))
      expect(onCommitDrop).not.toHaveBeenCalled()
    } finally {
      hiddenHeader.remove()
    }
  })

  it('accepts external drops onto an empty workspace slot as center-only even near edges and ignores secondary pointerIds', () => {
    slotEl?.remove()
    const emptySlot = document.createElement('div')
    emptySlot.dataset.workbenchSlotGroupId = 'group:1'
    emptySlot.dataset.workbenchEmptySlot = 'true'
    emptySlot.getBoundingClientRect = () => new DOMRect(100, 100, 400, 300)
    document.body.appendChild(emptySlot)

    const onCommitDrop = vi.fn()
    try {
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, pointerId: 7, clientX: 10, clientY: 10 }),
        getTitle: () => 'empty.md',
        isMobile: () => false,
        getGroupTabIds: () => [],
        isTabPinned: () => false,
        onDragStateChange: vi.fn(),
        onCommitDrop,
      })

      // Move near left edge of empty slot -> must still resolve to center (not left split).
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, pointerId: 7, clientX: 110, clientY: 200 }))
      expect(activeWorkbenchDrag.value?.target?.groupId).toBe('group:1')
      expect(activeWorkbenchDrag.value?.target?.zone).toBe('center')

      // Unrelated secondary pointerup must be ignored.
      window.dispatchEvent(pointerEvent('pointerup', { button: 0, pointerId: 99, clientX: 0, clientY: 0 }))
      expect(activeWorkbenchDrag.value).not.toBeNull()
      expect(onCommitDrop).not.toHaveBeenCalled()

      // Primary pointerup commits cleanly to center of group:1.
      window.dispatchEvent(pointerEvent('pointerup', { button: 0, pointerId: 7, clientX: 110, clientY: 200 }))
      expect(onCommitDrop).toHaveBeenCalledWith('group:1', 'center', undefined)
      expect(activeWorkbenchDrag.value).toBeNull()
    } finally {
      emptySlot.remove()
    }
  })

  it('tracks 1:1 horizontal rail offset within the source header, reorders live, detaches outside header, and restores order on Escape', () => {
    const header = document.createElement('header')
    header.dataset.workbenchHeaderGroupId = 'group:1'
    header.getBoundingClientRect = () => new DOMRect(100, 0, 400, 40)

    const track = document.createElement('div')
    track.className = 'workbench-tab-track'
    track.getBoundingClientRect = () => new DOMRect(100, 0, 300, 40)

    const tabIds = ['tab:1', 'tab:2', 'tab:3']
    const tabEls = tabIds.map((id, i) => {
      const el = document.createElement('div')
      el.dataset.workbenchTabId = id
      Object.defineProperty(el, 'offsetLeft', { configurable: true, get: () => tabIds.indexOf(id) * 100 })
      Object.defineProperty(el, 'offsetWidth', { configurable: true, value: 100 })
      el.getBoundingClientRect = () => new DOMRect(100 + i * 100, 0, 100, 40)
      track.appendChild(el)
      return el
    })
    header.appendChild(track)
    document.body.appendChild(header)

    const onReorder = vi.fn((_gid: string, tid: string, targetIdx: number) => {
      const from = tabIds.indexOf(tid)
      tabIds.splice(from, 1)
      tabIds.splice(targetIdx, 0, tid)
    })
    const onCommitDrop = vi.fn()

    try {
      const downEv = pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 150, clientY: 20 })
      Object.defineProperty(downEv, 'currentTarget', { value: tabEls[0] })

      startWorkbenchTabPointerGesture({
        event: downEv,
        tabId: 'tab:1',
        groupId: 'group:1',
        pinned: false,
        getTitle: () => 'Tab 1',
        isMobile: () => false,
        getGroupTabIds: () => [...tabIds],
        isTabPinned: () => false,
        onActivate: vi.fn(),
        onDragStateChange: vi.fn(),
        onReorderInGroup: onReorder,
        onCommitDrop,
      })

      // Move +40px inside source strip (right edge 240 < tab:2 mid 250 + 4) -> glides 1:1 via --wb-drag-x without swapping yet.
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 190, clientY: 20 }))
      const railRef = activeWorkbenchDrag.value
      expect(railRef?.railTab).toEqual({ tabId: 'tab:1', groupId: 'group:1' })
      expect(railRef?.detachedTabId).toBeNull()
      expect(tabEls[0]!.style.getPropertyValue('--wb-drag-x')).toBe('40px')
      expect(onReorder).not.toHaveBeenCalled()

      // Continuing on the same rail updates --wb-drag-x directly on DOM without replacing activeWorkbenchDrag (0 reactive churn).
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 195, clientY: 20 }))
      expect(activeWorkbenchDrag.value).toBe(railRef)
      expect(tabEls[0]!.style.getPropertyValue('--wb-drag-x')).toBe('45px')

      // Move +60px (right edge 260 > tab:2 mid 250 + 4) -> swaps live into index 1 at ~50% overlap.
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 210, clientY: 20 }))
      expect(onReorder).toHaveBeenCalledWith('group:1', 'tab:1', 1)
      expect(tabIds).toEqual(['tab:2', 'tab:1', 'tab:3'])

      // Move to the last slot boundary (clientX 320 -> right edge 370 > tab:3 mid 350 + 4) -> swaps into the final slot cleanly.
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 320, clientY: 20 }))
      expect(tabIds).toEqual(['tab:2', 'tab:3', 'tab:1'])

      // Pull down outside header band -> clears --wb-drag-x, detaches from rail (floating chip mode), and targets slot group:2.
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 350, clientY: 250 }))
      expect(tabEls[0]!.style.getPropertyValue('--wb-drag-x')).toBe('')
      expect(activeWorkbenchDrag.value?.railTab).toBeNull()
      expect(activeWorkbenchDrag.value?.detachedTabId).toBe('tab:1')
      expect(activeWorkbenchDrag.value?.target?.groupId).toBe('group:2')

      // Press Escape -> cancels drop and restores original tab order.
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      expect(activeWorkbenchDrag.value).toBeNull()
      expect(onCommitDrop).not.toHaveBeenCalled()
      expect(tabIds).toEqual(['tab:1', 'tab:2', 'tab:3'])
    } finally {
      header.remove()
    }
  })

  it('animates moved siblings via WAAPI on first-tab reorder, skips pinned auto-scroll, and aborts cleanly if dragged tab vanishes', async () => {
    const header = document.createElement('header')
    header.dataset.workbenchHeaderGroupId = 'group:1'
    header.getBoundingClientRect = () => new DOMRect(100, 0, 400, 40)

    const pinnedZone = document.createElement('div')
    pinnedZone.dataset.workbenchPinnedZone = ''
    const pinnedTrack = document.createElement('div')
    pinnedTrack.className = 'workbench-tab-track'
    pinnedTrack.getBoundingClientRect = () => new DOMRect(100, 0, 80, 40)
    const pinnedEl = document.createElement('div')
    pinnedEl.dataset.workbenchTabId = 'pin:1'
    Object.defineProperty(pinnedEl, 'offsetLeft', { configurable: true, value: 0 })
    Object.defineProperty(pinnedEl, 'offsetWidth', { configurable: true, value: 80 })
    pinnedEl.getBoundingClientRect = () => new DOMRect(100, 0, 80, 40)
    pinnedTrack.appendChild(pinnedEl)
    pinnedZone.appendChild(pinnedTrack)

    const scroller = document.createElement('div')
    scroller.dataset.workbenchTabScroller = ''
    Object.defineProperty(scroller, 'scrollWidth', { configurable: true, value: 600 })
    Object.defineProperty(scroller, 'clientWidth', { configurable: true, value: 300 })
    scroller.scrollLeft = 50
    scroller.getBoundingClientRect = () => new DOMRect(180, 0, 300, 40)

    const track = document.createElement('div')
    track.className = 'workbench-tab-track'
    track.getBoundingClientRect = () => new DOMRect(180, 0, 300, 40)

    const tabIds = ['pin:1', 'tab:1', 'tab:2']
    const regularIds = () => tabIds.filter(id => !id.startsWith('pin:'))
    const animateSpies: Record<string, ReturnType<typeof vi.fn>> = {}
    const tabEls: Record<string, HTMLElement> = {}
    for (const id of ['tab:1', 'tab:2']) {
      const el = document.createElement('div')
      el.dataset.workbenchTabId = id
      Object.defineProperty(el, 'offsetLeft', { configurable: true, get: () => regularIds().indexOf(id) * 100 })
      Object.defineProperty(el, 'offsetWidth', { configurable: true, value: 100 })
      el.getBoundingClientRect = () => new DOMRect(180 + regularIds().indexOf(id) * 100, 0, 100, 40)
      const spy = vi.fn(() => ({ cancel: vi.fn(), onfinish: null, oncancel: null }))
      el.animate = spy as unknown as HTMLElement['animate']
      animateSpies[id] = spy
      tabEls[id] = el
      track.appendChild(el)
    }

    scroller.appendChild(track)
    header.append(pinnedZone, scroller)
    document.body.appendChild(header)

    const onReorder = vi.fn((_gid: string, tid: string, targetIdx: number) => {
      const from = tabIds.indexOf(tid)
      tabIds.splice(from, 1)
      tabIds.splice(targetIdx, 0, tid)
    })

    try {
      // 1. Dragging pinned tab near scroller edge must NOT scroll the unpinned scroller.
      const pinDown = pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 140, clientY: 20 })
      Object.defineProperty(pinDown, 'currentTarget', { value: pinnedEl })
      startWorkbenchTabPointerGesture({
        event: pinDown,
        tabId: 'pin:1',
        groupId: 'group:1',
        pinned: true,
        getTitle: () => 'Pinned',
        isMobile: () => false,
        getGroupTabIds: () => [...tabIds],
        isTabPinned: id => id.startsWith('pin:'),
        onActivate: vi.fn(),
        onDragStateChange: vi.fn(),
        onReorderInGroup: onReorder,
        onCommitDrop: vi.fn(),
      })
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 190, clientY: 20 }))
      expect(scroller.scrollLeft).toBe(50)
      window.dispatchEvent(pointerEvent('pointerup', { button: 0, clientX: 190, clientY: 20 }))

      // 2. Dragging first regular tab across sibling triggers WAAPI FLIP animation on tab:2.
      const tab1Down = pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 230, clientY: 20 })
      Object.defineProperty(tab1Down, 'currentTarget', { value: tabEls['tab:1'] })
      startWorkbenchTabPointerGesture({
        event: tab1Down,
        tabId: 'tab:1',
        groupId: 'group:1',
        pinned: false,
        getTitle: () => 'Tab 1',
        isMobile: () => false,
        getGroupTabIds: () => [...tabIds],
        isTabPinned: id => id.startsWith('pin:'),
        onActivate: vi.fn(),
        onDragStateChange: vi.fn(),
        onReorderInGroup: onReorder,
        onCommitDrop: vi.fn(),
      })
      // Move +60px -> right edge 340 > tab:2 mid 330 + 4 -> reorders tab:1 past tab:2.
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 290, clientY: 20 }))
      await nextTick()
      expect(tabIds).toEqual(['pin:1', 'tab:2', 'tab:1'])
      expect(animateSpies['tab:2']).toHaveBeenCalledWith(
        [{ transform: 'translate3d(100px, 0, 0)' }, { transform: 'translate3d(0, 0, 0)' }],
        { duration: 160, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' },
      )
      expect(animateSpies['tab:1']).not.toHaveBeenCalled()

      // 3. If tab:1 DOM element is removed while rail-dragging, next pointermove clears --wb-drag-x cleanly.
      tabEls['tab:1']!.remove()
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 295, clientY: 20 }))
      expect(tabEls['tab:1']!.style.getPropertyValue('--wb-drag-x')).toBe('')
    } finally {
      header.remove()
    }
  })

  it('clamps cross-group header insertion lines inside the target scroller and suppresses contextmenu only after drag starts', () => {
    const targetHeader = document.createElement('header')
    targetHeader.dataset.workbenchHeaderGroupId = 'group:2'
    targetHeader.getBoundingClientRect = () => new DOMRect(100, 0, 400, 40)

    const scroller = document.createElement('div')
    scroller.dataset.workbenchTabScroller = ''
    scroller.getBoundingClientRect = () => new DOMRect(100, 0, 200, 40)

    const overflowingTab = document.createElement('div')
    overflowingTab.dataset.workbenchTabId = 'tab:overflow'
    overflowingTab.getBoundingClientRect = () => new DOMRect(250, 0, 200, 40)
    scroller.appendChild(overflowingTab)
    targetHeader.appendChild(scroller)
    document.body.appendChild(targetHeader)

    try {
      // Right-click BEFORE crossing drag threshold must NOT suppress contextmenu (allows tab ContextMenu to open).
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 10, clientY: 10 }),
        getTitle: () => 'file.ts',
        isMobile: () => false,
        getGroupTabIds: () => ['tab:overflow'],
        isTabPinned: () => false,
        onDragStateChange: vi.fn(),
        onCommitDrop: vi.fn(),
      })
      const preStartCtx = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
      window.dispatchEvent(preStartCtx)
      expect(preStartCtx.defaultPrevented).toBe(false)

      // Drag onto target header past overflowing tab -> insertion line clamped inside scroller [102, 298].
      startWorkbenchExternalPointerDrag({
        event: pointerEvent('pointerdown', { button: 0, buttons: 1, clientX: 10, clientY: 10 }),
        getTitle: () => 'file.ts',
        isMobile: () => false,
        getGroupTabIds: () => ['tab:overflow'],
        isTabPinned: () => false,
        onDragStateChange: vi.fn(),
        onCommitDrop: vi.fn(),
      })
      window.dispatchEvent(pointerEvent('pointermove', { buttons: 1, clientX: 380, clientY: 20 }))
      expect(activeWorkbenchDrag.value?.target?.isInsertionLine).toBe(true)
      expect(activeWorkbenchDrag.value?.target?.rect.left).toBe(297) // clampLineX(450)=298, rect.left=298-1

      // Right-click AFTER crossing drag threshold cancels drag AND suppresses stray contextmenu.
      const postStartCtx = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
      window.dispatchEvent(postStartCtx)
      expect(postStartCtx.defaultPrevented).toBe(true)
      expect(activeWorkbenchDrag.value).toBeNull()
    } finally {
      targetHeader.remove()
    }
  })
})
