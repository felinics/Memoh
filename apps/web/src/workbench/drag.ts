import { nextTick, shallowRef } from 'vue'
import { WORKBENCH_ROOT_DROP_ID } from './tree'
import type { WorkbenchDropZone } from './types'

export interface WorkbenchDropTarget {
  groupId: string
  zone: WorkbenchDropZone
  insertIndex?: number
  /** Viewport-relative coordinates converted by workspace root into local overlay rect */
  rect: { left: number, top: number, width: number, height: number }
  isInsertionLine?: boolean
}

export interface ActiveTabDragState {
  title: string
  clientX: number
  clientY: number
  target: WorkbenchDropTarget | null
  /** Non-null while an existing tab glides 1:1 along its source strip (offset written directly to --wb-drag-x). */
  railTab: { tabId: string, groupId: string } | null
  /** Set while an existing tab is detached from its source strip for cross-split / split-drop. */
  detachedTabId: string | null
}

export const activeWorkbenchDrag = shallowRef<ActiveTabDragState | null>(null)

let activeGestureCancel: (() => void) | null = null

/** Abort any active pointer drag or sash gesture and remove its global window listeners. */
export function cancelActiveWorkbenchDrag() {
  activeGestureCancel?.()
}

interface StartTabDragOptions {
  event: PointerEvent
  tabId: string
  groupId: string
  pinned: boolean
  getTitle: () => string
  isMobile: () => boolean
  getGroupTabIds: (groupId: string) => string[]
  isTabPinned: (tabId: string) => boolean
  onActivate: () => void
  onDragStateChange: (dragging: boolean) => void
  onReorderInGroup: (groupId: string, tabId: string, targetIndex: number) => void
  onCommitDrop: (tabId: string, targetGroupId: string, zone: WorkbenchDropZone, insertIndex?: number) => void
}

interface StartExternalDragOptions {
  event: PointerEvent
  getTitle: () => string
  isMobile: () => boolean
  getGroupTabIds: (groupId: string) => string[]
  isTabPinned: (tabId: string) => boolean
  onDragStateChange: (dragging: boolean) => void
  onCommitDrop: (targetGroupId: string, zone: WorkbenchDropZone, insertIndex?: number) => void
}

interface StartSashDragOptions {
  event: PointerEvent
  captureEl: HTMLElement | null
  onStart: () => void
  onMove: (event: PointerEvent) => void
  onEnd: () => void
}

const DRAG_THRESHOLD_PX = 5
const EXTERNAL_DRAG_THRESHOLD_PX = 7
const HEADER_BAND_SLACK_PX = 18
const ROOT_EDGE_BAND_PX = 16
const SCROLLER_AUTOSCROLL_EDGE_PX = 32
const SCROLLER_AUTOSCROLL_STEP_PX = 12

interface PointerSessionOptions {
  event: PointerEvent
  thresholdPx: number
  captureEl?: HTMLElement | null
  preventContextMenu?: boolean
  suppressClickAfterDrag?: boolean
  onStart?: () => void
  onMove: (event: PointerEvent) => void
  onUp?: (event: PointerEvent) => void
  onEnd: (reason: EndReason) => void
}

type EndReason = 'up' | 'user-cancel' | 'programmatic'

