<template>
  <!-- Graph section: a muted-tier SectionGroup (same level as the dated
       SettingsSections below, so the tones must match). The chart card is the
       group's BARE body — it carries its own border, the group adds no card. -->
  <SectionGroup
    tone="muted"
    :title="$t('memory.graphTitle')"
    :description="$t('memory.graphViewHint')"
  >
    <template #actions>
      <div
        v-if="graphData"
        class="flex shrink-0 gap-4 text-body text-muted-foreground"
      >
        <span>{{ graphData.nodes.length }} {{ $t('memory.graphNodes') }}</span>
        <span>{{ visibleGraphEdges.length }} {{ $t('memory.graphEdges') }}</span>
      </div>
    </template>

    <div class="relative h-[30rem] overflow-hidden rounded-menu-shell border border-border bg-card">
      <PanePlaceholder
        v-if="loading"
        loading
      >
        {{ $t('common.loading') }}
      </PanePlaceholder>
      <VChart
        v-else-if="graphData && graphData.nodes.length > 0"
        :option="chartOption"
        :autoresize="chartAutoresize"
        :update-options="chartUpdateOptions"
        class="size-full"
        @click="handleNodeClick"
      />
      <PanePlaceholder v-else>
        {{ $t('memory.graphEmpty') }}
      </PanePlaceholder>
    </div>

    <Dialog
      :open="!!selectedNode"
      @update:open="handleDialogOpen"
    >
      <DialogScrollContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {{ selectedNodeTitle }}
          </DialogTitle>
        </DialogHeader>
        <div
          v-if="selectedNode"
          class="space-y-3"
        >
          <div class="flex flex-wrap gap-2">
            <Badge
              v-if="selectedNode.slug"
              variant="info"
              size="sm"
            >
              {{ selectedNode.slug }}
            </Badge>
            <Badge
              v-if="selectedNode.topic"
              variant="success"
              size="sm"
            >
              {{ selectedNode.topic }}
            </Badge>
          </div>
          <p class="whitespace-pre-wrap text-control leading-relaxed text-foreground">
            {{ selectedNode.memory }}
          </p>
        </div>
      </DialogScrollContent>
    </Dialog>
  </SectionGroup>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useDark } from '@vueuse/core'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import type { ECElementEvent } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { GraphChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import { Badge, Dialog, DialogHeader, DialogScrollContent, DialogTitle, PanePlaceholder, SectionGroup } from '@felinic/ui'
import {
  getBotsByBotIdMemoryGraph,
  type HandlersGraphEdge,
  type HandlersGraphNode,
  type HandlersGraphResponse,
} from '@memohai/sdk'

use([CanvasRenderer, GraphChart, TooltipComponent])

type GraphNode = HandlersGraphNode
type GraphEdge = HandlersGraphEdge

interface GraphData {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

interface ChartNodeData extends GraphNode {
  id: string
  name: string
  displayName: string
  symbolSize: number
  itemStyle: { color: string }
}

interface GraphEdgeCandidate extends GraphEdge {
  source: string
  target: string
  strength: number
}

interface ChartTheme {
  label: string
  line: string
  fallback: string
  fontFamily: string
  palette: string[]
}

const props = defineProps<{ botId: string }>()
const MAX_VISIBLE_EDGES_PER_NODE = 4

const isDark = useDark()
const loading = ref(true)
const graphData = ref<GraphData | null>(null)
const selectedNode = ref<GraphNode | null>(null)
let fetchSeq = 0
const chartAutoresize = { throttle: 120 }
const chartUpdateOptions = { lazyUpdate: true }

const colorCanvas = typeof document !== 'undefined'
  ? document.createElement('canvas').getContext('2d', { willReadFrequently: true })
  : null

function readColor(token: string, fallback: string): string {
  if (typeof document === 'undefined') return fallback
  const probe = document.createElement('span')
  probe.style.color = `var(${token})`
  probe.style.display = 'none'
  document.body.appendChild(probe)
  const resolved = getComputedStyle(probe).color
  probe.remove()
  if (!resolved) return fallback
  if (!colorCanvas) return resolved
  try {
    colorCanvas.clearRect(0, 0, 1, 1)
    colorCanvas.fillStyle = '#000'
    colorCanvas.fillStyle = resolved
    colorCanvas.fillRect(0, 0, 1, 1)
    const [r = 0, g = 0, b = 0, a = 255] = colorCanvas.getImageData(0, 0, 1, 1).data
    return a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`
  }
  catch {
    return fallback
  }
}

const chartTheme = computed<ChartTheme>(() => {
  void isDark.value
  return {
    label: readColor('--muted-foreground', '#71717a'),
    line: readColor('--border', '#d4d4d8'),
    fallback: readColor('--muted-foreground', '#71717a'),
    fontFamily: typeof document !== 'undefined' ? getComputedStyle(document.body).fontFamily : 'inherit',
    palette: [
      readColor('--accent-blue', '#2383e2'),
      readColor('--accent-green', '#448361'),
      readColor('--accent-teal', '#2c8b9e'),
      readColor('--accent-orange', '#d9730d'),
      readColor('--accent-pink', '#c14c8a'),
      readColor('--accent-red', '#cd3c3a'),
      readColor('--accent-yellow', '#cb912f'),
      readColor('--accent-purple', '#9065b0'),
    ],
  }
})

const selectedNodeTitle = computed(() => selectedNode.value ? displayName(selectedNode.value) : '')

const graphEdges = computed<GraphEdgeCandidate[]>(() => {
  const edges = graphData.value?.edges ?? []
  return edges
    .filter((edge): edge is GraphEdge & { source: string; target: string } => !!edge.source && !!edge.target)
    .map((edge) => ({
      ...edge,
      strength: graphEdgeStrength(edge),
    }))
})

const visibleGraphEdges = computed<GraphEdgeCandidate[]>(() => {
  const degree = new Map<string, number>()
  const selected: GraphEdgeCandidate[] = []

  for (const edge of [...graphEdges.value].sort(compareGraphEdges)) {
    const sourceDegree = degree.get(edge.source) ?? 0
    const targetDegree = degree.get(edge.target) ?? 0
    if (sourceDegree >= MAX_VISIBLE_EDGES_PER_NODE || targetDegree >= MAX_VISIBLE_EDGES_PER_NODE) {
      continue
    }
    selected.push(edge)
    degree.set(edge.source, sourceDegree + 1)
    degree.set(edge.target, targetDegree + 1)
  }

  return selected.sort((a, b) => {
    if (a.source !== b.source) return a.source < b.source ? -1 : 1
    if (a.target !== b.target) return a.target < b.target ? -1 : 1
    return graphRelRank(a.rel) - graphRelRank(b.rel)
  })
})

const chartOption = computed(() => {
  if (!graphData.value || graphData.value.nodes.length === 0) return {}
  const theme = chartTheme.value
  const nodes = graphData.value.nodes.filter((node): node is GraphNode & { id: string } => !!node.id)
  const edges = visibleGraphEdges.value
  return {
    animation: false,
    animationDurationUpdate: 0,
    tooltip: {
      trigger: 'item',
      formatter: (params: { dataType?: string; data?: ChartNodeData }) => {
        if (params.dataType !== 'node' || !params.data) return ''
        const text = params.data.memory || params.data.label || ''
        return escapeTooltip([
          params.data.displayName,
          text.length > 100 ? `${text.slice(0, 97)}...` : text,
        ].filter(Boolean).join('\n'))
      },
    },
    series: [{
      type: 'graph',
      layout: 'force',
      roam: true,
      draggable: true,
      label: {
        show: true,
        fontSize: 11,
        color: theme.label,
        fontFamily: theme.fontFamily,
        formatter: (params: { data?: ChartNodeData }) => params.data?.displayName ?? '',
      },
      force: {
        initLayout: 'circular',
        repulsion: 200,
        edgeLength: [60, 160],
        gravity: 0.08,
        layoutAnimation: false,
      },
      lineStyle: {
        color: theme.line,
        width: 1,
        curveness: 0.1,
      },
      emphasis: {
        focus: 'adjacency',
        lineStyle: { width: 3 },
      },
      data: nodes.map((node): ChartNodeData => ({
        ...node,
        name: node.id,
        displayName: displayName(node),
        symbolSize: graphNodeSize(node.count),
        itemStyle: {
          color: subjectColor(node.subject || node.slug || node.topic, theme),
        },
      })),
      links: edges.map((edge) => ({
        source: edge.source,
        target: edge.target,
        value: edge.strength,
        lineStyle: {
          width: graphEdgeWidth(edge.strength),
          opacity: graphEdgeOpacity(edge.strength),
        },
      })),
    }],
  }
})

function compareGraphEdges(a: GraphEdgeCandidate, b: GraphEdgeCandidate): number {
  if (a.strength !== b.strength) return b.strength - a.strength
  const relRank = graphRelRank(a.rel) - graphRelRank(b.rel)
  if (relRank !== 0) return relRank
  if (a.source !== b.source) return a.source < b.source ? -1 : 1
  if (a.target !== b.target) return a.target < b.target ? -1 : 1
  return 0
}

function graphEdgeStrength(edge: GraphEdge): number {
  const weight = Number(edge.weight)
  if (Number.isFinite(weight) && weight > 0) return weight
  const count = Number(edge.count)
  if (Number.isFinite(count) && count > 0) return count
  return graphRelWeight(edge.rel)
}

function graphRelWeight(rel: string | undefined): number {
  switch (rel) {
    case 'refs': return 1.2
    case 'same_profile': return 1
    case 'same_topic': return 0.8
    case 'same_day': return 0.5
    default: return 0.4
  }
}

function graphRelRank(rel: string | undefined): number {
  switch (rel) {
    case 'refs': return 0
    case 'same_profile': return 1
    case 'same_topic': return 2
    case 'same_day': return 3
    default: return 100
  }
}

function graphEdgeWidth(strength: number): number {
  return Math.min(3, 0.8 + Math.log2(strength + 1) * 0.55)
}

function graphEdgeOpacity(strength: number): number {
  return Math.min(0.72, 0.32 + Math.log2(strength + 1) * 0.08)
}

function graphNodeSize(count: number | undefined): number {
  const value = Number(count)
  if (!Number.isFinite(value) || value <= 1) return 30
  return Math.min(46, 30 + Math.log2(value) * 6)
}

function displayName(node: GraphNode): string {
  return node.slug || node.subject || node.label || node.id || ''
}

function subjectColor(subject: string | undefined, theme: ChartTheme): string {
  if (!subject || theme.palette.length === 0) return theme.fallback
  let hash = 0
  for (let i = 0; i < subject.length; i++) {
    hash = ((hash << 5) - hash + subject.charCodeAt(i)) | 0
  }
  return theme.palette[Math.abs(hash) % theme.palette.length] ?? theme.fallback
}

function escapeTooltip(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll('\n', '<br>')
}

function isChartNodeData(data: unknown): data is ChartNodeData {
  return typeof data === 'object'
    && data !== null
    && 'id' in data
    && 'name' in data
}

function handleNodeClick(params: ECElementEvent) {
  if (params.dataType === 'node' && isChartNodeData(params.data)) {
    selectedNode.value = params.data
  }
}

function handleDialogOpen(open: boolean) {
  if (!open) {
    selectedNode.value = null
  }
}

function normalizeGraph(data: HandlersGraphResponse | undefined): GraphData {
  return {
    nodes: data?.nodes ?? [],
    edges: data?.edges ?? [],
  }
}

async function fetchGraph() {
  const botId = props.botId.trim()
  const seq = ++fetchSeq
  if (!botId) {
    graphData.value = null
    loading.value = false
    return
  }

  loading.value = true
  try {
    const { data } = await getBotsByBotIdMemoryGraph({
      path: { bot_id: botId },
      throwOnError: true,
    })
    if (seq === fetchSeq) {
      graphData.value = normalizeGraph(data)
    }
  }
  catch (error) {
    if (seq === fetchSeq) {
      console.error('failed to load memory graph', error)
      graphData.value = { nodes: [], edges: [] }
    }
  }
  finally {
    if (seq === fetchSeq) {
      loading.value = false
    }
  }
}

watch(() => props.botId, () => {
  selectedNode.value = null
  void fetchGraph()
}, { immediate: true })

defineExpose({ refresh: fetchGraph })
</script>
