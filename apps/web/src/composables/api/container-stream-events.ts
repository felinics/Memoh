import type { HandlersCreateContainerResponse } from '@memohai/sdk'
import { isSSEErrorEvent, type SSEErrorEvent } from './sse-error'

export type ContainerCreateLayerStatus = {
  ref: string
  offset: number
  total: number
}

// codesync(container-create-stream): keep these manual SSE payload types in sync
// with internal/handlers/containerd.go.
export type ContainerCreateStreamEvent =
  | { type: 'pulling'; image: string }
  | { type: 'pull_progress'; layers: ContainerCreateLayerStatus[] }
  | { type: 'pull_skipped'; image: string; message?: string }
  | { type: 'pull_delegated'; image: string; message?: string }
  | { type: 'creating' }
  | { type: 'restoring' }
  | { type: 'complete'; container: HandlersCreateContainerResponse }
  | SSEErrorEvent

function isLayerStatus(value: unknown): value is ContainerCreateLayerStatus {
  return !!value
    && typeof value === 'object'
    && typeof (value as { ref?: unknown }).ref === 'string'
    && typeof (value as { offset?: unknown }).offset === 'number'
    && typeof (value as { total?: unknown }).total === 'number'
}

export function containerLayerProgressPercent(layers: ContainerCreateLayerStatus[] | null | undefined): number {
  if (!layers || layers.length === 0) return 0
  let totalOffset = 0
  let totalSize = 0
  for (const layer of layers) {
    totalOffset += layer.offset
    totalSize += layer.total
  }
  return totalSize > 0 ? Math.round((totalOffset / totalSize) * 100) : 0
}

export function isContainerCreateStreamEvent(value: unknown): value is ContainerCreateStreamEvent {
  if (!value || typeof value !== 'object') return false

  const event = value as Record<string, unknown>
  switch (event.type) {
    case 'pulling':
      return typeof event.image === 'string'
    case 'pull_progress':
      return Array.isArray(event.layers) && event.layers.every(isLayerStatus)
    case 'pull_skipped':
    case 'pull_delegated':
      return typeof event.image === 'string'
        && (event.message === undefined || typeof event.message === 'string')
    case 'creating':
    case 'restoring':
      return true
    case 'complete':
      return !!event.container && typeof event.container === 'object'
    case 'error':
      return isSSEErrorEvent(event)
    default:
      return false
  }
}
