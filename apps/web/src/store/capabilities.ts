import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getPing } from '@memohai/sdk'

export const useCapabilitiesStore = defineStore('capabilities', () => {
  const snapshotSupported = ref(true)
  const connectors = ref(false)
  const serverVersion = ref('')
  const commitHash = ref('')
  const loaded = ref(false)

  async function load() {
    if (loaded.value) return
    try {
      const { data } = await getPing()
      if (data) {
        snapshotSupported.value = data.snapshot_supported !== false
        connectors.value = data.connectors === true
        serverVersion.value = data.version ?? ''
        commitHash.value = data.commit_hash ?? ''
      }
    } catch {
      // fallback: keep defaults
    }
    loaded.value = true
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
