export type WorkbenchTabKind =
  | 'chat'
  | 'file'
  | 'preview'
  | 'asset'
  | 'terminal'
  | 'browser'
  | 'display'
  | 'schedule'

export type WorkbenchTabTarget =
  | {
      kind: 'chat'
      sessionId: string | null
      explicitSelection: boolean
      fallbackTitle?: string
    }
  | {
      kind: 'file'
      filePath: string
    }
  | {
      kind: 'preview'
      filePath: string
    }
  | {
      kind: 'asset'
      assetKey: string
      name: string
      botId?: string
      contentHash?: string
      src?: string
    }
  | {
      kind: 'terminal'
      instance: number
      shellTitle?: string
    }
  | {
      kind: 'browser'
      instance: number
      address: string
      customTitle?: boolean
    }
  | {
      kind: 'display'
    }
  | {
      kind: 'schedule'
      scheduleId?: string
      customTitle?: string
    }

export interface WorkbenchTab {
  id: string
  target: WorkbenchTabTarget
  /** Pinned tabs sit in the non-scrolling left segment and ignore Close All / Close Others. */
  pinned: boolean
}

export interface WorkbenchGroupNode {
  kind: 'group'
  id: string
  /** Ordered tab IDs; all pinned tabs precede unpinned tabs. */
  tabIds: string[]
  activeTabId: string | null
  /** At most one unpinned preview tab ID per group. */
  previewTabId: string | null
}

export interface WorkbenchSplitNode {
  kind: 'split'
  id: string
  axis: 'row' | 'col'
  /** Share allocated to `first` in (0, 1); `second` receives `1 - ratio`. */
  ratio: number
  first: WorkbenchNode
  second: WorkbenchNode
}

export type WorkbenchNode = WorkbenchGroupNode | WorkbenchSplitNode

export type WorkbenchSplitDirection = 'left' | 'right' | 'top' | 'bottom'
export type WorkbenchDropZone = WorkbenchSplitDirection | 'center'

export interface WorkbenchState {
  version: 1
  root: WorkbenchNode
  activeGroupId: string
  tabs: Record<string, WorkbenchTab>
}
