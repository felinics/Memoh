import { computed } from 'vue'
import { readCanvasTokenColor, useDocumentThemeRevision } from '@/components/chart'
import type { ChartTheme } from './types'

export function useMemoryGraphTheme() {
  const themeRevision = useDocumentThemeRevision()

  const chartTheme = computed<ChartTheme>(() => {
    void themeRevision.value
    return {
      line: readCanvasTokenColor('--border', '#d4d4d8'),
      fallback: {
        core: readCanvasTokenColor('--accent-gray', '#5f5e59'),
        disk: readCanvasTokenColor('--accent-gray-border', '#d4d3cf'),
      },
      fontFamily: getComputedStyle(document.body).fontFamily || 'inherit',
      palette: [
        { core: readCanvasTokenColor('--accent-blue', '#2383e2'), disk: readCanvasTokenColor('--accent-blue-border', '#b6d4f3') },
        { core: readCanvasTokenColor('--accent-green', '#448361'), disk: readCanvasTokenColor('--accent-green-border', '#bed9c9') },
        { core: readCanvasTokenColor('--accent-teal', '#2c8b9e'), disk: readCanvasTokenColor('--accent-teal-border', '#b0dbe4') },
        { core: readCanvasTokenColor('--accent-orange', '#d9730d'), disk: readCanvasTokenColor('--accent-orange-border', '#eaccb2') },
        { core: readCanvasTokenColor('--accent-pink', '#c14c8a'), disk: readCanvasTokenColor('--accent-pink-border', '#eac4d5') },
        { core: readCanvasTokenColor('--accent-red', '#cd3c3a'), disk: readCanvasTokenColor('--accent-red-border', '#f0c5be') },
        { core: readCanvasTokenColor('--accent-yellow', '#cb912f'), disk: readCanvasTokenColor('--accent-yellow-border', '#e8d497') },
        { core: readCanvasTokenColor('--accent-purple', '#9065b0'), disk: readCanvasTokenColor('--accent-purple-border', '#dbc8e8') },
      ],
      tooltip: readCanvasTokenColor('--tooltip', '#18181b'),
      tooltipForeground: readCanvasTokenColor('--tooltip-foreground', '#fafafa'),
      label: readCanvasTokenColor('--foreground', '#18181b'),
    }
  })

  return { chartTheme }
}
