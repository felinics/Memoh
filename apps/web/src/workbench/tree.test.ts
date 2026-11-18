import { describe, expect, it } from 'vitest'
import {
  WORKBENCH_MIN_COL_PX,
  WORKBENCH_MIN_ROW_PX,
  activateGroup,
  activateTab,
  clampSplitRatioForSize,
  createEmptyWorkbenchState,
  findAdjacentGroupId,
  findGroup,
  findGroupOfTab,
  findSpatialFirstGroupId,
  insertTab,
  keepOpenTab,
  listGroups,
  moveTabToGroup,
  parseWorkbenchState,
  removeTab,
  reorderTabInGroup,
  setSplitRatio,
  setTabPinned,
  splitGroupWithTab,
  validateWorkbenchState,
} from './tree'
import type { WorkbenchDropZone, WorkbenchSplitDirection, WorkbenchState, WorkbenchTab } from './types'

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
    state = insertTab(state, makeFileTab('a.ts'), { preview: true }).state
    const g1 = listGroups(state.root)[0]!
    expect(g1.previewTabId).toBe('file:/data/a.ts')
    expect(g1.tabIds).toEqual(['file:/data/a.ts'])

    const replaced = insertTab(state, makeFileTab('b.ts'), { preview: true })
    state = replaced.state
    expect(replaced.replacedTabId).toBe('file:/data/a.ts')
    expect(state.tabs['file:/data/a.ts']).toBeUndefined()
    expect(listGroups(state.root)[0]!.tabIds).toEqual(['file:/data/b.ts'])
    expect(listGroups(state.root)[0]!.previewTabId).toBe('file:/data/b.ts')

    // Promote b.ts -> next preview c.ts appends beside b.ts instead of evicting it.
    state = keepOpenTab(state, 'file:/data/b.ts')
    expect(listGroups(state.root)[0]!.previewTabId).toBeNull()
    state = insertTab(state, makeFileTab('c.ts'), { preview: true }).state
    expect(listGroups(state.root)[0]!.tabIds).toEqual(['file:/data/b.ts', 'file:/data/c.ts'])
    expect(listGroups(state.root)[0]!.previewTabId).toBe('file:/data/c.ts')
    expect(validateWorkbenchState(state)).toEqual({ ok: true })
  })

  it('maintains pinned tabs in the leading segment and clears preview when pinned', () => {
    let state = createEmptyWorkbenchState()
    state = insertTab(state, makeFileTab('one.ts')).state
    state = insertTab(state, makeFileTab('two.ts'), { preview: true }).state
    state = setTabPinned(state, 'file:/data/two.ts', true)

    const group = listGroups(state.root)[0]!
    expect(group.tabIds).toEqual(['file:/data/two.ts', 'file:/data/one.ts'])
    expect(group.previewTabId).toBeNull()

    // Reordering an unpinned tab cannot cross into the pinned prefix.
    state = insertTab(state, makeFileTab('three.ts')).state
    state = reorderTabInGroup(state, group.id, 'file:/data/three.ts', 0)
    expect(listGroups(state.root)[0]!.tabIds).toEqual([
      'file:/data/two.ts',
      'file:/data/three.ts',
      'file:/data/one.ts',
    ])
    expect(validateWorkbenchState(state)).toEqual({ ok: true })
  })

  it('tracks spatial top-left first group and right-adjacent group across splits and collapses', () => {
    let state = createEmptyWorkbenchState('left-group')
    state = insertTab(state, makeFileTab('left.ts')).state
    state = splitGroupWithTab(state, 'left-group', 'right', makeFileTab('right.ts'))

    const rightGroup = findGroupOfTab(state.root, 'file:/data/right.ts')!
    expect(findSpatialFirstGroupId(state.root)).toBe('left-group')
    expect(findAdjacentGroupId(state.root, 'left-group', 'right')).toBe(rightGroup.id)
    expect(findAdjacentGroupId(state.root, rightGroup.id, 'left')).toBe('left-group')

    // Splitting to the LEFT of left-group makes the new group the spatial top-left first group.
    state = splitGroupWithTab(state, 'left-group', 'left', makeFileTab('far-left.ts'))
    const farLeftGroup = findGroupOfTab(state.root, 'file:/data/far-left.ts')!
    expect(findSpatialFirstGroupId(state.root)).toBe(farLeftGroup.id)

    // Closing far-left collapses its split and restores left-group as spatial first.
    state = removeTab(state, 'file:/data/far-left.ts')
    expect(findSpatialFirstGroupId(state.root)).toBe('left-group')

    // Closing left-group collapses root back to rightGroup alone.
    state = removeTab(state, 'file:/data/left.ts')
    expect(state.root.kind).toBe('group')
    expect(findSpatialFirstGroupId(state.root)).toBe(rightGroup.id)
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
    state = insertTab(state, makeFileTab('a.ts')).state
    state = splitGroupWithTab(state, listGroups(state.root)[0]!.id, 'right', makeFileTab('b.ts'))
    const roundTripped = parseWorkbenchState(JSON.parse(JSON.stringify(state)))
    expect(roundTripped).toEqual(state)
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
          ).state
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
})
