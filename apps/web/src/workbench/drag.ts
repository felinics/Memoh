import { shallowRef } from 'vue'
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
  tabId: string
  sourceGroupId: string
  pinned: boolean
  title: string
  clientX: number
  clientY: number
  target: WorkbenchDropTarget | null
}

export const activeWorkbenchDrag = shallowRef<ActiveTabDragState | null>(null)

export interface StartTabDragOptions {
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

export interface StartExternalDragOptions {
  event: PointerEvent
  dragId: string
  getTitle: () => string
  isMobile: () => boolean
  getGroupTabIds: (groupId: string) => string[]
  isTabPinned: (tabId: string) => boolean
  onDragStateChange: (dragging: boolean) => void
  onCommitDrop: (targetGroupId: string, zone: WorkbenchDropZone, insertIndex?: number) => void
}

const DRAG_THRESHOLD_PX = 5
const EXTERNAL_DRAG_THRESHOLD_PX = 7
const HEADER_BAND_SLACK_PX = 18
const ROOT_EDGE_BAND_PX = 16
const SCROLLER_AUTOSCROLL_EDGE_PX = 32
const SCROLLER_AUTOSCROLL_STEP_PX = 12

function maybeAutoScrollHeader(headerEl: HTMLElement, clientX: number) {
  const scroller = headerEl.querySelector<HTMLElement>('[data-workbench-tab-scroller]')
  if (!scroller || scroller.scrollWidth <= scroller.clientWidth) return
  const sr = scroller.getBoundingClientRect()
  if (clientX >= sr.left && clientX < sr.left + SCROLLER_AUTOSCROLL_EDGE_PX) {
    scroller.scrollLeft -= SCROLLER_AUTOSCROLL_STEP_PX
  } else if (clientX <= sr.right && clientX > sr.right - SCROLLER_AUTOSCROLL_EDGE_PX) {
    scroller.scrollLeft += SCROLLER_AUTOSCROLL_STEP_PX
  }
}

function computeHeaderInsert(
  headerEl: HTMLElement,
  groupId: string,
  clientX: number,
  pinned: boolean,
  getGroupTabIds: (gid: string) => string[],
  isTabPinned: (tid: string) => boolean,
): {
  insertIndex: number
  lineX: number
  top: number
  height: number
} {
  const groupTabIds = getGroupTabIds(groupId)
  const pinnedCount = groupTabIds.filter(id => isTabPinned(id)).length
  const minIdx = pinned ? 0 : pinnedCount
  const maxIdx = pinned ? pinnedCount : groupTabIds.length

  const tabNodes = Array.from(
    headerEl.querySelectorAll<HTMLElement>('[data-workbench-tab-id]'),
  ).filter((el) => {
    if (el.classList.contains('wb-tab-leave-active')) return false
    const id = el.dataset.workbenchTabId ?? ''
    return isTabPinned(id) === pinned
  })

  const headerRect = headerEl.getBoundingClientRect()
  if (tabNodes.length === 0) {
    return {
      insertIndex: minIdx,
      lineX: headerRect.left + 12,
      top: headerRect.top + 6,
      height: Math.max(16, headerRect.height - 12),
    }
  }

  for (let i = 0; i < tabNodes.length; i += 1) {
    const r = tabNodes[i]!.getBoundingClientRect()
    const mid = r.left + r.width / 2
    if (clientX < mid) {
      return {
        insertIndex: Math.min(maxIdx, minIdx + i),
        lineX: r.left,
        top: r.top + 4,
        height: Math.max(16, r.height - 8),
      }
    }
  }
  const lastRect = tabNodes[tabNodes.length - 1]!.getBoundingClientRect()
  return {
    insertIndex: maxIdx,
    lineX: lastRect.right,
    top: lastRect.top + 4,
    height: Math.max(16, lastRect.height - 8),
  }
}

function resolveSlotOrRootDropTarget(
  clientX: number,
  clientY: number,
  isMobile: boolean,
  sourceGroupId: string | null,
  sourceGroupSingleTab: boolean,
): WorkbenchDropTarget | null {
  const allSlots = document.querySelectorAll<HTMLElement>('[data-workbench-slot-group-id]')

  // Outer root-edge full-span split when already in a multi-pane layout
  if (!isMobile && allSlots.length > 1) {
    const rootEl = document.querySelector<HTMLElement>('.workbench-root')
    if (rootEl) {
      const rr = rootEl.getBoundingClientRect()
      const inRoot = clientX >= rr.left && clientX <= rr.right && clientY >= rr.top && clientY <= rr.bottom
      if (inRoot && clientY > rr.top + 42) {
        if (clientY >= rr.bottom - ROOT_EDGE_BAND_PX) {
          return {
            groupId: WORKBENCH_ROOT_DROP_ID,
            zone: 'bottom',
            rect: { left: rr.left, top: rr.top + rr.height * 0.62, width: rr.width, height: rr.height * 0.38 },
          }
        }
        if (clientX <= rr.left + ROOT_EDGE_BAND_PX) {
          return {
            groupId: WORKBENCH_ROOT_DROP_ID,
            zone: 'left',
            rect: { left: rr.left, top: rr.top, width: rr.width * 0.5, height: rr.height },
          }
        }
        if (clientX >= rr.right - ROOT_EDGE_BAND_PX) {
          return {
            groupId: WORKBENCH_ROOT_DROP_ID,
            zone: 'right',
            rect: { left: rr.left + rr.width * 0.5, top: rr.top, width: rr.width * 0.5, height: rr.height },
          }
        }
      }
    }
  }

  for (const slotEl of allSlots) {
    const gid = slotEl.dataset.workbenchSlotGroupId
    if (!gid) continue
    const sr = slotEl.getBoundingClientRect()
    if (clientX < sr.left || clientX > sr.right || clientY < sr.top || clientY > sr.bottom) {
      continue
    }
    const rx = sr.width > 0 ? (clientX - sr.left) / sr.width : 0.5
    const ry = sr.height > 0 ? (clientY - sr.top) / sr.height : 0.5
    const sameSingleTabGroup = gid === sourceGroupId && sourceGroupSingleTab
    let zone: WorkbenchDropZone = 'center'
    if (!isMobile && !sameSingleTabGroup) {
      if (rx < 0.25) zone = 'left'
      else if (rx > 0.75) zone = 'right'
      else if (ry < 0.25) zone = 'top'
      else if (ry > 0.75) zone = 'bottom'
    }
    if (gid === sourceGroupId && zone === 'center') return null

    const rect = (() => {
      switch (zone) {
        case 'left':
          return { left: sr.left, top: sr.top, width: sr.width * 0.5, height: sr.height }
        case 'right':
          return { left: sr.left + sr.width * 0.5, top: sr.top, width: sr.width * 0.5, height: sr.height }
        case 'top':
          return { left: sr.left, top: sr.top, width: sr.width, height: sr.height * 0.5 }
        case 'bottom':
          return { left: sr.left, top: sr.top + sr.height * 0.5, width: sr.width, height: sr.height * 0.5 }
        case 'center':
          return { left: sr.left, top: sr.top, width: sr.width, height: sr.height }
      }
    })()

    return { groupId: gid, zone, rect }
  }

  return null
}

export function startWorkbenchTabPointerGesture(opts: StartTabDragOptions) {
  const { event } = opts
  if (event.button !== 0) return
  const startX = event.clientX
  const startY = event.clientY
  let started = false

  opts.onActivate()

  function maybeReorderWithinSourceStrip(headerEl: HTMLElement, clientX: number) {
    maybeAutoScrollHeader(headerEl, clientX)
    const groupTabIds = opts.getGroupTabIds(opts.groupId)
    const currentIdx = groupTabIds.indexOf(opts.tabId)
    if (currentIdx < 0) return

    const tabNodes = Array.from(
      headerEl.querySelectorAll<HTMLElement>('[data-workbench-tab-id]'),
    ).filter(el => !el.classList.contains('wb-tab-leave-active'))

    for (const el of tabNodes) {
      const siblingId = el.dataset.workbenchTabId
      if (!siblingId || siblingId === opts.tabId) continue
      if (opts.isTabPinned(siblingId) !== opts.pinned) continue
      const siblingIdx = groupTabIds.indexOf(siblingId)
      if (siblingIdx < 0) continue
      const rect = el.getBoundingClientRect()
      const mid = rect.left + rect.width / 2
      if (currentIdx < siblingIdx && clientX > mid) {
        opts.onReorderInGroup(opts.groupId, opts.tabId, siblingIdx)
        return
      }
      if (currentIdx > siblingIdx && clientX < mid) {
        opts.onReorderInGroup(opts.groupId, opts.tabId, siblingIdx)
        return
      }
    }
  }

  function resolveDropTarget(clientX: number, clientY: number): WorkbenchDropTarget | null {
    const sourceHeader = document.querySelector<HTMLElement>(
      `[data-workbench-header-group-id="${CSS.escape(opts.groupId)}"]`,
    )
    if (sourceHeader) {
      const hr = sourceHeader.getBoundingClientRect()
      if (
        clientX >= hr.left
        && clientX <= hr.right
        && clientY >= hr.top - HEADER_BAND_SLACK_PX
        && clientY <= hr.bottom + HEADER_BAND_SLACK_PX
      ) {
        maybeReorderWithinSourceStrip(sourceHeader, clientX)
        return null
      }
    }

    const allHeaders = document.querySelectorAll<HTMLElement>('[data-workbench-header-group-id]')
    for (const headerEl of allHeaders) {
      const gid = headerEl.dataset.workbenchHeaderGroupId
      if (!gid || gid === opts.groupId) continue
      const hr = headerEl.getBoundingClientRect()
      if (clientX >= hr.left && clientX <= hr.right && clientY >= hr.top && clientY <= hr.bottom) {
        maybeAutoScrollHeader(headerEl, clientX)
        const ins = computeHeaderInsert(headerEl, gid, clientX, opts.pinned, opts.getGroupTabIds, opts.isTabPinned)
        return {
          groupId: gid,
          zone: 'center',
          insertIndex: ins.insertIndex,
          rect: { left: ins.lineX - 1, top: ins.top, width: 2, height: ins.height },
          isInsertionLine: true,
        }
      }
    }

    return resolveSlotOrRootDropTarget(
      clientX,
      clientY,
      opts.isMobile(),
      opts.groupId,
      opts.getGroupTabIds(opts.groupId).length <= 1,
    )
  }

  function cleanup() {
    window.removeEventListener('pointermove', onPointerMove, true)
    window.removeEventListener('pointerup', onPointerUp, true)
    window.removeEventListener('pointercancel', onCancel, true)
    window.removeEventListener('keydown', onKeyDown, true)
    window.removeEventListener('blur', onCancel)
    if (started) {
      activeWorkbenchDrag.value = null
      opts.onDragStateChange(false)
    }
  }

  function onPointerMove(e: PointerEvent) {
    if (!started) {
      if (Math.hypot(e.clientX - startX, e.clientY - startY) < DRAG_THRESHOLD_PX) return
      started = true
      opts.onDragStateChange(true)
    }
    e.preventDefault()
    const target = resolveDropTarget(e.clientX, e.clientY)
    activeWorkbenchDrag.value = {
      tabId: opts.tabId,
      sourceGroupId: opts.groupId,
      pinned: opts.pinned,
      title: opts.getTitle(),
      clientX: e.clientX,
      clientY: e.clientY,
      target,
    }
  }

  function onPointerUp(e: PointerEvent) {
    if (started) {
      const finalTarget = resolveDropTarget(e.clientX, e.clientY) ?? activeWorkbenchDrag.value?.target ?? null
      if (finalTarget) {
        opts.onCommitDrop(opts.tabId, finalTarget.groupId, finalTarget.zone, finalTarget.insertIndex)
      }
    }
    cleanup()
  }

  function onCancel() {
    cleanup()
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      cleanup()
    }
  }

