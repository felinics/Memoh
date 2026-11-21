import type {
  WorkbenchDropZone,
  WorkbenchGroupNode,
  WorkbenchNode,
  WorkbenchSplitDirection,
  WorkbenchSplitNode,
  WorkbenchState,
  WorkbenchTab,
  WorkbenchTabTarget,
} from './types'

export const WORKBENCH_MIN_COL_PX = 220
export const WORKBENCH_MIN_ROW_PX = 140
export const WORKBENCH_ROOT_DROP_ID = '__root__'
const WORKBENCH_MIN_RATIO = 0.1
const WORKBENCH_MAX_RATIO = 0.9

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
    root: { kind: 'group', id: groupId, tabIds: [], activeTabId: null, previewTabId: null },
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

/** Top-most group directly to the right of the spatial-first group, or null when no horizontal split exists above it. */
export function findRightNeighborOfSpatialFirst(root: WorkbenchNode): string | null {
  let cursor = root
  let deepestRowSplit: WorkbenchSplitNode | null = null
  while (cursor.kind === 'split') {
    if (cursor.axis === 'row') deepestRowSplit = cursor
    cursor = cursor.first
  }
  return deepestRowSplit ? findSpatialFirstGroupId(deepestRowSplit.second) : null
}

/** Whether `groupId` touches the top edge of the workbench (eligible for Electron window dragging). */
export function isTopEdgeGroup(node: WorkbenchNode, groupId: string): boolean {
  if (node.kind === 'group') return node.id === groupId
  if (node.axis === 'col') return isTopEdgeGroup(node.first, groupId)
  return isTopEdgeGroup(node.first, groupId) || isTopEdgeGroup(node.second, groupId)
}

export function clampRatio(ratio: number): number {
  if (!Number.isFinite(ratio)) return 0.5
  return Math.min(WORKBENCH_MAX_RATIO, Math.max(WORKBENCH_MIN_RATIO, ratio))
}

export function clampSplitRatioForSize(ratio: number, axis: 'row' | 'col', containerPx: number): number {
  const minPx = axis === 'row' ? WORKBENCH_MIN_COL_PX : WORKBENCH_MIN_ROW_PX
  if (!Number.isFinite(containerPx) || containerPx <= minPx * 2) {
    return containerPx > 0 ? 0.5 : clampRatio(ratio)
  }
  const minRatio = Math.max(WORKBENCH_MIN_RATIO, minPx / containerPx)
  return Math.min(1 - minRatio, Math.max(minRatio, Number.isFinite(ratio) ? ratio : 0.5))
}

/** Enforce single-array prefix invariant: all pinned tabs precede unpinned tabs while keeping relative order. */
function normalizeGroupTabs(group: WorkbenchGroupNode, tabs: Record<string, WorkbenchTab>): WorkbenchGroupNode {
  const seen = new Set<string>()
  const pinned: string[] = []
  const regular: string[] = []
  for (const id of group.tabIds) {
    const tab = tabs[id]
    if (!tab || seen.has(id)) continue
    seen.add(id)
    if (tab.pinned) pinned.push(id)
    else regular.push(id)
  }
  const tabIds = [...pinned, ...regular]
  const activeTabId = group.activeTabId && seen.has(group.activeTabId) ? group.activeTabId : (tabIds[0] ?? null)
  const previewTabId = group.previewTabId && seen.has(group.previewTabId) && !tabs[group.previewTabId]?.pinned
    ? group.previewTabId
    : null
  return { ...group, tabIds, activeTabId, previewTabId }
}

function mapGroup(node: WorkbenchNode, groupId: string, fn: (group: WorkbenchGroupNode) => WorkbenchNode): WorkbenchNode {
  if (node.kind === 'group') return node.id === groupId ? fn(node) : node
  const first = mapGroup(node.first, groupId, fn)
  const second = mapGroup(node.second, groupId, fn)
  return first === node.first && second === node.second ? node : { ...node, first, second }
}