/** Shared window-scoped pointer session used by tab drag, sidebar drag, and split sash resize. */
function startPointerSession(opts: PointerSessionOptions): () => void {
  cancelActiveWorkbenchDrag()
  const { event, thresholdPx, captureEl } = opts
  const activePointerId = event.pointerId
  const startX = event.clientX
  const startY = event.clientY
  const trackPrimaryButton = (event.buttons & 1) === 1
  const ac = new AbortController()
  const { signal } = ac
  let started = thresholdPx === 0
  let ended = false

  function suppressNextClickWindow() {
    const clickAc = new AbortController()
    window.addEventListener('click', (e) => {
      e.preventDefault()
      e.stopPropagation()
      clickAc.abort()
    }, { capture: true, signal: clickAc.signal })
    setTimeout(() => clickAc.abort(), 100)
  }

  function finish(reason: EndReason) {
    if (ended) return
    ended = true
    ac.abort()
    if (captureEl && captureEl.hasPointerCapture?.(activePointerId)) {
      try {
        captureEl.releasePointerCapture(activePointerId)
      } catch {
        // Ignore if pointer capture already released by the browser.
      }
    }
    if (activeGestureCancel === cancel) activeGestureCancel = null
    if (!started) return

    opts.onEnd(reason)
    if (!opts.suppressClickAfterDrag) return
    if (reason === 'up') {
      suppressNextClickWindow()
    } else if (reason === 'user-cancel') {
      // Pressing Escape or right-clicking while the primary button is still down leaves a trailing click on release.
      const releaseAc = new AbortController()
      window.addEventListener('pointerup', (e) => {
        if (e.pointerId !== activePointerId) return
        releaseAc.abort()
        suppressNextClickWindow()
      }, { capture: true, signal: releaseAc.signal })
      window.addEventListener('pointerdown', () => releaseAc.abort(), { capture: true, signal: releaseAc.signal })
      window.addEventListener('pointercancel', () => releaseAc.abort(), { capture: true, signal: releaseAc.signal })
      window.addEventListener('blur', () => releaseAc.abort(), { signal: releaseAc.signal })
      setTimeout(() => releaseAc.abort(), 2000)
    }
  }

  const cancel = () => finish('programmatic')

  if (captureEl) {
    try {
      captureEl.setPointerCapture?.(activePointerId)
    } catch {
      // Synthetic or already-ended pointers cannot be captured; window listeners still govern cleanup.
    }
    captureEl.addEventListener('lostpointercapture', (e) => {
      const pe = e as PointerEvent
      if (typeof pe.pointerId === 'number' && pe.pointerId !== activePointerId) return
      finish('programmatic')
    }, { signal })
  }
  if (started) opts.onStart?.()

  activeGestureCancel = cancel
  window.addEventListener('pointermove', (e) => {
    if (e.pointerId !== activePointerId) return
    if (trackPrimaryButton && (e.buttons & 1) === 0) {
      finish('programmatic')
      return
    }
    if (!started) {
      if (Math.hypot(e.clientX - startX, e.clientY - startY) < thresholdPx) return
      started = true
      opts.onStart?.()
    }
    e.preventDefault()
    opts.onMove(e)
  }, { capture: true, signal })
  window.addEventListener('pointerup', (e) => {
    if (e.pointerId !== activePointerId) return
    if (started) opts.onUp?.(e)
    finish('up')
  }, { capture: true, signal })
  window.addEventListener('pointercancel', (e) => {
    if (e.pointerId !== activePointerId) return
    finish('programmatic')
  }, { capture: true, signal })
  window.addEventListener('contextmenu', (e) => {
    if (opts.preventContextMenu) e.preventDefault()
    finish('user-cancel')
  }, { capture: true, signal })
  window.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      finish('user-cancel')
    }
  }, { capture: true, signal })
  window.addEventListener('blur', () => finish('programmatic'), { signal })
  return cancel
}

function readHeaderAutoScroll(headerEl: HTMLElement, clientX: number): { scroller: HTMLElement | null, delta: number } {
  const scroller = headerEl.querySelector<HTMLElement>('[data-workbench-tab-scroller]')
  if (!scroller || scroller.scrollWidth <= scroller.clientWidth) return { scroller: null, delta: 0 }
  const sr = scroller.getBoundingClientRect()
  const maxScroll = scroller.scrollWidth - scroller.clientWidth
  let nextScroll = scroller.scrollLeft
  if (clientX >= sr.left && clientX < sr.left + SCROLLER_AUTOSCROLL_EDGE_PX) {
    nextScroll = Math.max(0, scroller.scrollLeft - SCROLLER_AUTOSCROLL_STEP_PX)
  } else if (clientX <= sr.right && clientX > sr.right - SCROLLER_AUTOSCROLL_EDGE_PX) {
    nextScroll = Math.min(maxScroll, scroller.scrollLeft + SCROLLER_AUTOSCROLL_STEP_PX)
  }
  return { scroller, delta: nextScroll - scroller.scrollLeft }
}

