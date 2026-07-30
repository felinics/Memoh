import type { GraphViewport, PinnedNodePosition } from './types'

export function fitGraphToView(
  positions: Map<string, PinnedNodePosition>,
  viewport: GraphViewport,
  options?: {
    padding?: number
    minSpan?: number
    minZoom?: number
    maxZoom?: number
  },
): { zoom: number, center: [number, number] } | null {
  if (positions.size === 0) return null

  const padding = options?.padding ?? 0.14
  const minSpan = options?.minSpan ?? 80
  const minZoom = options?.minZoom ?? 0.12
  const maxZoom = options?.maxZoom ?? 2

  let minX = Infinity
  let maxX = -Infinity
  let minY = Infinity
  let maxY = -Infinity
  for (const { x, y } of positions.values()) {
    minX = Math.min(minX, x)
    maxX = Math.max(maxX, x)
    minY = Math.min(minY, y)
    maxY = Math.max(maxY, y)
  }

  const graphW = Math.max(maxX - minX, minSpan)
  const graphH = Math.max(maxY - minY, minSpan)
  const cx = (minX + maxX) / 2
  const cy = (minY + maxY) / 2

  const { width, height } = viewport
  if (width <= 0 || height <= 0) return null

  const zoom = 0.92 * Math.min(
    (width * (1 - padding * 2)) / graphW,
    (height * (1 - padding * 2)) / graphH,
  )

  return {
    zoom: Math.max(minZoom, Math.min(zoom, maxZoom)),
    center: [cx, cy],
  }
}

export function wheelZoomScale(deltaY: number, deltaMode: number, viewportHeight: number, rate: number): number {
  const delta = deltaMode === 1
    ? deltaY * 16
    : deltaMode === 2
      ? deltaY * viewportHeight
      : deltaY
  if (delta === 0) return 1
  return Math.exp(-delta * rate)
}