function pruneEmptyGroups(node: WorkbenchNode): WorkbenchNode | null {
  if (node.kind === 'group') return node.tabIds.length > 0 ? node : null
  const first = pruneEmptyGroups(node.first)
  const second = pruneEmptyGroups(node.second)
  if (!first && !second) return null
  if (!first) return second
  if (!second) return first
  return first === node.first && second === node.second ? node : { ...node, first, second }
}

function resolveTargetGroup(state: WorkbenchState, groupId?: string | null): WorkbenchGroupNode {
  return findGroup(state.root, groupId)
    ?? findGroup(state.root, state.activeGroupId)
    ?? listGroups(state.root)[0]!
}

export function setSplitRatio(state: WorkbenchState, splitId: string, ratio: number): WorkbenchState {
  const clamped = clampRatio(ratio)
  function visit(node: WorkbenchNode): WorkbenchNode {
    if (node.kind === 'group') return node
    if (node.id === splitId) return node.ratio === clamped ? node : { ...node, ratio: clamped }
    const first = visit(node.first)
    const second = visit(node.second)
    return first === node.first && second === node.second ? node : { ...node, first, second }
  }
  const root = visit(state.root)
  return root === state.root ? state : { ...state, root }
}

export function activateTab(state: WorkbenchState, tabId: string): WorkbenchState {
  const group = findGroupOfTab(state.root, tabId)
  if (!group) return state
  if (group.activeTabId === tabId && state.activeGroupId === group.id) return state
  return { ...state, root: mapGroup(state.root, group.id, g => ({ ...g, activeTabId: tabId })), activeGroupId: group.id }
}

export function activateGroup(state: WorkbenchState, groupId: string): WorkbenchState {
  const group = findGroup(state.root, groupId)
  if (!group || state.activeGroupId === group.id) return state
  return { ...state, activeGroupId: group.id }
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
  return { ...state, tabs: { ...state.tabs, [tabId]: { ...existing, target: nextTarget } } }
}

export function keepOpenTab(state: WorkbenchState, tabId: string): WorkbenchState {
  const group = findGroupOfTab(state.root, tabId)
  if (!group || group.previewTabId !== tabId) return state
  return { ...state, root: mapGroup(state.root, group.id, g => ({ ...g, previewTabId: null })) }
}

export function markPreviewTab(state: WorkbenchState, tabId: string): WorkbenchState {
  const group = findGroupOfTab(state.root, tabId)
  const tab = state.tabs[tabId]
  if (!group || !tab || tab.pinned || group.previewTabId === tabId) return state
  return { ...state, root: mapGroup(state.root, group.id, g => ({ ...g, previewTabId: tabId })) }
}

export function setTabPinned(state: WorkbenchState, tabId: string, pinned: boolean): WorkbenchState {
  const existing = state.tabs[tabId]
  const group = findGroupOfTab(state.root, tabId)
  if (!existing || !group || existing.pinned === pinned) return state
  const nextTabs: Record<string, WorkbenchTab> = { ...state.tabs, [tabId]: { ...existing, pinned } }
  const root = mapGroup(state.root, group.id, (g) => {
    const without = g.tabIds.filter(id => id !== tabId)
    const pinnedCount = without.filter(id => nextTabs[id]?.pinned).length
    // Pinning appends to the pinned prefix; unpinning prepends to the regular segment — both land at `pinnedCount`.
    const nextTabIds = [...without.slice(0, pinnedCount), tabId, ...without.slice(pinnedCount)]
    return normalizeGroupTabs({
      ...g,
      tabIds: nextTabIds,
      previewTabId: pinned && g.previewTabId === tabId ? null : g.previewTabId,
    }, nextTabs)
  })
  return { ...state, tabs: nextTabs, root }
}

