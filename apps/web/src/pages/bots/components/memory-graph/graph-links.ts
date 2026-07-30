import type { ChartTheme, GraphEdgeCandidate } from './types'

export function graphEdgeWidth(strength: number): number {
  return Math.min(3, 0.8 + Math.log2(strength + 1) * 0.55)
}

export function graphEdgeOpacity(strength: number): number {
  return Math.min(0.72, 0.32 + Math.log2(strength + 1) * 0.08)
}

export function buildStaticLink(edge: GraphEdgeCandidate, theme: ChartTheme) {
  return {
    source: edge.source,
    target: edge.target,
    value: edge.strength,
    silent: true,
    tooltip: { show: false },
    emphasis: { disabled: true, focus: 'none' as const },
    select: { disabled: true },
    blur: { disabled: true },
    lineStyle: {
      color: theme.line,
      width: graphEdgeWidth(edge.strength),
      opacity: graphEdgeOpacity(edge.strength),
    },
  }
}

export function buildFocusLink(edge: GraphEdgeCandidate, theme: ChartTheme, activeId: string) {
  const base = buildStaticLink(edge, theme)
  const adjacent = edge.source === activeId || edge.target === activeId
  const baseWidth = graphEdgeWidth(edge.strength)
  const baseOpacity = graphEdgeOpacity(edge.strength)
  return {
    ...base,
    lineStyle: adjacent
      ? {
          color: theme.line,
          width: baseWidth,
          opacity: Math.min(0.78, baseOpacity * 1.15),
        }
      : {
          color: theme.line,
          width: baseWidth,
          opacity: 0.05,
        },
  }
}

export function buildGraphLinks(
  edges: GraphEdgeCandidate[],
  theme: ChartTheme,
  hovered: string | null,
  layoutReady: boolean,
) {
  if (!layoutReady || !hovered) {
    return edges.map(edge => buildStaticLink(edge, theme))
  }
  return edges.map(edge => buildFocusLink(edge, theme, hovered))
}
