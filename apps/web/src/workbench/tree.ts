import type {
  WorkbenchAdjacentDirection,
  WorkbenchDropZone,
  WorkbenchGroupNode,
  WorkbenchNode,
  WorkbenchNormalizedRect,
  WorkbenchSplitDirection,
  WorkbenchState,
  WorkbenchTab,
  WorkbenchTabTarget,
} from './types'

export const WORKBENCH_MIN_COL_PX = 220
export const WORKBENCH_MIN_ROW_PX = 140
export const WORKBENCH_ROOT_DROP_ID = '__root__'
export const WORKBENCH_MIN_RATIO = 0.1
export const WORKBENCH_MAX_RATIO = 0.9

let seq = 0
function nextInternalId(prefix: 'group' | 'split', existing: Set<string>): string {
  do {
    seq += 1
  } while (existing.has(`${prefix}:${seq}`))
  return `${prefix}:${seq}`
}

function collectNodeIds(node: WorkbenchNode, out = new Set<string>()): Set<string> {
  out.add(node.id)
  if (node.kind === 'split') {
    collectNodeIds(node.first, out)
    collectNodeIds(node.second, out)
  }
  return out
}

export function createEmptyWorkbenchState(groupId = 'group:1'): WorkbenchState {
  return {
    version: 1,
    root: {
      kind: 'group',
      id: groupId,
      tabIds: [],
      activeTabId: null,
      previewTabId: null,
    },
    activeGroupId: groupId,
    tabs: {},
  }
}

export function listGroups(node: WorkbenchNode, out: WorkbenchGroupNode[] = []): WorkbenchGroupNode[] {
  if (node.kind === 'group') {
    out.push(node)
    return out
  }
  listGroups(node.first, out)
  listGroups(node.second, out)
  return out
}

export function findGroup(node: WorkbenchNode, groupId: string | null | undefined): WorkbenchGroupNode | null {
  if (!groupId) return null
  if (node.kind === 'group') return node.id === groupId ? node : null
  return findGroup(node.first, groupId) ?? findGroup(node.second, groupId)
}

export function findGroupOfTab(node: WorkbenchNode, tabId: string): WorkbenchGroupNode | null {
  if (node.kind === 'group') return node.tabIds.includes(tabId) ? node : null
  return findGroupOfTab(node.first, tabId) ?? findGroupOfTab(node.second, tabId)
}

/**
 * In any binary axis-aligned partition of [0,1]×[0,1], `first` always owns the
 * top/left half of its parent box, so walking `first` down to a leaf reaches
 * the unique group containing the top-left origin (0, 0).
 */
export function findSpatialFirstGroupId(root: WorkbenchNode): string {
  let cursor = root
  while (cursor.kind === 'split') cursor = cursor.first
  return cursor.id
}

export function computeNormalizedGroupRects(
  node: WorkbenchNode,
  box = { x: 0, y: 0, w: 1, h: 1 },
  out: WorkbenchNormalizedRect[] = [],
): WorkbenchNormalizedRect[] {
  if (node.kind === 'group') {
    out.push({ groupId: node.id, ...box })
    return out
  }
  const r = clampRatio(node.ratio)
  if (node.axis === 'row') {
    computeNormalizedGroupRects(node.first, { x: box.x, y: box.y, w: box.w * r, h: box.h }, out)
    computeNormalizedGroupRects(node.second, { x: box.x + box.w * r, y: box.y, w: box.w * (1 - r), h: box.h }, out)
  } else {
    computeNormalizedGroupRects(node.first, { x: box.x, y: box.y, w: box.w, h: box.h * r }, out)
    computeNormalizedGroupRects(node.second, { x: box.x, y: box.y + box.h * r, w: box.w, h: box.h * (1 - r) }, out)
  }
  return out
}

