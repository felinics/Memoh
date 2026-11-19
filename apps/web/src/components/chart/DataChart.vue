<template>
  <!-- Inline height on VChart: vue-echarts puts an inline height on its root,
       which beats a Tailwind height class and collapses the canvas to 0. -->
  <VChart
    :option="option"
    :update-options="UPDATE_OPTIONS"
    autoresize
    :style="{ height: resolvedHeight, width: '100%' }"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import VChart from 'vue-echarts'
import {
  buildChartOption,
  useChartTheme,
  type ChartLegendPosition,
  type ChartPieItem,
  type ChartSeries,
  type ChartType,
} from './theme'

use([
  CanvasRenderer,
  BarChart,
  LineChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
])

const UPDATE_OPTIONS = { notMerge: true } as const

const props = withDefaults(defineProps<{
  type: ChartType
  categories?: string[]
  series?: ChartSeries[]
  pieItems?: ChartPieItem[]
  stacked?: boolean
  smooth?: boolean
  showSymbol?: boolean
  legend?: ChartLegendPosition
  height?: string | number
  yAxisMax?: number
  xAxisLabelRotate?: number
  xAxisLabelFormatter?: (label: string) => string
  yAxisLabelFormatter?: (value: number) => string
  valueFormatter?: (value: number) => string
  tooltipValueLabel?: string
}>(), {
  categories: () => [],
  series: () => [],
  pieItems: undefined,
  stacked: false,
  smooth: true,
  showSymbol: false,
  legend: 'auto',
  height: '18.75rem',
  yAxisMax: undefined,
  xAxisLabelRotate: undefined,
  xAxisLabelFormatter: undefined,
  yAxisLabelFormatter: undefined,
  valueFormatter: undefined,
  tooltipValueLabel: undefined,
})

const chartTheme = useChartTheme()

const resolvedHeight = computed(() =>
  typeof props.height === 'number' ? `${props.height}px` : props.height,
)

const option = computed(() =>
  buildChartOption(
    {
      type: props.type,
      categories: props.categories,
      series: props.series,
      pieItems: props.pieItems,
      stacked: props.stacked,
      smooth: props.smooth,
      showSymbol: props.showSymbol,
      legend: props.legend,
      yAxisMax: props.yAxisMax,
      xAxisLabelRotate: props.xAxisLabelRotate,
      xAxisLabelFormatter: props.xAxisLabelFormatter,
      yAxisLabelFormatter: props.yAxisLabelFormatter,
      valueFormatter: props.valueFormatter,
      tooltipValueLabel: props.tooltipValueLabel,
    },
    chartTheme.value,
  ),
)
</script>
