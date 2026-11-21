import { describe, expect, it } from 'vitest'
import {
  WORKBENCH_MIN_COL_PX,
  WORKBENCH_MIN_ROW_PX,
  WORKBENCH_ROOT_DROP_ID,
  activateGroup,
  activateTab,
  clampSplitRatioForSize,
  createEmptyWorkbenchState,
  findGroup,
  findGroupOfTab,
  findRightNeighborOfSpatialFirst,
  findSpatialFirstGroupId,
  insertTab,
  isTopEdgeGroup,
  keepOpenTab,
  listGroups,
  moveTabToGroup,
  parseWorkbenchState,
  removeTab,
  reorderTabInGroup,
  setSplitRatio,
  setTabPinned,
  splitGroupWithTab,
} from './tree'
import type { WorkbenchDropZone, WorkbenchNode, WorkbenchSplitDirection, WorkbenchState, WorkbenchTab } from './types'

function validateWorkbenchState(state: WorkbenchState): { ok: true } | { ok: false, reason: string } {
  const seenNodeIds = new Set<string>()
  const seenTabIds = new Set<string>()
  const groupIds = new Set<string>()

  const walk = (node: WorkbenchNode): string | null => {
    if (seenNodeIds.has(node.id)) return `duplicate node id ${node.id}`
    seenNodeIds.add(node.id)
    if (node.kind === 'split') {
      if (!Number.isFinite(node.ratio) || node.ratio <= 0 || node.ratio >= 1) return `invalid ratio on ${node.id}`
      return walk(node.first) ?? walk(node.second)
    }
    groupIds.add(node.id)
    if (state.root.kind === 'split' && node.tabIds.length === 0) return `empty group ${node.id} inside split`
    let seenUnpinned = false
    for (const tid of node.tabIds) {
      if (seenTabIds.has(tid)) return `tab ${tid} appears in multiple groups`
      seenTabIds.add(tid)
      const tab = state.tabs[tid]
      if (!tab) return `group ${node.id} references missing tab ${tid}`
      if (tab.pinned && seenUnpinned) return `pinned tab ${tid} appears after unpinned tab in ${node.id}`
      if (!tab.pinned) seenUnpinned = true
    }
    if (node.tabIds.length === 0) {
      if (node.activeTabId !== null || node.previewTabId !== null) return `empty group ${node.id} has non-null active/preview`
    } else {
      if (!node.activeTabId || !node.tabIds.includes(node.activeTabId)) return `invalid activeTabId on ${node.id}`
      if (node.previewTabId !== null) {
        if (!node.tabIds.includes(node.previewTabId)) return `invalid previewTabId on ${node.id}`
        if (state.tabs[node.previewTabId]?.pinned) return `previewTabId ${node.previewTabId} cannot be pinned`
      }
    }
    return null
  }

  const err = walk(state.root)
  if (err) return { ok: false, reason: err }
  if (!groupIds.has(state.activeGroupId)) return { ok: false, reason: `activeGroupId ${state.activeGroupId} not in tree` }
  for (const tid of Object.keys(state.tabs)) {
    if (!seenTabIds.has(tid)) return { ok: false, reason: `orphaned tab ${tid} in tabs map` }
  }
  return { ok: true }
}

function makeFileTab(name: string, pinned = false): WorkbenchTab {
  return {
    id: `file:/data/${name}`,
    pinned,
    target: { kind: 'file', filePath: `/data/${name}` },
  }
}

