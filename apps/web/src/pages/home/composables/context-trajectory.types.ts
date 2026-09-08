import type { HandlersContextTrajectoryBlock, HandlersContextTrajectoryEntry, HandlersContextTrajectoryResponse } from '@memohai/sdk'

export type ContextTrajectoryFilter = 'all' | 'context' | 'requests'

export interface ContextBlockComparison {
  key: string
  before?: HandlersContextTrajectoryBlock
  after?: HandlersContextTrajectoryBlock
  change: 'added' | 'removed' | 'changed' | 'unchanged'
}

export interface ContextCapturePage {
  before?: string
  data: HandlersContextTrajectoryResponse
}

export interface ContextCaptureWindow {
  events: HandlersContextTrajectoryEntry[]
  nextCursor: string | null
  gapCursor: string | null
  invalidEntries: number
}