export function findAdjacentGroupId(
  root: WorkbenchNode,
  groupId: string,
  direction: WorkbenchAdjacentDirection,
): string | null {
  const rects = computeNormalizedGroupRects(root)
  const source = rects.find(r => r.groupId === groupId)
  if (!source) return null
  const eps = 1e-5
  const candidates = rects.filter((cand) => {
    if (cand.groupId === groupId) return false
    if (direction === 'right') {
      const touches = Math.abs(cand.x - (source.x + source.w)) < eps
      const overlapY = Math.min(cand.y + cand.h, source.y + source.h) - Math.max(cand.y, source.y)
      return touches && overlapY > eps
    }
    if (direction === 'left') {
      const touches = Math.abs((cand.x + cand.w) - source.x) < eps
      const overlapY = Math.min(cand.y + cand.h, source.y + source.h) - Math.max(cand.y, source.y)
      return touches && overlapY > eps
    }
    if (direction === 'below') {
      const touches = Math.abs(cand.y - (source.y + source.h)) < eps
      const overlapX = Math.min(cand.x + cand.w, source.x + source.w) - Math.max(cand.x, source.x)
      return touches && overlapX > eps
    }
    const touches = Math.abs((cand.y + cand.h) - source.y) < eps
    const overlapX = Math.min(cand.x + cand.w, source.x + source.w) - Math.max(cand.x, source.x)
    return touches && overlapX > eps
  })
  if (candidates.length === 0) return null
  candidates.sort((a, b) => (direction === 'left' || direction === 'right' ? a.y - b.y : a.x - b.x))
  return candidates[0]!.groupId
}

export function clampRatio(ratio: number): number {
  if (!Number.isFinite(ratio)) return 0.5
  return Math.min(WORKBENCH_MAX_RATIO, Math.max(WORKBENCH_MIN_RATIO, ratio))
}

export function clampSplitRatioForSize(
  ratio: number,
  axis: 'row' | 'col',
  containerPx: number,
): number {
  const minPx = axis === 'row' ? WORKBENCH_MIN_COL_PX : WORKBENCH_MIN_ROW_PX
  if (!Number.isFinite(containerPx) || containerPx <= minPx * 2) {
    return containerPx > 0 ? 0.5 : clampRatio(ratio)
  }
  const minRatio = Math.max(WORKBENCH_MIN_RATIO, minPx / containerPx)
  const maxRatio = Math.min(WORKBENCH_MAX_RATIO, 1 - minRatio)
  return Math.min(maxRatio, Math.max(minRatio, ratio))
}

function normalizeGroupTabs(group: WorkbenchGroupNode, tabs: Record<string, WorkbenchTab>): WorkbenchGroupNode {
  const pinnedIds: string[] = []
  const unpinnedIds: string[] = []
  for (const id of group.tabIds) {
    const tab = tabs[id]
    if (!tab) continue
    if (tab.pinned) pinnedIds.push(id)
    else unpinnedIds.push(id)
  }
  const tabIds = [...pinnedIds, ...unpinnedIds]
  const previewTabId = group.previewTabId && unpinnedIds.includes(group.previewTabId)
    ? group.previewTabId
    : null
  const activeTabId = tabIds.length === 0
    ? null
    : (group.activeTabId && tabIds.includes(group.activeTabId) ? group.activeTabId : tabIds[0]!)
  return {
    ...group,
    tabIds,
    activeTabId,
    previewTabId,
  }
}

function mapGroup(
  node: WorkbenchNode,
  groupId: string,
  fn: (group: WorkbenchGroupNode) => WorkbenchNode,
): WorkbenchNode {
  if (node.kind === 'group') return node.id === groupId ? fn(node) : node
  const first = mapGroup(node.first, groupId, fn)
  const second = mapGroup(node.second, groupId, fn)
  if (first === node.first && second === node.second) return node
  return { ...node, first, second }
}

function pruneEmptyGroups(node: WorkbenchNode): WorkbenchNode | null {
  if (node.kind === 'group') {
    return node.tabIds.length === 0 ? null : node
  }
  const first = pruneEmptyGroups(node.first)
  const second = pruneEmptyGroups(node.second)
  if (!first && !second) return null
  if (!first) return second
  if (!second) return first
  if (first === node.first && second === node.second) return node
  return { ...node, first, second }
}

function resolveTargetGroup(state: WorkbenchState, groupId?: string): WorkbenchGroupNode {
  return findGroup(state.root, groupId)
    ?? findGroup(state.root, state.activeGroupId)
    ?? listGroups(state.root)[0]!
}

export function setSplitRatio(state: WorkbenchState, splitId: string, ratio: number): WorkbenchState {
  function walk(node: WorkbenchNode): WorkbenchNode {
    if (node.kind === 'group') return node
    if (node.id === splitId) return { ...node, ratio: clampRatio(ratio) }
    const first = walk(node.first)
    const second = walk(node.second)
    return first === node.first && second === node.second ? node : { ...node, first, second }
  }
  const root = walk(state.root)
  return root === state.root ? state : { ...state, root }
}

