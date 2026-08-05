import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getPing } from '@memohai/sdk'

export const useCapabilitiesStore = defineStore('capabilities', () => {
  const containerBackend = ref('containerd')
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
        containerBackend.value = data.container_backend ?? 'containerd'
        snapshotSupported.value = data.snapshot_supported !== false
        connectors.value = data.connectors === true
        serverVersion.value = data.version ?? ''
        commitHash.value = data.commit_hash ?? ''
      }
    } catch {
      // fallback: assume containerd
    }
    loaded.value = true
  }

  return {
    containerBackend,
    snapshotSupported,
    connectors,
    serverVersion,
    commitHash,
    loaded,
    load,
  }
})
