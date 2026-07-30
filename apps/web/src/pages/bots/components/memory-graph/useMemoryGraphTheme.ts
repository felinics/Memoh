import { computed } from 'vue'
import { useDark } from '@vueuse/core'
import { readResolvedColor } from './graph-nodes'
import type { ChartTheme } from './types'

function readColor(token: string, fallback: string, canvas: CanvasRenderingContext2D | null): string {
  if (typeof document === 'undefined') return fallback
  const probe = document.createElement('span')
  probe.style.color = `var(${token})`
  probe.style.display = 'none'
  document.body.appendChild(probe)
  const resolved = getComputedStyle(probe).color
  probe.remove()
  if (!resolved) return fallback
  return readResolvedColor(resolved, canvas)
}

export function useMemoryGraphTheme() {
  const isDark = useDark()
  const colorCanvas = typeof document !== 'undefined'
    ? document.createElement('canvas').getContext('2d', { willReadFrequently: true })
    : null

  const chartTheme = computed<ChartTheme>(() => {
    void isDark.value
    return {
      line: readColor('--border', '#d4d4d8', colorCanvas),
      fallback: {
        core: readColor('--accent-gray', '#5f5e59', colorCanvas),
        disk: readColor('--accent-gray-border', '#d4d3cf', colorCanvas),
      },
      fontFamily: typeof document !== 'undefined' ? getComputedStyle(document.body).fontFamily : 'inherit',
      palette: [
        { core: readColor('--accent-blue', '#2383e2', colorCanvas), disk: readColor('--accent-blue-border', '#b6d4f3', colorCanvas) },
        { core: readColor('--accent-green', '#448361', colorCanvas), disk: readColor('--accent-green-border', '#bed9c9', colorCanvas) },
        { core: readColor('--accent-teal', '#2c8b9e', colorCanvas), disk: readColor('--accent-teal-border', '#b0dbe4', colorCanvas) },
        { core: readColor('--accent-orange', '#d9730d', colorCanvas), disk: readColor('--accent-orange-border', '#eaccb2', colorCanvas) },
        { core: readColor('--accent-pink', '#c14c8a', colorCanvas), disk: readColor('--accent-pink-border', '#eac4d5', colorCanvas) },
        { core: readColor('--accent-red', '#cd3c3a', colorCanvas), disk: readColor('--accent-red-border', '#f0c5be', colorCanvas) },
        { core: readColor('--accent-yellow', '#cb912f', colorCanvas), disk: readColor('--accent-yellow-border', '#e8d497', colorCanvas) },
        { core: readColor('--accent-purple', '#9065b0', colorCanvas), disk: readColor('--accent-purple-border', '#dbc8e8', colorCanvas) },
      ],
      tooltip: readColor('--tooltip', '#18181b', colorCanvas),
      tooltipForeground: readColor('--tooltip-foreground', '#fafafa', colorCanvas),
      label: readColor('--foreground', '#18181b', colorCanvas),
    }
  })

  return { chartTheme }
}