export function activateTab(state: WorkbenchState, tabId: string): WorkbenchState {
  const group = findGroupOfTab(state.root, tabId)
  if (!group) return state
  if (state.activeGroupId === group.id && group.activeTabId === tabId) return state
  const root = mapGroup(state.root, group.id, g => ({ ...g, activeTabId: tabId }))
  return { ...state, root, activeGroupId: group.id }
}

export function activateGroup(state: WorkbenchState, groupId: string): WorkbenchState {
  const group = findGroup(state.root, groupId)
  if (!group || state.activeGroupId === groupId) return state
  return { ...state, activeGroupId: groupId }
}

export function updateTabTarget(
  state: WorkbenchState,
  tabId: string,
  updater: (target: WorkbenchTabTarget) => WorkbenchTabTarget,
): WorkbenchState {
  const existing = state.tabs[tabId]
  if (!existing) return state
  const nextTarget = updater(existing.target)
  if (nextTarget === existing.target) return state
  return {
    ...state,
    tabs: {
      ...state.tabs,
      [tabId]: { ...existing, target: nextTarget },
    },
  }
}

export function keepOpenTab(state: WorkbenchState, tabId: string): WorkbenchState {
  const group = findGroupOfTab(state.root, tabId)
  if (!group || group.previewTabId !== tabId) return state
  return {
    ...state,
    root: mapGroup(state.root, group.id, g => ({ ...g, previewTabId: null })),
  }
}

export function markPreviewTab(state: WorkbenchState, tabId: string): WorkbenchState {
  const tab = state.tabs[tabId]
  const group = findGroupOfTab(state.root, tabId)
  if (!tab || tab.pinned || !group || group.previewTabId === tabId) return state
  return {
    ...state,
    root: mapGroup(state.root, group.id, g => ({ ...g, previewTabId: tabId })),
  }
}

export function setTabPinned(state: WorkbenchState, tabId: string, pinned: boolean): WorkbenchState {
  const tab = state.tabs[tabId]
  const group = findGroupOfTab(state.root, tabId)
  if (!tab || !group || tab.pinned === pinned) return state
  const nextTabs: Record<string, WorkbenchTab> = {
    ...state.tabs,
    [tabId]: { ...tab, pinned },
  }
  const root = mapGroup(state.root, group.id, g => normalizeGroupTabs({
    ...g,
    previewTabId: pinned && g.previewTabId === tabId ? null : g.previewTabId,
  }, nextTabs))
  return { ...state, root, tabs: nextTabs }
}

export interface InsertTabOptions {
  groupId?: string
  preview?: boolean
  activate?: boolean
  /** Optional predicate allowing caller to protect dirty tabs from preview replacement. */
  canReplacePreview?: (tabId: string) => boolean
}

export interface InsertTabResult {
  state: WorkbenchState
  replacedTabId: string | null
}

export function insertTab(
  state: WorkbenchState,
  tab: WorkbenchTab,
  options: InsertTabOptions = {},
): InsertTabResult {
  const activate = options.activate !== false
  const preview = Boolean(options.preview) && !tab.pinned
  const existingGroup = findGroupOfTab(state.root, tab.id)
  if (existingGroup) {
    const nextTabs = { ...state.tabs, [tab.id]: tab }
    const nextState: WorkbenchState = {
      ...state,
      tabs: nextTabs,
      root: mapGroup(state.root, existingGroup.id, g => normalizeGroupTabs({
        ...g,
        activeTabId: activate ? tab.id : g.activeTabId,
        previewTabId: !preview && g.previewTabId === tab.id ? null : g.previewTabId,
      }, nextTabs)),
      activeGroupId: activate ? existingGroup.id : state.activeGroupId,
    }
    return { state: nextState, replacedTabId: null }
  }

  const targetGroup = resolveTargetGroup(state, options.groupId)
  const canReplace = options.canReplacePreview ?? (() => true)
  const replaceableId = preview
    && targetGroup.previewTabId
    && targetGroup.previewTabId !== tab.id
    && canReplace(targetGroup.previewTabId)
    ? targetGroup.previewTabId
    : null

  const nextTabs: Record<string, WorkbenchTab> = { ...state.tabs, [tab.id]: tab }
  if (replaceableId) {
    delete nextTabs[replaceableId]
  }

  const root = mapGroup(state.root, targetGroup.id, (g) => {
    let nextTabIds: string[]
    if (replaceableId) {
      const idx = g.tabIds.indexOf(replaceableId)
      nextTabIds = idx >= 0
        ? [...g.tabIds.slice(0, idx), tab.id, ...g.tabIds.slice(idx + 1)]
        : [...g.tabIds, tab.id]
    } else {
      nextTabIds = [...g.tabIds, tab.id]
    }
    return normalizeGroupTabs({
      ...g,
      tabIds: nextTabIds,
      activeTabId: activate || !g.activeTabId || g.activeTabId === replaceableId ? tab.id : g.activeTabId,
      previewTabId: preview ? tab.id : g.previewTabId,
    }, nextTabs)
  })

  return {
    state: {
      ...state,
      tabs: nextTabs,
      root,
      activeGroupId: activate ? targetGroup.id : state.activeGroupId,
    },
    replacedTabId: replaceableId,
  }
}