export interface InsertTabOptions {
  groupId?: string
  preview?: boolean
  activate?: boolean
  canReplacePreview?: (tabId: string) => boolean
}

export function insertTab(state: WorkbenchState, tab: WorkbenchTab, options: InsertTabOptions = {}): WorkbenchState {
  const activate = options.activate !== false
  const preview = Boolean(options.preview) && !tab.pinned
  const existingGroup = findGroupOfTab(state.root, tab.id)
  if (existingGroup) {
    const nextTabs = { ...state.tabs, [tab.id]: tab }
    return {
      ...state,
      tabs: nextTabs,
      root: mapGroup(state.root, existingGroup.id, g => normalizeGroupTabs({
        ...g,
        activeTabId: activate ? tab.id : g.activeTabId,
        previewTabId: !preview && g.previewTabId === tab.id ? null : g.previewTabId,
      }, nextTabs)),
      activeGroupId: activate ? existingGroup.id : state.activeGroupId,
    }
  }

  const targetGroup = resolveTargetGroup(state, options.groupId)
  const canReplace = options.canReplacePreview ?? (() => true)
  const replaceableId = preview && targetGroup.previewTabId && targetGroup.previewTabId !== tab.id && canReplace(targetGroup.previewTabId)
    ? targetGroup.previewTabId
    : null

  const nextTabs: Record<string, WorkbenchTab> = { ...state.tabs, [tab.id]: tab }
  if (replaceableId) delete nextTabs[replaceableId]

  const root = mapGroup(state.root, targetGroup.id, (g) => {
    const idx = replaceableId ? g.tabIds.indexOf(replaceableId) : -1
    const nextTabIds = idx >= 0
      ? [...g.tabIds.slice(0, idx), tab.id, ...g.tabIds.slice(idx + 1)]
      : [...g.tabIds, tab.id]
    return normalizeGroupTabs({
      ...g,
      tabIds: nextTabIds,
      activeTabId: activate || !g.activeTabId || g.activeTabId === replaceableId ? tab.id : g.activeTabId,
      previewTabId: preview ? tab.id : g.previewTabId,
    }, nextTabs)
  })

  return { ...state, tabs: nextTabs, root, activeGroupId: activate ? targetGroup.id : state.activeGroupId }
}

/**
 * Split a leaf group — or the entire root when `anchorGroupId === WORKBENCH_ROOT_DROP_ID` —
 * inserting `tab` into the newly created pane.
 */
export function splitGroupWithTab(
  state: WorkbenchState,
  anchorGroupId: string | undefined,
  direction: WorkbenchSplitDirection,
  tab: WorkbenchTab,
  options: { preview?: boolean, activate?: boolean } = {},
): WorkbenchState {
  const base = state.tabs[tab.id] ? removeTab(state, tab.id) : state
  const splitRoot = anchorGroupId === WORKBENCH_ROOT_DROP_ID

  if (splitRoot) {
    if (base.root.kind === 'group' && base.root.tabIds.length === 0) {
      return insertTab(base, tab, { groupId: base.root.id, preview: options.preview, activate: options.activate })
    }
  } else {
    const anchor = resolveTargetGroup(base, anchorGroupId)
    // Splitting an empty root group populates that group directly instead of creating a dead empty pane.
    if (anchor.tabIds.length === 0) {
      return insertTab(base, tab, { groupId: anchor.id, preview: options.preview, activate: options.activate })
    }
    anchorGroupId = anchor.id
  }

  const existingIds = collectNodeIds(base.root)
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
  const ratio = splitRoot && direction === 'bottom' ? 0.62 : 0.5
  const wrap = (node: WorkbenchNode): WorkbenchNode => ({
    kind: 'split',
    id: splitId,
    axis,
    ratio,
    first: newFirst ? newGroup : node,
    second: newFirst ? node : newGroup,
  })

  return {
    ...base,
    tabs: { ...base.tabs, [tab.id]: tab },
    root: splitRoot ? wrap(base.root) : mapGroup(base.root, anchorGroupId!, wrap),
    activeGroupId: activate ? newGroupId : base.activeGroupId,
  }
}