function computeHeaderInsert(
  headerEl: HTMLElement,
  groupId: string,
  clientX: number,
  pinned: boolean,
  getGroupTabIds: (gid: string) => string[],
  isTabPinned: (tid: string) => boolean,
): { insertIndex: number, lineX: number, top: number, height: number } {
  const groupTabIds = getGroupTabIds(groupId)
  const pinnedCount = groupTabIds.filter(id => isTabPinned(id)).length
  const minIdx = pinned ? 0 : pinnedCount
  const maxIdx = pinned ? pinnedCount : groupTabIds.length

  const tabNodes = Array.from(headerEl.querySelectorAll<HTMLElement>('[data-workbench-tab-id]'))
    .filter(el => !el.classList.contains('wb-tab-leave-active') && isTabPinned(el.dataset.workbenchTabId ?? '') === pinned)

  const headerRect = headerEl.getBoundingClientRect()
  if (tabNodes.length === 0) {
    const pinnedZone = !pinned ? headerEl.querySelector<HTMLElement>('[data-workbench-pinned-zone]')?.getBoundingClientRect() : null
    const scroller = !pinned ? headerEl.querySelector<HTMLElement>('[data-workbench-tab-scroller]')?.getBoundingClientRect() : null
    const prefixNav = headerEl.querySelector<HTMLElement>('[data-workbench-prefix-nav]')?.getBoundingClientRect()
    const lineX = pinnedZone && pinnedZone.width > 0
      ? pinnedZone.right + 4
      : scroller && scroller.left > headerRect.left
        ? scroller.left + 4
        : prefixNav && prefixNav.width > 0
          ? prefixNav.right + 4
          : headerRect.left + 12
    return { insertIndex: minIdx, lineX, top: headerRect.top + 6, height: Math.max(16, headerRect.height - 12) }
  }

  for (let i = 0; i < tabNodes.length; i += 1) {
    const r = tabNodes[i]!.getBoundingClientRect()
    if (clientX < r.left + r.width / 2) {
      return { insertIndex: Math.min(maxIdx, minIdx + i), lineX: r.left, top: r.top + 4, height: Math.max(16, r.height - 8) }
    }
  }
  const lastRect = tabNodes[tabNodes.length - 1]!.getBoundingClientRect()
  return { insertIndex: maxIdx, lineX: lastRect.right, top: lastRect.top + 4, height: Math.max(16, lastRect.height - 8) }
}

function resolveOtherHeaderTarget(
  allHeaders: HTMLElement[],
  clientX: number,
  clientY: number,
  excludeGroupId: string | null,
  pinned: boolean,
  getGroupTabIds: (gid: string) => string[],
  isTabPinned: (tid: string) => boolean,
): WorkbenchDropTarget | null {
  for (const headerEl of allHeaders) {
    const gid = headerEl.dataset.workbenchHeaderGroupId
    if (!gid || gid === excludeGroupId) continue
    const hr = headerEl.getBoundingClientRect()
    if (hr.width <= 0 || hr.height <= 0) continue
    if (clientX >= hr.left && clientX <= hr.right && clientY >= hr.top && clientY <= hr.bottom) {
      const scroll = readHeaderAutoScroll(headerEl, clientX)
      const ins = computeHeaderInsert(headerEl, gid, clientX, pinned, getGroupTabIds, isTabPinned)
      if (scroll.delta !== 0 && scroll.scroller) scroll.scroller.scrollLeft += scroll.delta
      return {
        groupId: gid,
        zone: 'center',
        insertIndex: ins.insertIndex,
        rect: { left: ins.lineX - scroll.delta - 1, top: ins.top, width: 2, height: ins.height },
        isInsertionLine: true,
      }
    }
  }
  return null
}