export function splitGroupWithTab(
  state: WorkbenchState,
  anchorGroupId: string | undefined,
  direction: WorkbenchSplitDirection,
  tab: WorkbenchTab,
  options: { preview?: boolean, activate?: boolean } = {},
): WorkbenchState {
  const anchor = resolveTargetGroup(state, anchorGroupId)
  // Splitting an empty root group simply populates that group directly instead of creating a dead empty pane.
  if (anchor.tabIds.length === 0) {
    return insertTab(state, tab, { groupId: anchor.id, preview: options.preview, activate: options.activate }).state
  }
  const existingIds = collectNodeIds(state.root)
  const newGroupId = nextInternalId('group', existingIds)
  existingIds.add(newGroupId)
  const splitId = nextInternalId('split', existingIds)
  const activate = options.activate !== false
  const preview = Boolean(options.preview) && !tab.pinned

  const nextTabs: Record<string, WorkbenchTab> = { ...state.tabs, [tab.id]: tab }
  if (state.tabs[tab.id]) {
    // If tab already lived in a group, detach it first before splitting.
    const detached = detachTabFromTree(state, tab.id)
    return splitGroupWithTab(detached, anchorGroupId, direction, tab, options)
  }

  const newGroup: WorkbenchGroupNode = {
    kind: 'group',
    id: newGroupId,
    tabIds: [tab.id],
    activeTabId: tab.id,
    previewTabId: preview ? tab.id : null,
  }

  const axis: 'row' | 'col' = direction === 'left' || direction === 'right' ? 'row' : 'col'
  const newFirst = direction === 'left' || direction === 'top'
  const root = mapGroup(state.root, anchor.id, g => ({
    kind: 'split',
    id: splitId,
    axis,
    ratio: 0.5,
    first: newFirst ? newGroup : g,
    second: newFirst ? g : newGroup,
  }))

  return {
    ...state,
    tabs: nextTabs,
    root,
    activeGroupId: activate ? newGroupId : state.activeGroupId,
  }
}

export function splitRootWithTab(
  state: WorkbenchState,
  direction: WorkbenchSplitDirection,
  tab: WorkbenchTab,
  options: { preview?: boolean, activate?: boolean } = {},
): WorkbenchState {
  if (state.root.kind === 'group' && state.root.tabIds.length === 0) {
    return insertTab(state, tab, {
      groupId: state.root.id,
      preview: options.preview,
      activate: options.activate,
    }).state
  }
  if (state.tabs[tab.id]) {
    const detached = detachTabFromTree(state, tab.id)
    return splitRootWithTab(detached, direction, tab, options)
  }

  const existingIds = collectNodeIds(state.root)
  const newGroupId = nextInternalId('group', existingIds)
  existingIds.add(newGroupId)
  const splitId = nextInternalId('split', existingIds)
  const activate = options.activate !== false
  const preview = Boolean(options.preview) && !tab.pinned

  const newGroup: WorkbenchGroupNode = {
    kind: 'group',
    id: newGroupId,
    tabIds: [tab.id],
    activeTabId: tab.id,
    previewTabId: preview ? tab.id : null,
  }

  const axis: 'row' | 'col' = direction === 'left' || direction === 'right' ? 'row' : 'col'
  const newFirst = direction === 'left' || direction === 'top'
  const ratio = direction === 'bottom' ? 0.62 : direction === 'top' ? 0.38 : 0.5
  const nextTabs: Record<string, WorkbenchTab> = { ...state.tabs, [tab.id]: tab }

  return {
    ...state,
    tabs: nextTabs,
    root: {
      kind: 'split',
      id: splitId,
      axis,
      ratio,
      first: newFirst ? newGroup : state.root,
      second: newFirst ? state.root : newGroup,
    },
    activeGroupId: activate ? newGroupId : state.activeGroupId,
  }
}

