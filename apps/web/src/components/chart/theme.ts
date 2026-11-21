import { computed, ref, type ComputedRef, type Ref } from 'vue'
import type { EChartsCoreOption } from 'echarts/core'

export type ChartType = 'bar' | 'line' | 'area' | 'pie'
export type ChartLegendPosition = 'auto' | 'top' | 'bottom' | 'right' | 'none'

export interface ChartSeries {
  name: string
  data: number[]
}

export interface ChartPieItem {
  name: string
  value: number
}

export interface MemohChartTheme {
  bar: string
  barMuted: string
  text: string
  line: string
  surface: string
  series: string[]
  pie: string[]
  fontFamily: string
}

export interface DataChartOptionInput {
  type: ChartType
  categories?: string[]
  series?: ChartSeries[]
  pieItems?: ChartPieItem[]
  stacked?: boolean
  smooth?: boolean
  showSymbol?: boolean
  legend?: ChartLegendPosition
  yAxisMax?: number
  xAxisLabelRotate?: number
  xAxisLabelFormatter?: (label: string) => string
  yAxisLabelFormatter?: (value: number) => string
  valueFormatter?: (value: number) => string
  tooltipValueLabel?: string
}

let sharedColorCanvas: CanvasRenderingContext2D | null | undefined
let sharedThemeRevision: Ref<number> | null = null

function getSharedColorCanvas(): CanvasRenderingContext2D | null {
  if (sharedColorCanvas !== undefined) return sharedColorCanvas
  sharedColorCanvas = document.createElement('canvas').getContext('2d', { willReadFrequently: true })
  return sharedColorCanvas
}

/**
 * Shared reactive counter bumped whenever the root `<html>` theme attributes
 * (`class` for dark mode, `data-color-scheme` for palette, `style` for UI
 * font) mutate. Observing the root element directly avoids drift between
 * separate `useDark()` instances and reacts to color-scheme / font changes too.
 */
export function useDocumentThemeRevision(): Ref<number> {
  if (sharedThemeRevision) return sharedThemeRevision
  const revision = ref(0)
  sharedThemeRevision = revision
  if (typeof MutationObserver !== 'undefined') {
    const observer = new MutationObserver(() => {
      revision.value += 1
    })
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'data-color-scheme', 'style'],
    })
  }
  return revision
}

/**
 * Rasterizes a resolved CSS color string through a 1x1 canvas pixel to produce
 * concrete `rgb()` / `rgba()` bytes. ECharts' zrender `liftColor` emphasis
 * helper only parses hex/rgb/rgba/hsl — never CSS Color 4 `oklch(...)` — so
 * without this round-trip hovered bars and pie slices paint transparent.
 */