function resolveSlotOrRootDropTarget(
  clientX: number,
  clientY: number,
  isMobile: boolean,
  sourceGroupId: string | null,
  sourceGroupSingleTab: boolean,
): WorkbenchDropTarget | null {
  const allSlots = Array.from(document.querySelectorAll<HTMLElement>('[data-workbench-slot-group-id]'))
  const visibleNonEmptySlots = allSlots.filter((el) => {
    if (el.dataset.workbenchEmptySlot === 'true') return false
    const r = el.getBoundingClientRect()
    return r.width > 0 && r.height > 0
  })

  if (!isMobile && visibleNonEmptySlots.length > 1) {
    const rootEl = document.querySelector<HTMLElement>('.workbench-root')
    if (rootEl) {
      const rr = rootEl.getBoundingClientRect()
      if (clientX >= rr.left && clientX <= rr.right && clientY > rr.top + 42 && clientY <= rr.bottom) {
        if (clientY >= rr.bottom - ROOT_EDGE_BAND_PX) {
          return { groupId: WORKBENCH_ROOT_DROP_ID, zone: 'bottom', rect: { left: rr.left, top: rr.top + rr.height * 0.62, width: rr.width, height: rr.height * 0.38 } }
        }
        if (clientX <= rr.left + ROOT_EDGE_BAND_PX) {
          return { groupId: WORKBENCH_ROOT_DROP_ID, zone: 'left', rect: { left: rr.left, top: rr.top, width: rr.width * 0.5, height: rr.height } }
        }
        if (clientX >= rr.right - ROOT_EDGE_BAND_PX) {
          return { groupId: WORKBENCH_ROOT_DROP_ID, zone: 'right', rect: { left: rr.left + rr.width * 0.5, top: rr.top, width: rr.width * 0.5, height: rr.height } }
        }
      }
    }
  }

  for (const slotEl of allSlots) {
    const gid = slotEl.dataset.workbenchSlotGroupId
    if (!gid) continue
    const sr = slotEl.getBoundingClientRect()
    if (sr.width <= 0 || sr.height <= 0 || clientX < sr.left || clientX > sr.right || clientY < sr.top || clientY > sr.bottom) {
      continue
    }
    const rx = (clientX - sr.left) / sr.width
    const ry = (clientY - sr.top) / sr.height
    const allowSplit = !isMobile && !(gid === sourceGroupId && sourceGroupSingleTab) && slotEl.dataset.workbenchEmptySlot !== 'true'
    let zone: WorkbenchDropZone = 'center'
    if (allowSplit) {
      if (rx < 0.25) zone = 'left'
      else if (rx > 0.75) zone = 'right'
      else if (ry < 0.25) zone = 'top'
      else if (ry > 0.75) zone = 'bottom'
    }
    if (gid === sourceGroupId && zone === 'center') return null

    const rect = zone === 'left'
      ? { left: sr.left, top: sr.top, width: sr.width * 0.5, height: sr.height }
      : zone === 'right'
        ? { left: sr.left + sr.width * 0.5, top: sr.top, width: sr.width * 0.5, height: sr.height }
        : zone === 'top'
          ? { left: sr.left, top: sr.top, width: sr.width, height: sr.height * 0.5 }
          : zone === 'bottom'
            ? { left: sr.left, top: sr.top + sr.height * 0.5, width: sr.width, height: sr.height * 0.5 }
            : { left: sr.left, top: sr.top, width: sr.width, height: sr.height }
    return { groupId: gid, zone, rect }
  }

  return null
}

function startDropDrag(opts: {
  event: PointerEvent
  thresholdPx: number
  suppressClickAfterDrag?: boolean
  getTitle: () => string
  resolveState: (clientX: number, clientY: number, committing: boolean) => {
    target: WorkbenchDropTarget | null
    railTab?: { tabId: string, groupId: string } | null
    detachedTabId?: string | null
  }
  onDragStateChange: (dragging: boolean) => void
  onCommit: (target: WorkbenchDropTarget) => void
  onEnd?: (reason: EndReason) => void
}) {
  startPointerSession({
    event: opts.event,
    thresholdPx: opts.thresholdPx,
    suppressClickAfterDrag: opts.suppressClickAfterDrag,
    onStart: () => opts.onDragStateChange(true),
    onMove: (e) => {
      const resolved = opts.resolveState(e.clientX, e.clientY, false)
      const railTab = resolved.railTab ?? null
      const detachedTabId = resolved.detachedTabId ?? null
      const prev = activeWorkbenchDrag.value
      // While gliding along the same rail, --wb-drag-x writes straight to the dragged DOM element;
      // skip reactive state churn so 120Hz pointermove never re-renders the tab bar.
      if (
        railTab
        && prev?.railTab
        && prev.railTab.tabId === railTab.tabId
        && prev.railTab.groupId === railTab.groupId
        && prev.target === null
        && prev.detachedTabId === null
      ) {
        return
      }
      activeWorkbenchDrag.value = {
        title: opts.getTitle(),
        clientX: e.clientX,
        clientY: e.clientY,
        target: resolved.target,
        railTab,
        detachedTabId,
      }
    },
    onUp: (e) => {
      const resolved = opts.resolveState(e.clientX, e.clientY, true)
      if (resolved.target) opts.onCommit(resolved.target)
    },
    onEnd: (reason) => {
      activeWorkbenchDrag.value = null
      opts.onEnd?.(reason)
      opts.onDragStateChange(false)
    },
  })
}

