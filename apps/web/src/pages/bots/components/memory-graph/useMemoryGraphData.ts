import { computed, ref } from 'vue'
import { getBotsByBotIdMemoryGraph } from '@memohai/sdk'
import {
  normalizeGraph,
  selectVisibleGraphEdges,
  toGraphEdgeCandidates,
} from './graph-data'
import type { GraphData } from './types'

export function useMemoryGraphData(botId: () => string) {
  const loading = ref(true)
  const graphData = ref<GraphData | null>(null)
  let fetchSeq = 0

  const graphEdges = computed(() => toGraphEdgeCandidates(graphData.value?.edges ?? []))
  const visibleGraphEdges = computed(() => selectVisibleGraphEdges(graphEdges.value))

  async function fetchGraph() {
    const id = botId().trim()
    const seq = ++fetchSeq
    if (!id) {
      graphData.value = null
      loading.value = false
      return
    }

    loading.value = true
    try {
      const { data } = await getBotsByBotIdMemoryGraph({
        path: { bot_id: id },
        throwOnError: true,
      })
      if (seq === fetchSeq) graphData.value = normalizeGraph(data)
    }
    catch (error) {
      if (seq === fetchSeq) {
        console.error('failed to load memory graph', error)
        graphData.value = { nodes: [], edges: [] }
      }
    }
    finally {
      if (seq === fetchSeq) loading.value = false
    }
  }

  return {
    loading,
    graphData,
    graphEdges,
    visibleGraphEdges,
    fetchGraph,
  }
}
