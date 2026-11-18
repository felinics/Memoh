import { defineStore, getActivePinia } from 'pinia'
import { computed, ref } from 'vue'
import { useQueryCache, useQueryState } from '@pinia/colada'
import { getPing, type HandlersPingResponse } from '@memohai/sdk'
import { onAuthSessionCleared } from '@/lib/auth-session'

const CAPABILITIES_QUERY_KEY = ['server-capabilities'] as const

async function fetchPing(): Promise<HandlersPingResponse> {
  const { data } = await getPing({ throwOnError: true })
  if (!data) throw new Error('Empty ping response')
  return data
}

function hasPiniaColadaInstalled(): boolean {
  const pinia = getActivePinia() as (ReturnType<typeof getActivePinia> & { _s?: Map<string, unknown> }) | undefined
  return Boolean(pinia?._s?.has('_pc_query'))
}

export const useCapabilitiesStore = defineStore('capabilities', () => {
  let inflight: Promise<void> | null = null

  if (hasPiniaColadaInstalled()) {
    const queryCache = useQueryCache()
    const queryState = useQueryState<HandlersPingResponse>(CAPABILITIES_QUERY_KEY)
    const sessionLoaded = ref(false)

    onAuthSessionCleared(() => {
      sessionLoaded.value = false
      inflight = null
    })

    const snapshotSupported = computed(() => (queryState.data.value ? queryState.data.value.snapshot_supported !== false : true))
    const connectors = computed(() => queryState.data.value?.connectors === true)
    const serverVersion = computed(() => queryState.data.value?.version ?? '')
    const commitHash = computed(() => queryState.data.value?.commit_hash ?? '')
    const loaded = computed(() => sessionLoaded.value && queryState.status.value === 'success')

    async function load() {
      if (sessionLoaded.value && queryState.status.value === 'success') return
      if (inflight) return inflight
      inflight = (async () => {
        try {
          const entry = queryCache.ensure({
            key: CAPABILITIES_QUERY_KEY,
            query: fetchPing,
            staleTime: Number.POSITIVE_INFINITY,
          })
          if (!sessionLoaded.value) {
            queryCache.invalidate(entry)
          }
          const state = await queryCache.refresh(entry).catch(() => entry.state.value)
          if (state.status === 'success' && state.data) {
            sessionLoaded.value = true
          }
        } finally {
          inflight = null
        }
      })()
      return inflight
    }

    return {
      snapshotSupported,
      connectors,
      serverVersion,
      commitHash,
      loaded,
      load,
    }
  }

  const rawData = ref<HandlersPingResponse | undefined>(undefined)
  const loadedState = ref(false)

  onAuthSessionCleared(() => {
    rawData.value = undefined
    loadedState.value = false
    inflight = null
  })

  const snapshotSupported = computed(() => (rawData.value ? rawData.value.snapshot_supported !== false : true))
  const connectors = computed(() => rawData.value?.connectors === true)
  const serverVersion = computed(() => rawData.value?.version ?? '')
  const commitHash = computed(() => rawData.value?.commit_hash ?? '')
  const loaded = computed(() => loadedState.value)

  async function load() {
    if (loadedState.value) return
    if (inflight) return inflight
    inflight = (async () => {
      try {
        rawData.value = await fetchPing()
        loadedState.value = true
      } catch {
        // Leave loaded=false so subsequent callers can retry /ping after transient failures.
      } finally {
        inflight = null
      }
    })()
    return inflight
  }

  return {
    snapshotSupported,
    connectors,
    serverVersion,
    commitHash,
    loaded,
    load,
  }
})