  window.addEventListener('pointermove', onPointerMove, true)
  window.addEventListener('pointerup', onPointerUp, true)
  window.addEventListener('pointercancel', onCancel, true)
  window.addEventListener('keydown', onKeyDown, true)
  window.addEventListener('blur', onCancel)
}

export function startWorkbenchExternalPointerDrag(opts: StartExternalDragOptions) {
  const { event } = opts
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  const startX = event.clientX
  const startY = event.clientY
  let started = false

  function resolveDropTarget(clientX: number, clientY: number): WorkbenchDropTarget | null {
    const allHeaders = document.querySelectorAll<HTMLElement>('[data-workbench-header-group-id]')
    for (const headerEl of allHeaders) {
      const gid = headerEl.dataset.workbenchHeaderGroupId
      if (!gid) continue
      const hr = headerEl.getBoundingClientRect()
      if (clientX >= hr.left && clientX <= hr.right && clientY >= hr.top && clientY <= hr.bottom) {
        maybeAutoScrollHeader(headerEl, clientX)
        const ins = computeHeaderInsert(headerEl, gid, clientX, false, opts.getGroupTabIds, opts.isTabPinned)
        return {
          groupId: gid,
          zone: 'center',
          insertIndex: ins.insertIndex,
          rect: { left: ins.lineX - 1, top: ins.top, width: 2, height: ins.height },
          isInsertionLine: true,
        }
      }
    }
    return resolveSlotOrRootDropTarget(clientX, clientY, opts.isMobile(), null, false)
  }

  function suppressNextClick(e: MouseEvent) {
    e.preventDefault()
    e.stopPropagation()
  }

  function cleanup() {
    window.removeEventListener('pointermove', onPointerMove, true)
    window.removeEventListener('pointerup', onPointerUp, true)
    window.removeEventListener('pointercancel', onCancel, true)
    window.removeEventListener('keydown', onKeyDown, true)
    window.removeEventListener('blur', onCancel)
    if (started) {
      activeWorkbenchDrag.value = null
      opts.onDragStateChange(false)
      window.addEventListener('click', suppressNextClick, { capture: true, once: true })
    }
  }

  function onPointerMove(e: PointerEvent) {
    if (!started) {
      if (Math.hypot(e.clientX - startX, e.clientY - startY) < EXTERNAL_DRAG_THRESHOLD_PX) return
      started = true
      opts.onDragStateChange(true)
    }
    e.preventDefault()
    const target = resolveDropTarget(e.clientX, e.clientY)
    activeWorkbenchDrag.value = {
      tabId: opts.dragId,
      sourceGroupId: '__external__',
      pinned: false,
      title: opts.getTitle(),
      clientX: e.clientX,
      clientY: e.clientY,
      target,
    }
  }

  function onPointerUp(e: PointerEvent) {
    if (started) {
      const finalTarget = resolveDropTarget(e.clientX, e.clientY) ?? activeWorkbenchDrag.value?.target ?? null
      if (finalTarget) {
        opts.onCommitDrop(finalTarget.groupId, finalTarget.zone, finalTarget.insertIndex)
      }
    }
    cleanup()
  }

  function onCancel() {
    cleanup()
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      cleanup()
    }
  }

  window.addEventListener('pointermove', onPointerMove, true)
  window.addEventListener('pointerup', onPointerUp, true)
  window.addEventListener('pointercancel', onCancel, true)
  window.addEventListener('keydown', onKeyDown, true)
  window.addEventListener('blur', onCancel)
}