function detachTabFromTree(state: WorkbenchState, tabId: string): WorkbenchState {
  const sourceGroup = findGroupOfTab(state.root, tabId)
  if (!sourceGroup) return state
  const closedIdx = sourceGroup.tabIds.indexOf(tabId)
  const remaining = sourceGroup.tabIds.filter(id => id !== tabId)
  const nextActiveInSource = remaining.length === 0
    ? null
    : (sourceGroup.activeTabId === tabId
        ? remaining[Math.min(closedIdx, remaining.length - 1)]!
        : sourceGroup.activeTabId)

  const nextTabs = { ...state.tabs }
  delete nextTabs[tabId]

  const updatedRoot = mapGroup(state.root, sourceGroup.id, g => ({
    ...g,
    tabIds: remaining,
    activeTabId: nextActiveInSource,
    previewTabId: g.previewTabId === tabId ? null : g.previewTabId,
  }))

  const pruned = pruneEmptyGroups(updatedRoot)
  const root: WorkbenchNode = pruned ?? {
    kind: 'group',
    id: sourceGroup.id,
    tabIds: [],
    activeTabId: null,
    previewTabId: null,
  }
  const activeGroupStillExists = Boolean(findGroup(root, state.activeGroupId))
  const activeGroupId = activeGroupStillExists ? state.activeGroupId : listGroups(root)[0]!.id
  return {
    ...state,
    tabs: nextTabs,
    root,
    activeGroupId,
  }
}

export function removeTab(state: WorkbenchState, tabId: string): WorkbenchState {
  if (!state.tabs[tabId]) return state
  return detachTabFromTree(state, tabId)
}

export function reorderTabInGroup(
  state: WorkbenchState,
  groupId: string,
  tabId: string,
  targetIndex: number,
): WorkbenchState {
  const group = findGroup(state.root, groupId)
  const tab = state.tabs[tabId]
  if (!group || !tab) return state
  const currentIdx = group.tabIds.indexOf(tabId)
  if (currentIdx < 0) return state
  const pinnedCount = group.tabIds.filter(id => state.tabs[id]?.pinned).length
  const minIdx = tab.pinned ? 0 : pinnedCount
  const maxIdx = tab.pinned ? Math.max(0, pinnedCount - 1) : group.tabIds.length - 1
  const clamped = Math.min(maxIdx, Math.max(minIdx, targetIndex))
  if (clamped === currentIdx) return state
  const nextIds = [...group.tabIds]
  nextIds.splice(currentIdx, 1)
  nextIds.splice(clamped, 0, tabId)
  return {
    ...state,
    root: mapGroup(state.root, groupId, g => normalizeGroupTabs({ ...g, tabIds: nextIds }, state.tabs)),
  }
}

