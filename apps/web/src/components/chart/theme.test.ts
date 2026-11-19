import { describe, expect, it } from 'vitest'
import {
  buildAxisTooltipFormatter,
  buildChartOption,
  buildItemTooltipFormatter,
  rasterizeCanvasColor,
  useDocumentThemeRevision,
  type MemohChartTheme,
} from './theme'

const mockTheme: MemohChartTheme = {
  bar: 'rgb(24, 24, 27)',
  barMuted: 'rgb(161, 161, 170)',
  text: 'rgb(161, 161, 170)',
  line: 'rgb(228, 228, 231)',
  surface: 'rgb(255, 255, 255)',
  series: ['rgb(24, 24, 27)', 'rgb(161, 161, 170)', 'rgb(113, 113, 122)', 'rgb(228, 228, 231)'],
  pie: ['rgb(24, 24, 27)', 'rgb(161, 161, 170)', 'rgb(228, 228, 231)'],
  fontFamily: 'Inter, sans-serif',
}

describe('shared ECharts theme & option builder', () => {
  it('builds stacked bar option with shadow axisPointer and top-segment rounding', () => {
    const option = buildChartOption(
      {
        type: 'bar',
        stacked: true,
        categories: ['2026-11-17', '2026-11-18'],
        series: [
          { name: 'Input', data: [1000, 2000] },
          { name: 'Output', data: [200, 300] },
        ],
      },
      mockTheme,
    ) as {
      tooltip: { axisPointer: { type: string } }
      legend: { bottom: number }
      series: Array<{ stack: string, itemStyle: Record<string, unknown> }>
    }

    expect(option.tooltip.axisPointer.type).toBe('shadow')
    expect(option.legend.bottom).toBe(0)
    expect(option.series).toHaveLength(2)
    expect(option.series[0]?.stack).toBe('total')
    expect(option.series[0]?.itemStyle).toEqual({ color: mockTheme.bar })
    expect(option.series[1]?.itemStyle).toEqual({
      color: mockTheme.barMuted,
      borderRadius: [3, 3, 0, 0],
    })
  })

  it('builds pie donut option from either pieItems or categories + series', () => {
    const option = buildChartOption(
      {
        type: 'pie',
        categories: ['Model A', 'Model B'],
        series: [{ name: 'Tokens', data: [70, 30] }],
        tooltipValueLabel: 'Tokens',
      },
      mockTheme,
    ) as {
      color: string[]
      legend: { orient: string }
      series: Array<{ radius: string[], center: string[], data: Array<{ name: string, value: number }> }>
    }

    expect(option.color).toEqual(mockTheme.pie)
    expect(option.legend.orient).toBe('vertical')
    expect(option.series[0]?.radius).toEqual(['52%', '72%'])
    expect(option.series[0]?.center).toEqual(['36%', '50%'])
    expect(option.series[0]?.data).toEqual([
      { name: 'Model A', value: 70 },
      { name: 'Model B', value: 30 },
    ])
  })

  it('builds area line chart with monochrome translucent gradient fill', () => {
    const option = buildChartOption(
      {
        type: 'area',
        categories: ['May', 'Jun'],
        series: [{ name: 'Hit Rate', data: [60, 80] }],
        yAxisMax: 100,
      },
      mockTheme,
    ) as {
      legend?: unknown
      yAxis: { max: number }
      series: Array<{ type: string, areaStyle: { color: { colorStops: Array<{ offset: number, color: string }> } } }>
    }

    expect(option.legend).toBeUndefined()
    expect(option.yAxis.max).toBe(100)
    expect(option.series[0]?.type).toBe('line')
    expect(option.series[0]?.areaStyle.color.colorStops).toEqual([
      { offset: 0, color: 'rgba(24, 24, 27, 0.16)' },
      { offset: 1, color: 'rgba(24, 24, 27, 0.01)' },
    ])
  })

  it('escapes HTML entities in axis and item tooltip formatters', () => {
    const axisFmt = buildAxisTooltipFormatter(v => `${v}K`)
    const axisHtml = axisFmt([
      { axisValueLabel: '<img src=x>', seriesName: '<b>Input</b>', value: 12, color: 'rgb(0,0,0)' },
    ])
    expect(axisHtml).toContain('&lt;img src=x&gt;')
    expect(axisHtml).toContain('&lt;b&gt;Input&lt;/b&gt;')
    expect(axisHtml).not.toContain('<img')

    const itemFmt = buildItemTooltipFormatter(v => `${v}`, '<Tokens>')
    const itemHtml = itemFmt({ name: '<script>x</script>', value: 42, percent: 50 })
    expect(itemHtml).toContain('&lt;script&gt;x&lt;/script&gt;')
    expect(itemHtml).toContain('&lt;Tokens&gt;')
    expect(itemHtml).not.toContain('<script>')
  })

  it('rasterizes pixel bytes when canvas context is provided and falls back cleanly otherwise', () => {
    const fakeCanvas = {
      clearRect: () => {},
      fillRect: () => {},
      fillStyle: '',
      getImageData: () => ({ data: new Uint8ClampedArray([18, 52, 86, 255]) }),
    } as unknown as CanvasRenderingContext2D

    expect(rasterizeCanvasColor('oklch(0.21 0.004 95)', fakeCanvas)).toBe('rgb(18, 52, 86)')
    expect(rasterizeCanvasColor('rgb(10, 20, 30)', null)).toBe('rgb(10, 20, 30)')
  })

  it('bumps document theme revision when root dark class toggles', async () => {
    const rev = useDocumentThemeRevision()
    const before = rev.value
    document.documentElement.classList.add('dark')
    await Promise.resolve()
    expect(rev.value).toBeGreaterThan(before)
    document.documentElement.classList.remove('dark')
  })
})