export function rasterizeCanvasColor(
  resolved: string,
  canvas: CanvasRenderingContext2D | null = getSharedColorCanvas(),
): string {
  if (!resolved || !canvas) return resolved
  try {
    canvas.clearRect(0, 0, 1, 1)
    canvas.fillStyle = '#000'
    canvas.fillStyle = resolved
    canvas.fillRect(0, 0, 1, 1)
    const [r = 0, g = 0, b = 0, a = 255] = canvas.getImageData(0, 0, 1, 1).data
    return a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`
  }
  catch {
    return resolved
  }
}

/**
 * Resolves a Memoh CSS design token (e.g. `--foreground`) into a canvas-safe
 * `rgb(...)` / `rgba(...)` string for ECharts.
 */
export function readCanvasTokenColor(
  token: string,
  fallback: string,
  canvas: CanvasRenderingContext2D | null = getSharedColorCanvas(),
): string {
  const probe = document.createElement('span')
  probe.style.color = `var(${token})`
  probe.style.display = 'none'
  document.body.appendChild(probe)
  const resolved = getComputedStyle(probe).color
  probe.remove()
  if (!resolved) return fallback
  return rasterizeCanvasColor(resolved, canvas) || fallback
}

function withRgbAlpha(rgbColor: string, alpha: number): string {
  const match = rgbColor.match(/rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/i)
  if (!match) return rgbColor
  return `rgba(${match[1]}, ${match[2]}, ${match[3]}, ${alpha})`
}

/**
 * Reactive monochrome (black / white / grey) ECharts theme aligned with
 * Memoh's design tokens. Updates automatically when dark mode, color scheme,
 * or UI font changes on the root document element.
 */
export function useChartTheme(): ComputedRef<MemohChartTheme> {
  const themeRevision = useDocumentThemeRevision()

  return computed<MemohChartTheme>(() => {
    void themeRevision.value
    const fontFamily = getComputedStyle(document.body).fontFamily || 'inherit'
    const bar = readCanvasTokenColor('--foreground', '#18181b')
    const barMuted = readCanvasTokenColor('--muted-foreground', '#a1a1aa')
    const ring = readCanvasTokenColor('--ring', '#71717a')
    const line = readCanvasTokenColor('--border', '#e4e4e7')
    const surface = readCanvasTokenColor('--card', '#ffffff')

    return {
      bar,
      barMuted,
      text: barMuted,
      line,
      surface,
      series: [bar, barMuted, ring, line],
      pie: [bar, barMuted, line],
      fontFamily,
    }
  })
}

export function escapeTooltipHtml(raw: string): string {
  return raw
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll('\'', '&#39;')
}

/**
 * Tooltip surface matching Popover/HoverCard tokens directly: same shell
 * radius, hairline border, and dropdown shadow, with ECharts' own chrome zeroed.
 */
export function buildChartTooltipSurface(fontFamily: string) {
  return {
    backgroundColor: 'transparent',
    borderWidth: 0,
    padding: 0,
    extraCssText: [
      'background: var(--popover)',
      'color: var(--popover-foreground)',
      'border: 1px solid var(--border-menu)',
      'border-radius: var(--radius-menu-shell)',
      'box-shadow: var(--shadow-dropdown)',
      'padding: 10px 12px',
      `font-family: ${fontFamily}`,
    ].join('; '),
  }
}

interface AxisTooltipParam {
  seriesName?: string
  value?: number
  color?: string
  axisValueLabel?: string
}

interface ItemTooltipParam {
  name?: string
  value?: number
  percent?: number
}

export function buildAxisTooltipFormatter(formatValue: (v: number) => string) {
  return (params: AxisTooltipParam[] | AxisTooltipParam): string => {
    const list = Array.isArray(params) ? params : [params]
    const head = escapeTooltipHtml(String(list[0]?.axisValueLabel ?? ''))
    const rows = list.map((p) => {
      const val = escapeTooltipHtml(formatValue(typeof p.value === 'number' ? p.value : 0))
      const name = escapeTooltipHtml(String(p.seriesName ?? ''))
      const dotColor = typeof p.color === 'string' && p.color ? p.color : 'var(--muted-foreground)'
      const dot = `<span style="display:inline-block;width:6px;height:6px;border-radius:9999px;margin-right:7px;background:${dotColor};"></span>`
      return '<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;line-height:1.7;">'
        + `<span style="color:var(--muted-foreground);">${dot}${name}</span>`
        + `<span style="color:var(--popover-foreground);font-weight:500;font-variant-numeric:tabular-nums;">${val}</span></div>`
    }).join('')
    return '<div style="font-size:var(--text-body);line-height:var(--text-body--line-height);min-width:140px;">'
      + `<div style="color:var(--muted-foreground);margin-bottom:3px;">${head}</div>${rows}</div>`
  }
}

export function buildItemTooltipFormatter(
  formatValue: (v: number) => string,
  valueLabel?: string,
) {
  return (params: ItemTooltipParam): string => {
    const title = escapeTooltipHtml(String(params.name ?? ''))
    const val = escapeTooltipHtml(formatValue(typeof params.value === 'number' ? params.value : 0))
    const pct = typeof params.percent === 'number' ? ` (${params.percent}%)` : ''
    const labelHtml = valueLabel
      ? `<span style="color:var(--muted-foreground);">${escapeTooltipHtml(valueLabel)}</span>`
      : ''
    return '<div style="font-size:var(--text-body);line-height:var(--text-body--line-height);min-width:140px;">'
      + `<div style="color:var(--popover-foreground);font-weight:500;margin-bottom:3px;">${title}</div>`
      + '<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;line-height:1.7;">'
      + `${labelHtml}<span style="color:var(--popover-foreground);font-weight:500;font-variant-numeric:tabular-nums;">${val}${pct}</span></div></div>`
  }
}

function defaultFormatNumber(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value)
}

function resolveLegendPosition(
  requested: ChartLegendPosition | undefined,
  type: ChartType,
  seriesCount: number,
): Exclude<ChartLegendPosition, 'auto'> {
  if (requested && requested !== 'auto') return requested
  if (type === 'pie') return 'right'
  return seriesCount > 1 ? 'bottom' : 'none'
}

function buildLegendConfig(
  position: Exclude<ChartLegendPosition, 'auto'>,
  names: string[],
  theme: MemohChartTheme,
) {
  if (position === 'none') return undefined

  const base = {
    icon: 'roundRect' as const,
    itemWidth: 8,
    itemHeight: 8,
    textStyle: {
      color: theme.text,
      fontFamily: theme.fontFamily,
      fontSize: 11,
    },
  }

  if (position === 'right') {
    return {
      ...base,
      orient: 'vertical' as const,
      right: 8,
      top: 'middle' as const,
      textStyle: {
        ...base.textStyle,
        overflow: 'truncate' as const,
        width: 130,
      },
    }
  }

  if (position === 'top') {
    return {
      ...base,
      data: names,
      top: 0,
      itemGap: 16,
    }
  }

  return {
    ...base,
    data: names,
    bottom: 0,
    itemGap: 16,
  }
}

export function buildChartOption(
  input: DataChartOptionInput,
  theme: MemohChartTheme,
): EChartsCoreOption {
  const formatValue = input.valueFormatter ?? defaultFormatNumber
  const seriesList = input.series ?? []
  const legendPos = resolveLegendPosition(input.legend, input.type, seriesList.length)

  if (input.type === 'pie') {
    const items: ChartPieItem[] = input.pieItems
      ?? (seriesList[0]?.data ?? []).map((value, idx) => ({
        name: input.categories?.[idx] ?? String(idx + 1),
        value,
      }))

    const legend = buildLegendConfig(legendPos, items.map(i => i.name), theme)

    return {
      color: theme.pie,
      textStyle: { fontFamily: theme.fontFamily },
      tooltip: {
        trigger: 'item',
        ...buildChartTooltipSurface(theme.fontFamily),
        formatter: buildItemTooltipFormatter(formatValue, input.tooltipValueLabel),
      },
      ...(legend ? { legend } : {}),
      series: [
        {
          type: 'pie',
          radius: ['52%', '72%'],
          center: legendPos === 'right' ? ['36%', '50%'] : ['50%', legendPos === 'bottom' ? '45%' : '50%'],
          avoidLabelOverlap: true,
          itemStyle: {
            borderRadius: 4,
            borderColor: theme.surface,
            borderWidth: 2,
          },
          label: { show: false },
          emphasis: { label: { show: false } },
          data: items,
        },
      ],
    }
  }

  const isBar = input.type === 'bar'
  const isArea = input.type === 'area'
  const stacked = Boolean(input.stacked)
  const categories = input.categories ?? []
  const legend = buildLegendConfig(legendPos, seriesList.map(s => s.name), theme)

  const gridTop = legendPos === 'top' ? 36 : 14
  const gridBottom = input.xAxisLabelRotate
    ? 64
    : legendPos === 'bottom'
      ? 40
      : 24

  const series = seriesList.map((s, idx) => {
    const color = theme.series[idx % theme.series.length] ?? theme.bar
    if (isBar) {
      const isTopCap = !stacked || idx === seriesList.length - 1
      return {
        name: s.name,
        type: 'bar' as const,
        ...(stacked ? { stack: 'total' } : { barMaxWidth: 32 }),
        itemStyle: {
          color,
          ...(isTopCap ? { borderRadius: [3, 3, 0, 0] as [number, number, number, number] } : {}),
        },
        data: s.data,
      }
    }

    return {
      name: s.name,
      type: 'line' as const,
      smooth: input.smooth ?? true,
      symbol: input.showSymbol ? 'circle' as const : 'none' as const,
      symbolSize: 5,
      lineStyle: { color, width: 2 },
      itemStyle: { color },
      ...(isArea
        ? {
            areaStyle: {
              color: {
                type: 'linear' as const,
                x: 0,
                y: 0,
                x2: 0,
                y2: 1,
                colorStops: [
                  { offset: 0, color: withRgbAlpha(color, 0.16) },
                  { offset: 1, color: withRgbAlpha(color, 0.01) },
                ],
              },
            },
          }
        : {}),
      data: s.data,
    }
  })

  return {
    textStyle: { fontFamily: theme.fontFamily },
    tooltip: {
      trigger: 'axis',
      ...buildChartTooltipSurface(theme.fontFamily),
      // Use a translucent wash behind bars so narrow bars stay visible on hover.
      ...(isBar
        ? {
            axisPointer: {
              type: 'shadow',
              shadowStyle: { color: 'rgba(128, 128, 128, 0.12)' },
            },
          }
        : {}),
      formatter: buildAxisTooltipFormatter(formatValue),
    },
    ...(legend ? { legend } : {}),
    grid: {
      left: 8,
      right: 8,
      top: gridTop,
      bottom: gridBottom,
      containLabel: true,
    },
    xAxis: {
      type: 'category',
      data: categories,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: theme.line } },
      axisLabel: {
        color: theme.text,
        fontFamily: theme.fontFamily,
        fontSize: 10,
        ...(input.xAxisLabelRotate ? { rotate: input.xAxisLabelRotate } : {}),
        ...(input.xAxisLabelFormatter ? { formatter: input.xAxisLabelFormatter } : {}),
      },
    },
    yAxis: {
      type: 'value',
      ...(input.yAxisMax !== undefined ? { max: input.yAxisMax } : {}),
      axisLine: { show: false },
      splitLine: { lineStyle: { color: theme.line } },
      axisLabel: {
        color: theme.text,
        fontFamily: theme.fontFamily,
        fontSize: 10,
        formatter: input.yAxisLabelFormatter ?? formatValue,
      },
    },
    series,
  }
}