export function moveTabToGroup(
  state: WorkbenchState,
  tabId: string,
  targetGroupId: string,
  zone: WorkbenchDropZone,
  insertIndex?: number,
): WorkbenchState {
  const tab = state.tabs[tabId]
  const sourceGroup = findGroupOfTab(state.root, tabId)
  if (!tab || !sourceGroup) return state

  if (targetGroupId === WORKBENCH_ROOT_DROP_ID && zone !== 'center') {
    if (state.root.kind === 'group' && sourceGroup.tabIds.length <= 1) {
      return activateTab(state, tabId)
    }
    const wasPreview = sourceGroup.previewTabId === tabId
    const detached = detachTabFromTree(state, tabId)
    return splitRootWithTab(detached, zone, tab, {
      preview: wasPreview && !tab.pinned,
      activate: true,
    })
  }

  const targetGroup = findGroup(state.root, targetGroupId)
  if (!targetGroup) return state

  if (sourceGroup.id === targetGroup.id) {
    if (zone === 'center') {
      const next = typeof insertIndex === 'number'
        ? reorderTabInGroup(state, sourceGroup.id, tabId, insertIndex)
        : state
      return activateTab(next, tabId)
    }
    // Single-tab group cannot split from itself into an empty source.
    if (sourceGroup.tabIds.length <= 1) {
      return activateTab(state, tabId)
    }
  }

  const wasPreview = sourceGroup.previewTabId === tabId
  const detached = detachTabFromTree(state, tabId)

  if (zone !== 'center') {
    const survivingAnchor = findGroup(detached.root, targetGroupId) ?? listGroups(detached.root)[0]!
    return splitGroupWithTab(detached, survivingAnchor.id, zone, tab, {
      preview: wasPreview && !tab.pinned,
      activate: true,
    })
  }

  const dest = findGroup(detached.root, targetGroupId) ?? listGroups(detached.root)[0]!
  const nextTabs = { ...detached.tabs, [tabId]: tab }
  const root = mapGroup(detached.root, dest.id, (g) => {
    const rawIds = [...g.tabIds]
    const at = typeof insertIndex === 'number'
      ? Math.min(rawIds.length, Math.max(0, insertIndex))
      : rawIds.length
    rawIds.splice(at, 0, tabId)
    // Preserve at most one preview tab in destination: only carry preview status if destination has none.
    const nextPreview = !tab.pinned && wasPreview && !g.previewTabId ? tabId : g.previewTabId
    return normalizeGroupTabs({
      ...g,
      tabIds: rawIds,
      activeTabId: tabId,
      previewTabId: nextPreview,
    }, nextTabs)
  })
  return {
    ...detached,
    tabs: nextTabs,
    root,
    activeGroupId: dest.id,
  }
}

export function validateWorkbenchState(state: WorkbenchState): { ok: true } | { ok: false, reason: string } {
  if (!state || state.version !== 1) return { ok: false, reason: 'invalid version' }
  const nodeIds = new Set<string>()
  const seenTabs = new Set<string>()
  const groups: WorkbenchGroupNode[] = []

  function walk(node: WorkbenchNode, isRoot: boolean): string | null {
    if (!node || typeof node !== 'object' || !node.id) return 'missing node id'
    if (nodeIds.has(node.id)) return `duplicate node id: ${node.id}`
    nodeIds.add(node.id)

    if (node.kind === 'split') {
      if (node.axis !== 'row' && node.axis !== 'col') return `invalid split axis: ${String(node.axis)}`
      if (!Number.isFinite(node.ratio) || node.ratio <= 0 || node.ratio >= 1) {
        return `invalid split ratio: ${node.ratio}`
      }
      return walk(node.first, false) ?? walk(node.second, false)
    }

    if (node.kind !== 'group') return 'unknown node kind'
    groups.push(node)
    if (node.tabIds.length === 0) {
      if (!isRoot) return `non-root empty group: ${node.id}`
      if (node.activeTabId !== null) return `empty group has activeTabId: ${node.id}`
      if (node.previewTabId !== null) return `empty group has previewTabId: ${node.id}`
      return null
    }
    let seenUnpinned = false
    for (const tid of node.tabIds) {
      if (seenTabs.has(tid)) return `duplicate tabId in tree: ${tid}`
      seenTabs.add(tid)
      const tab = state.tabs[tid]
      if (!tab || tab.id !== tid) return `missing or mismatched tab record: ${tid}`
      if (tab.pinned) {
        if (seenUnpinned) return `pinned tab after unpinned tab in group ${node.id}`
      } else {
        seenUnpinned = true
      }
    }
    if (!node.activeTabId || !node.tabIds.includes(node.activeTabId)) {
      return `invalid activeTabId in group ${node.id}`
    }
    if (node.previewTabId !== null) {
      if (!node.tabIds.includes(node.previewTabId)) {
        return `previewTabId not in group ${node.id}`
      }
      if (state.tabs[node.previewTabId]?.pinned) {
        return `pinned tab marked as preview in group ${node.id}`
      }
    }
    return null
  }

  const err = walk(state.root, true)
  if (err) return { ok: false, reason: err }
  for (const tid of Object.keys(state.tabs)) {
    if (!seenTabs.has(tid)) return { ok: false, reason: `orphan tab in tabs map: ${tid}` }
  }
  if (!groups.some(g => g.id === state.activeGroupId)) {
    return { ok: false, reason: `activeGroupId not found: ${state.activeGroupId}` }
  }
  return { ok: true }
}