describe('workbench binary split & tab tree', () => {
  it('replaces single-slot preview tab in place and keeps open on double-click promotion', () => {
    let state = createEmptyWorkbenchState()
    state = insertTab(state, makeFileTab('a.ts'), { preview: true })
    const g1 = listGroups(state.root)[0]!
    expect(g1.previewTabId).toBe('file:/data/a.ts')
    expect(g1.tabIds).toEqual(['file:/data/a.ts'])

    state = insertTab(state, makeFileTab('b.ts'), { preview: true })
    expect(state.tabs['file:/data/a.ts']).toBeUndefined()
    expect(listGroups(state.root)[0]!.tabIds).toEqual(['file:/data/b.ts'])
    expect(listGroups(state.root)[0]!.previewTabId).toBe('file:/data/b.ts')

    // Promote b.ts -> next preview c.ts appends beside b.ts instead of evicting it.
    state = keepOpenTab(state, 'file:/data/b.ts')
    expect(listGroups(state.root)[0]!.previewTabId).toBeNull()
    state = insertTab(state, makeFileTab('c.ts'), { preview: true })
    expect(listGroups(state.root)[0]!.tabIds).toEqual(['file:/data/b.ts', 'file:/data/c.ts'])
    expect(listGroups(state.root)[0]!.previewTabId).toBe('file:/data/c.ts')
    expect(validateWorkbenchState(state)).toEqual({ ok: true })
  })

  it('maintains pinned tabs in the leading segment and clears preview when pinned', () => {
    let state = createEmptyWorkbenchState()
    state = insertTab(state, makeFileTab('one.ts'))
    state = insertTab(state, makeFileTab('two.ts'), { preview: true })
    state = setTabPinned(state, 'file:/data/two.ts', true)

    const group = listGroups(state.root)[0]!
    expect(group.tabIds).toEqual(['file:/data/two.ts', 'file:/data/one.ts'])
    expect(group.previewTabId).toBeNull()

    // Reordering an unpinned tab cannot cross into the pinned prefix.
    state = insertTab(state, makeFileTab('three.ts'))
    state = reorderTabInGroup(state, group.id, 'file:/data/three.ts', 0)
    expect(listGroups(state.root)[0]!.tabIds).toEqual([
      'file:/data/two.ts',
      'file:/data/three.ts',
      'file:/data/one.ts',
    ])
    expect(validateWorkbenchState(state)).toEqual({ ok: true })
  })

  it('tracks spatial top-left first group and its right neighbor across splits and collapses', () => {
    let state = createEmptyWorkbenchState('left-group')
    state = insertTab(state, makeFileTab('left.ts'))
    expect(findRightNeighborOfSpatialFirst(state.root)).toBeNull()

    state = splitGroupWithTab(state, 'left-group', 'right', makeFileTab('right.ts'))
    const rightGroup = findGroupOfTab(state.root, 'file:/data/right.ts')!
    expect(findSpatialFirstGroupId(state.root)).toBe('left-group')
    expect(findRightNeighborOfSpatialFirst(state.root)).toBe(rightGroup.id)

    // Splitting to the LEFT of left-group makes the new group spatial-first, with left-group as its right neighbor.
    state = splitGroupWithTab(state, 'left-group', 'left', makeFileTab('far-left.ts'))
    const farLeftGroup = findGroupOfTab(state.root, 'file:/data/far-left.ts')!
    expect(findSpatialFirstGroupId(state.root)).toBe(farLeftGroup.id)
    expect(findRightNeighborOfSpatialFirst(state.root)).toBe('left-group')

    // Closing far-left collapses its split and restores left-group as spatial first.
    state = removeTab(state, 'file:/data/far-left.ts')
    expect(findSpatialFirstGroupId(state.root)).toBe('left-group')
    expect(findRightNeighborOfSpatialFirst(state.root)).toBe(rightGroup.id)

    // Closing left-group collapses root back to rightGroup alone.
    state = removeTab(state, 'file:/data/left.ts')
    expect(state.root.kind).toBe('group')
    expect(findSpatialFirstGroupId(state.root)).toBe(rightGroup.id)
    expect(findRightNeighborOfSpatialFirst(state.root)).toBeNull()
    expect(validateWorkbenchState(state)).toEqual({ ok: true })
  })

  it('clamps split sash ratios to enforce minimum column and row pixel bounds', () => {
    expect(Math.round(clampSplitRatioForSize(0.01, 'row', 1000) * 1000)).toBeGreaterThanOrEqual(WORKBENCH_MIN_COL_PX)
    expect(Math.round((1 - clampSplitRatioForSize(0.99, 'row', 1000)) * 1000)).toBeGreaterThanOrEqual(WORKBENCH_MIN_COL_PX)
    expect(Math.round(clampSplitRatioForSize(0.01, 'col', 600) * 600)).toBeGreaterThanOrEqual(WORKBENCH_MIN_ROW_PX)
    expect(Math.round((1 - clampSplitRatioForSize(0.99, 'col', 600)) * 600)).toBeGreaterThanOrEqual(WORKBENCH_MIN_ROW_PX)
  })

  it('sanitizes persisted snapshots at the trust boundary and rejects corrupt payloads', () => {
    expect(parseWorkbenchState(null)).toBeNull()
    expect(parseWorkbenchState({ version: 2 })).toBeNull()

    let state = createEmptyWorkbenchState()
    state = insertTab(state, makeFileTab('a.ts'))
    state = splitGroupWithTab(state, listGroups(state.root)[0]!.id, 'right', makeFileTab('b.ts'))
    const roundTripped = parseWorkbenchState(JSON.parse(JSON.stringify(state)))
    expect(roundTripped).toEqual(state)

    // Duplicate tabId across two groups in corrupted storage must be deduplicated cleanly.
    const corrupted = {
      version: 1,
      activeGroupId: 'g1',
      root: {
        kind: 'split',
        id: 's1',
        axis: 'row',
        ratio: 0.5,
        first: { kind: 'group', id: 'g1', tabIds: ['file:/data/a.ts'], activeTabId: 'file:/data/a.ts', previewTabId: null },
        second: { kind: 'group', id: 'g2', tabIds: ['file:/data/a.ts'], activeTabId: 'file:/data/a.ts', previewTabId: null },
      },
      tabs: { 'file:/data/a.ts': makeFileTab('a.ts') },
    }
    const sanitized = parseWorkbenchState(corrupted)
    expect(sanitized).not.toBeNull()
    expect(validateWorkbenchState(sanitized!)).toEqual({ ok: true })
  })

  it('preserves all structural invariants across 200 seeds × 40 randomized operations', () => {
    function mulberry32(seed: number) {
      let t = seed >>> 0
      return () => {
        t += 0x6D2B79F5
        let r = Math.imul(t ^ (t >>> 15), 1 | t)
        r ^= r + Math.imul(r ^ (r >>> 7), 61 | r)
        return ((r ^ (r >>> 14)) >>> 0) / 4294967296
      }
    }

    const dirs: WorkbenchSplitDirection[] = ['left', 'right', 'top', 'bottom']
    const zones: WorkbenchDropZone[] = ['left', 'right', 'top', 'bottom', 'center']

    for (let seed = 1; seed <= 200; seed += 1) {
      const rand = mulberry32(seed)
      const pick = <T>(arr: readonly T[]): T => arr[Math.floor(rand() * arr.length)]!
      let state: WorkbenchState = createEmptyWorkbenchState()
      let counter = 0

      for (let step = 0; step < 40; step += 1) {
        const groups = listGroups(state.root)
        const tabIds = Object.keys(state.tabs)
        const op = Math.floor(rand() * 9)

        if (op === 0 || tabIds.length === 0) {
          counter += 1
          const preview = rand() < 0.55
          const pinned = !preview && rand() < 0.2
          const targetGroup = pick(groups)
          state = insertTab(
            state,
            makeFileTab(`f-${seed}-${counter}.ts`, pinned),
            { groupId: targetGroup.id, preview, activate: rand() < 0.8 },
          )
        } else if (op === 1) {
          state = keepOpenTab(state, pick(tabIds))
        } else if (op === 2) {
          const tid = pick(tabIds)
          state = setTabPinned(state, tid, !state.tabs[tid]!.pinned)
        } else if (op === 3 && groups.length < 6) {
          counter += 1
          const anchor = pick(groups)
          state = splitGroupWithTab(
            state,
            anchor.id,
            pick(dirs),
            makeFileTab(`s-${seed}-${counter}.ts`, false),
            { preview: rand() < 0.4, activate: rand() < 0.8 },
          )
        } else if (op === 4) {
          const tid = pick(tabIds)
          const dest = pick(groups)
          const idx = Math.floor(rand() * (dest.tabIds.length + 1))
          state = moveTabToGroup(state, tid, dest.id, pick(zones), idx)
        } else if (op === 5) {
          const g = pick(groups)
          if (g.tabIds.length > 1) {
            const tid = pick(g.tabIds)
            const idx = Math.floor(rand() * g.tabIds.length)
            state = reorderTabInGroup(state, g.id, tid, idx)
          }
        } else if (op === 6) {
          state = removeTab(state, pick(tabIds))
        } else if (op === 7) {
          state = rand() < 0.5 ? activateTab(state, pick(tabIds)) : activateGroup(state, pick(groups).id)
        } else if (op === 8 && state.root.kind === 'split') {
          state = setSplitRatio(state, state.root.id, 0.15 + rand() * 0.7)
        }

        const check = validateWorkbenchState(state)
        if (!check.ok) {
          throw new Error(`seed=${seed} step=${step} op=${op} failed invariant: ${check.reason}`)
        }
        expect(findGroup(state.root, findSpatialFirstGroupId(state.root))).not.toBeNull()
      }
    }
  })

  it('aligns same-group center drops with visual pre-removal slot indices', () => {
    let state = createEmptyWorkbenchState('g1')
    state = insertTab(state, makeFileTab('a.ts'))
    state = insertTab(state, makeFileTab('b.ts'))
    state = insertTab(state, makeFileTab('c.ts'))

    // Visually [a, b, c]: dropping `a` before `c` (slot boundary 2) places `a` between `b` and `c`.
    const movedBeforeC = moveTabToGroup(state, 'file:/data/a.ts', 'g1', 'center', 2)
    expect(listGroups(movedBeforeC.root)[0]!.tabIds).toEqual([
      'file:/data/b.ts',
      'file:/data/a.ts',
      'file:/data/c.ts',
    ])

    // Dropping `a` after `c` (slot boundary 3) places `a` at the end.
    const movedToEnd = moveTabToGroup(state, 'file:/data/a.ts', 'g1', 'center', 3)
    expect(listGroups(movedToEnd.root)[0]!.tabIds).toEqual([
      'file:/data/b.ts',
      'file:/data/c.ts',
      'file:/data/a.ts',
    ])
  })

  it('identifies top-edge groups accurately across row, column, and nested splits', () => {
    let state = createEmptyWorkbenchState('g1')
    state = insertTab(state, makeFileTab('a.ts'))
    expect(isTopEdgeGroup(state.root, 'g1')).toBe(true)

    // Horizontal split (row): both left and right touch the top edge.
    state = splitGroupWithTab(state, 'g1', 'right', makeFileTab('b.ts'))
    const rightGroup = findGroupOfTab(state.root, 'file:/data/b.ts')!
    expect(isTopEdgeGroup(state.root, 'g1')).toBe(true)
    expect(isTopEdgeGroup(state.root, rightGroup.id)).toBe(true)

    // Vertical split (col) inside left column: top touches top edge, bottom does not; right column still touches top edge.
    state = splitGroupWithTab(state, 'g1', 'bottom', makeFileTab('c.ts'))
    const bottomLeftGroup = findGroupOfTab(state.root, 'file:/data/c.ts')!
    expect(isTopEdgeGroup(state.root, 'g1')).toBe(true)
    expect(isTopEdgeGroup(state.root, bottomLeftGroup.id)).toBe(false)
    expect(isTopEdgeGroup(state.root, rightGroup.id)).toBe(true)

    // Root bottom split: entire new bottom strip does not touch the top edge.
    state = splitGroupWithTab(state, WORKBENCH_ROOT_DROP_ID, 'bottom', makeFileTab('d.ts'))
    const rootBottomGroup = findGroupOfTab(state.root, 'file:/data/d.ts')!
    expect(isTopEdgeGroup(state.root, rootBottomGroup.id)).toBe(false)
    expect(isTopEdgeGroup(state.root, 'g1')).toBe(true)
    expect(isTopEdgeGroup(state.root, rightGroup.id)).toBe(true)
  })

  it('rejects prototype keys and reserved root drop id in parseWorkbenchState', () => {
    const withReservedRootId = {
      version: 1,
      activeGroupId: WORKBENCH_ROOT_DROP_ID,
      root: { kind: 'group', id: WORKBENCH_ROOT_DROP_ID, tabIds: ['file:/data/a.ts'], activeTabId: 'file:/data/a.ts', previewTabId: null },
      tabs: { 'file:/data/a.ts': makeFileTab('a.ts') },
    }
    expect(parseWorkbenchState(withReservedRootId)).toBeNull()

    const withPrototypeTabIds = {
      version: 1,
      activeGroupId: 'g1',
      root: {
        kind: 'group',
        id: 'g1',
        tabIds: ['toString', 'constructor', 'file:/data/good.ts'],
        activeTabId: 'toString',
        previewTabId: null,
      },
      tabs: {
        'file:/data/good.ts': makeFileTab('good.ts'),
      },
    }
    const parsed = parseWorkbenchState(withPrototypeTabIds)
    expect(parsed).not.toBeNull()
    expect(Object.keys(parsed!.tabs)).toEqual(['file:/data/good.ts'])
    expect(listGroups(parsed!.root)[0]!.tabIds).toEqual(['file:/data/good.ts'])
    expect(validateWorkbenchState(parsed!)).toEqual({ ok: true })
  })
})
