import type { HandlersContextTrajectoryEntry, HandlersContextTrajectoryResponse } from '@memohai/sdk'

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
