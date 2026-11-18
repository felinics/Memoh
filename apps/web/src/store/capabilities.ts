import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getPing } from '@memohai/sdk'
import { onAuthSessionCleared } from '@/lib/auth-session'

export const useCapabilitiesStore = defineStore('capabilities', () => {
  const snapshotSupported = ref(true)
  const connectors = ref(false)
  const serverVersion = ref('')
  const commitHash = ref('')
  const loaded = ref(false)
  let inflight: Promise<void> | null = null
  let sessionGeneration = 0

  onAuthSessionCleared(() => {
    sessionGeneration++
    inflight = null
    snapshotSupported.value = true
    connectors.value = false
    serverVersion.value = ''
    commitHash.value = ''
    loaded.value = false
  })

  async function fetchCapabilities(generation: number) {
    try {
      const { data } = await getPing()
      if (!data || generation !== sessionGeneration) return
      snapshotSupported.value = data.snapshot_supported !== false
      connectors.value = data.connectors === true
      serverVersion.value = data.version ?? ''
      commitHash.value = data.commit_hash ?? ''
      loaded.value = true
    } catch {
      // Keep loaded=false so subsequent callers can retry /ping after transient failures.
    } finally {
      if (generation === sessionGeneration) {
        inflight = null
      }
    }
  }

  function load() {
    if (loaded.value) return Promise.resolve()
    if (!inflight) {
      inflight = fetchCapabilities(sessionGeneration)
    }
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