function isValidTarget(raw: unknown): raw is WorkbenchTabTarget {
  if (!raw || typeof raw !== 'object') return false
  const t = raw as Record<string, unknown>
  switch (t.kind) {
    case 'chat':
      return (t.sessionId === null || typeof t.sessionId === 'string')
        && typeof t.explicitSelection === 'boolean'
    case 'file':
    case 'preview':
      return typeof t.filePath === 'string' && t.filePath.trim().length > 0
    case 'asset':
      return typeof t.assetKey === 'string' && t.assetKey.trim().length > 0 && typeof t.name === 'string'
    case 'terminal':
      return typeof t.instance === 'number' && Number.isInteger(t.instance) && t.instance > 0
    case 'browser':
      return typeof t.instance === 'number' && Number.isInteger(t.instance) && t.instance > 0 && typeof t.address === 'string'
    case 'display':
      return true
    case 'schedule':
      return t.scheduleId === undefined || typeof t.scheduleId === 'string'
    default:
      return false
  }
}

/** Boundary parser for persisted JSON: normalizes or rejects invalid snapshots cleanly. */
export function parseWorkbenchState(raw: unknown): WorkbenchState | null {
  if (!raw || typeof raw !== 'object') return null
  const candidate = raw as Partial<WorkbenchState>
  if (candidate.version !== 1 || !candidate.root || !candidate.tabs || typeof candidate.tabs !== 'object') {
    return null
  }
  const tabs: Record<string, WorkbenchTab> = {}
  for (const [id, entry] of Object.entries(candidate.tabs)) {
    if (!entry || typeof entry !== 'object') continue
    const e = entry as Partial<WorkbenchTab>
    if (typeof e.id !== 'string' || e.id !== id || !isValidTarget(e.target)) continue
    tabs[id] = {
      id,
      target: e.target,
      pinned: Boolean(e.pinned),
    }
  }

  function sanitizeNode(node: unknown): WorkbenchNode | null {
    if (!node || typeof node !== 'object') return null
    const n = node as Record<string, unknown>
    if (typeof n.id !== 'string' || !n.id.trim()) return null
    if (n.kind === 'group') {
      if (!Array.isArray(n.tabIds)) return null
      return normalizeGroupTabs({
        kind: 'group',
        id: n.id,
        tabIds: n.tabIds.filter((tid: unknown): tid is string => typeof tid === 'string' && Boolean(tabs[tid])),
        activeTabId: typeof n.activeTabId === 'string' ? n.activeTabId : null,
        previewTabId: typeof n.previewTabId === 'string' ? n.previewTabId : null,
      }, tabs)
    }
    if (n.kind === 'split') {
      if (n.axis !== 'row' && n.axis !== 'col') return null
      const first = sanitizeNode(n.first)
      const second = sanitizeNode(n.second)
      if (!first && !second) return null
      if (!first) return second
      if (!second) return first
      return {
        kind: 'split',
        id: n.id,
        axis: n.axis,
        ratio: clampRatio(typeof n.ratio === 'number' ? n.ratio : 0.5),
        first,
        second,
      }
    }
    return null
  }

  const parsedRoot = sanitizeNode(candidate.root)
  if (!parsedRoot) return null
  const pruned = pruneEmptyGroups(parsedRoot) ?? createEmptyWorkbenchState().root
  // Drop any tabs not referenced in the surviving tree.
  const referenced = new Set<string>()
  for (const g of listGroups(pruned)) {
    for (const tid of g.tabIds) referenced.add(tid)
  }
  for (const tid of Object.keys(tabs)) {
    if (!referenced.has(tid)) delete tabs[tid]
  }
  const activeGroupId = typeof candidate.activeGroupId === 'string' && findGroup(pruned, candidate.activeGroupId)
    ? candidate.activeGroupId
    : listGroups(pruned)[0]!.id
  const built: WorkbenchState = {
    version: 1,
    root: pruned,
    activeGroupId,
    tabs,
  }
  return validateWorkbenchState(built).ok ? built : null
}