export function removeTab(state: WorkbenchState, tabId: string): WorkbenchState {
  if (!state.tabs[tabId]) return state
  const sourceGroup = findGroupOfTab(state.root, tabId)
  if (!sourceGroup) return state
  const closedIdx = sourceGroup.tabIds.indexOf(tabId)
  const remaining = sourceGroup.tabIds.filter(id => id !== tabId)
  const nextActiveInSource = remaining.length === 0
    ? null
    : (sourceGroup.activeTabId === tabId ? remaining[Math.min(closedIdx, remaining.length - 1)]! : sourceGroup.activeTabId)

  const nextTabs = { ...state.tabs }
  delete nextTabs[tabId]

  const updatedRoot = mapGroup(state.root, sourceGroup.id, g => ({
    ...g,
    tabIds: remaining,
    activeTabId: nextActiveInSource,
    previewTabId: g.previewTabId === tabId ? null : g.previewTabId,
  }))
  const root: WorkbenchNode = pruneEmptyGroups(updatedRoot) ?? {
    kind: 'group',
    id: sourceGroup.id,
    tabIds: [],
    activeTabId: null,
    previewTabId: null,
  }
  const activeGroupId = findGroup(root, state.activeGroupId) ? state.activeGroupId : listGroups(root)[0]!.id
  return { ...state, tabs: nextTabs, root, activeGroupId }
}

export function reorderTabInGroup(state: WorkbenchState, groupId: string, tabId: string, targetIndex: number): WorkbenchState {
  const group = findGroup(state.root, groupId)
  const tab = state.tabs[tabId]
  if (!group || !tab) return state
  const currentIdx = group.tabIds.indexOf(tabId)
  if (currentIdx < 0) return state

  const pinnedIds = group.tabIds.filter(id => state.tabs[id]?.pinned)
  const regularIds = group.tabIds.filter(id => !state.tabs[id]?.pinned)
  const segment = tab.pinned ? [...pinnedIds] : [...regularIds]
  const offset = tab.pinned ? 0 : pinnedIds.length
  const segFrom = currentIdx - offset
  const segTo = Math.max(0, Math.min(segment.length - 1, targetIndex - offset))
  if (segFrom === segTo) return state

  segment.splice(segFrom, 1)
  segment.splice(segTo, 0, tabId)
  const nextTabIds = tab.pinned ? [...segment, ...regularIds] : [...pinnedIds, ...segment]
  return { ...state, root: mapGroup(state.root, group.id, g => ({ ...g, tabIds: nextTabIds })) }
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

  if (targetGroupId === WORKBENCH_ROOT_DROP_ID) {
    if (zone === 'center') return state
    if (listGroups(state.root).length === 1 && sourceGroup.tabIds.length === 1) return state
    return splitGroupWithTab(removeTab(state, tabId), WORKBENCH_ROOT_DROP_ID, zone, tab, {
      preview: sourceGroup.previewTabId === tabId && !tab.pinned,
      activate: true,
    })
  }

  const targetGroup = findGroup(state.root, targetGroupId)
  if (!targetGroup) return state
  if (sourceGroup.id === targetGroup.id && sourceGroup.tabIds.length === 1 && zone !== 'center') return state

  if (sourceGroup.id === targetGroup.id && zone === 'center') {
    if (typeof insertIndex === 'number') {
      // Drop slots are measured before detaching the dragged tab, so indices after it shift left by 1.
      const currentIdx = sourceGroup.tabIds.indexOf(tabId)
      const adjusted = currentIdx >= 0 && currentIdx < insertIndex ? insertIndex - 1 : insertIndex
      return reorderTabInGroup(activateTab(state, tabId), sourceGroup.id, tabId, adjusted)
    }
    return activateTab(state, tabId)
  }

  const wasPreview = sourceGroup.previewTabId === tabId && !tab.pinned
  const detached = removeTab(state, tabId)
  if (zone !== 'center') {
    return splitGroupWithTab(detached, targetGroup.id, zone, tab, { preview: wasPreview, activate: true })
  }

  const dest = findGroup(detached.root, targetGroup.id) ?? listGroups(detached.root)[0]!
  const nextTabs = { ...detached.tabs, [tabId]: tab }
  const root = mapGroup(detached.root, dest.id, (g) => {
    const pinnedCount = g.tabIds.filter(id => nextTabs[id]?.pinned).length
    const minPos = tab.pinned ? 0 : pinnedCount
    const maxPos = tab.pinned ? pinnedCount : g.tabIds.length
    const pos = typeof insertIndex === 'number' ? Math.max(minPos, Math.min(maxPos, insertIndex)) : maxPos
    return normalizeGroupTabs({
      ...g,
      tabIds: [...g.tabIds.slice(0, pos), tabId, ...g.tabIds.slice(pos)],
      activeTabId: tabId,
    }, nextTabs)
  })
  return { ...detached, tabs: nextTabs, root, activeGroupId: dest.id }
}