export function startWorkbenchSashPointerDrag(opts: StartSashDragOptions): (() => void) | null {
  const { event } = opts
  if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return null
  event.preventDefault()
  event.stopPropagation()
  return startPointerSession({
    event,
    thresholdPx: 0,
    captureEl: opts.captureEl,
    preventContextMenu: true,
    onStart: opts.onStart,
    onMove: opts.onMove,
    onEnd: () => opts.onEnd(),
  })
}

export function startWorkbenchTabPointerGesture(opts: StartTabDragOptions) {
  if (opts.event.button !== 0 || opts.event.ctrlKey) return
  opts.onActivate()

  const sourceTabEl = ((opts.event.currentTarget ?? opts.event.target) as HTMLElement | null)?.closest<HTMLElement>('[data-workbench-tab-id]')
  const sourceTabRect = sourceTabEl?.getBoundingClientRect()
  const grabOffsetX = sourceTabRect && sourceTabRect.width > 0 ? opts.event.clientX - sourceTabRect.left : 0
  const initialIndex = opts.getGroupTabIds(opts.groupId).indexOf(opts.tabId)
  let lastClientX = opts.event.clientX
  let activeRailEl: HTMLElement | null = null

  function setRailOffset(el: HTMLElement | null, offsetX: number) {
    if (activeRailEl && activeRailEl !== el) {
      activeRailEl.style.removeProperty('--wb-drag-x')
    }
    activeRailEl = el
    if (el) el.style.setProperty('--wb-drag-x', `${offsetX}px`)
  }

  function clearRailOffset() {
    if (!activeRailEl) return
    activeRailEl.style.removeProperty('--wb-drag-x')
    activeRailEl = null
  }

  function computeRailLayout(headerEl: HTMLElement, clientX: number, scrollDelta: number) {
    const tabNodes = Array.from(headerEl.querySelectorAll<HTMLElement>('[data-workbench-tab-id]'))
      .filter(el => !el.classList.contains('wb-tab-leave-active') && opts.isTabPinned(el.dataset.workbenchTabId ?? '') === opts.pinned)
    const tabEl = tabNodes.find(el => el.dataset.workbenchTabId === opts.tabId)
    const trackEl = tabEl?.closest<HTMLElement>('.workbench-tab-track')
    if (!tabEl || !trackEl || tabNodes.length === 0) return null

    const trackLeft = trackEl.getBoundingClientRect().left - scrollDelta
    const layoutLeft = (el: HTMLElement) => (el.offsetWidth > 0 ? trackLeft + el.offsetLeft : el.getBoundingClientRect().left - scrollDelta)
    const layoutWidth = (el: HTMLElement) => (el.offsetWidth > 0 ? el.offsetWidth : el.getBoundingClientRect().width)

    const tabWidth = layoutWidth(tabEl)
    const first = tabNodes[0]!
    const last = tabNodes[tabNodes.length - 1]!
    const minLeft = layoutLeft(first)
    const maxLeft = Math.max(minLeft, layoutLeft(last) + layoutWidth(last) - tabWidth)
    const desiredLeft = Math.min(Math.max(clientX - grabOffsetX, minLeft), maxLeft)
    return {
      tabEl,
      tabNodes,
      layoutLeft,
      layoutWidth,
      tabWidth,
      desiredLeft,
      offsetX: desiredLeft - layoutLeft(tabEl),
    }
  }

  function updateWithinSourceStrip(headerEl: HTMLElement, clientX: number) {
    lastClientX = clientX
    // Read phase: compute scroll delta and untransformed slot geometry before any DOM write (0 forced reflows).
    const scroll = readHeaderAutoScroll(headerEl, clientX)
    const layout = computeRailLayout(headerEl, clientX, scroll.delta)
    if (!layout) {
      clearRailOffset()
      return
    }

    const groupTabIds = opts.getGroupTabIds(opts.groupId)
    const currentIdx = groupTabIds.indexOf(opts.tabId)
    let targetIdx = currentIdx
    if (currentIdx >= 0) {
      const draggedLeft = layout.desiredLeft
      const draggedRight = layout.desiredLeft + layout.tabWidth
      for (const el of layout.tabNodes) {
        const siblingId = el.dataset.workbenchTabId
        if (!siblingId || siblingId === opts.tabId) continue
        const siblingIdx = groupTabIds.indexOf(siblingId)
        if (siblingIdx < 0) continue
        const siblingMid = layout.layoutLeft(el) + layout.layoutWidth(el) / 2
        if (currentIdx < siblingIdx && draggedRight > siblingMid + 4) {
          targetIdx = Math.max(targetIdx, siblingIdx)
        } else if (currentIdx > siblingIdx && draggedLeft < siblingMid - 4) {
          targetIdx = Math.min(targetIdx, siblingIdx)
        }
      }
    }

    // Write phase: apply scroll, compositor CSS variable, and discrete reorder (if crossed).
    if (scroll.delta !== 0 && scroll.scroller) scroll.scroller.scrollLeft += scroll.delta
    setRailOffset(layout.tabEl, layout.offsetX)

    if (targetIdx !== currentIdx && currentIdx >= 0) {
      opts.onReorderInGroup(opts.groupId, opts.tabId, targetIdx)
      // Re-sync --wb-drag-x directly on DOM after Vue patches order (before paint) without triggering another Vue render.
      void nextTick(() => {
        if (!activeRailEl) return
        const fresh = computeRailLayout(headerEl, lastClientX, 0)
        if (fresh) setRailOffset(fresh.tabEl, fresh.offsetX)
      })
    }
  }

  startDropDrag({
    event: opts.event,
    thresholdPx: DRAG_THRESHOLD_PX,
    getTitle: opts.getTitle,
    onDragStateChange: opts.onDragStateChange,
    resolveState: (clientX, clientY, committing) => {
      const allHeaders = Array.from(document.querySelectorAll<HTMLElement>('[data-workbench-header-group-id]'))
      const sourceHeader = allHeaders.find(el => el.dataset.workbenchHeaderGroupId === opts.groupId)
      if (sourceHeader) {
        const hr = sourceHeader.getBoundingClientRect()
        if (hr.width > 0 && hr.height > 0 && clientX >= hr.left && clientX <= hr.right && clientY >= hr.top - HEADER_BAND_SLACK_PX && clientY <= hr.bottom + HEADER_BAND_SLACK_PX) {
          if (committing) {
            clearRailOffset()
            return { target: null, railTab: null, detachedTabId: null }
          }
          updateWithinSourceStrip(sourceHeader, clientX)
          return {
            target: null,
            railTab: { tabId: opts.tabId, groupId: opts.groupId },
            detachedTabId: null,
          }
        }
      }
      clearRailOffset()
      const target = resolveOtherHeaderTarget(allHeaders, clientX, clientY, opts.groupId, opts.pinned, opts.getGroupTabIds, opts.isTabPinned)
        ?? resolveSlotOrRootDropTarget(clientX, clientY, opts.isMobile(), opts.groupId, opts.getGroupTabIds(opts.groupId).length <= 1)
      return { target, railTab: null, detachedTabId: opts.tabId }
    },
    onCommit: t => opts.onCommitDrop(opts.tabId, t.groupId, t.zone, t.insertIndex),
    onEnd: (reason) => {
      clearRailOffset()
      if (reason === 'user-cancel' && initialIndex >= 0) {
        const currentIdx = opts.getGroupTabIds(opts.groupId).indexOf(opts.tabId)
        if (currentIdx >= 0 && currentIdx !== initialIndex) {
          opts.onReorderInGroup(opts.groupId, opts.tabId, initialIndex)
        }
      }
    },
  })
}

export function startWorkbenchExternalPointerDrag(opts: StartExternalDragOptions) {
  const { event } = opts
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return

  startDropDrag({
    event,
    thresholdPx: EXTERNAL_DRAG_THRESHOLD_PX,
    suppressClickAfterDrag: true,
    getTitle: opts.getTitle,
    onDragStateChange: opts.onDragStateChange,
    resolveState: (clientX, clientY) => {
      const allHeaders = Array.from(document.querySelectorAll<HTMLElement>('[data-workbench-header-group-id]'))
      const target = resolveOtherHeaderTarget(allHeaders, clientX, clientY, null, false, opts.getGroupTabIds, opts.isTabPinned)
        ?? resolveSlotOrRootDropTarget(clientX, clientY, opts.isMobile(), null, false)
      return { target }
    },
    onCommit: t => opts.onCommitDrop(t.groupId, t.zone, t.insertIndex),
  })
}
