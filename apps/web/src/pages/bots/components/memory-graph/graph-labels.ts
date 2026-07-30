import { LABEL_MAX_CHARS, LABEL_ZOOM_FADE_FULL, LABEL_ZOOM_FADE_START } from './constants'
import type { GraphNode } from './types'

export function nodeCaption(node: GraphNode): string {
  return (node.memory || node.label || node.subject || node.topic || '').trim()
}

export function truncateLabel(text: string, max = LABEL_MAX_CHARS): string {
  const value = text.trim()
  if (value.length <= max) return value
  return `${value.slice(0, Math.max(0, max - 1))}…`
}

export function labelOpacityForZoom(zoom: number): number {
  if (zoom <= LABEL_ZOOM_FADE_START) return 0
  if (zoom >= LABEL_ZOOM_FADE_FULL) return 1
  return (zoom - LABEL_ZOOM_FADE_START) / (LABEL_ZOOM_FADE_FULL - LABEL_ZOOM_FADE_START)
}

export function isTechnicalSlug(slug: string): boolean {
  return /^mem[-_]/i.test(slug.trim())
}

export function escapeTooltip(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll('\n', '<br>')
}

export function shouldShowZoomLabel(hovered: boolean, zoom: number): boolean {
  return !hovered && labelOpacityForZoom(zoom) > 0.02
}

export function formatNodeTooltipHtml(caption: string): string {
  if (!caption) return ''
  return `<div style="max-width:20rem;white-space:normal;word-break:break-word">${escapeTooltip(caption)}</div>`
}