function isValidTarget(raw: unknown): raw is WorkbenchTabTarget {
  if (!raw || typeof raw !== 'object') return false
  const t = raw as Record<string, unknown>
  switch (t.kind) {
    case 'chat':
      return (t.sessionId === null || typeof t.sessionId === 'string') && typeof t.explicitSelection === 'boolean'
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

/** Boundary parser for persisted JSON: normalizes or rejects invalid snapshots cleanly in one pass. */
export function parseWorkbenchState(raw: unknown): WorkbenchState | null {
  if (!raw || typeof raw !== 'object') return null
  const candidate = raw as Partial<WorkbenchState>
  if (candidate.version !== 1 || !candidate.root || !candidate.tabs || typeof candidate.tabs !== 'object') return null

  const tabs: Record<string, WorkbenchTab> = {}
  for (const [id, entry] of Object.entries(candidate.tabs)) {
    if (id === '__proto__' || !entry || typeof entry !== 'object') continue
    const e = entry as Partial<WorkbenchTab>
    if (typeof e.id !== 'string' || e.id !== id || !isValidTarget(e.target)) continue
    tabs[id] = { id, target: e.target, pinned: Boolean(e.pinned) }
  }

  const seenNodeIds = new Set<string>()
  const seenTabIds = new Set<string>()

  function sanitizeNode(node: unknown): WorkbenchNode | null {
    if (!node || typeof node !== 'object') return null
    const n = node as Record<string, unknown>
    if (typeof n.id !== 'string' || !n.id.trim() || n.id === WORKBENCH_ROOT_DROP_ID || seenNodeIds.has(n.id)) return null
    seenNodeIds.add(n.id)
    if (n.kind === 'group') {
      if (!Array.isArray(n.tabIds)) return null
      const uniqueTabIds: string[] = []
      for (const tid of n.tabIds) {
        if (typeof tid === 'string' && Object.hasOwn(tabs, tid) && !seenTabIds.has(tid)) {
          seenTabIds.add(tid)
          uniqueTabIds.push(tid)
        }
      }
      return normalizeGroupTabs({
        kind: 'group',
        id: n.id,
        tabIds: uniqueTabIds,
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
  const root = pruneEmptyGroups(parsedRoot) ?? createEmptyWorkbenchState().root
  for (const tid of Object.keys(tabs)) {
    if (!seenTabIds.has(tid)) delete tabs[tid]
  }
  const activeGroupId = typeof candidate.activeGroupId === 'string' && findGroup(root, candidate.activeGroupId)
    ? candidate.activeGroupId
    : listGroups(root)[0]!.id
  return { version: 1, root, activeGroupId, tabs }
}
