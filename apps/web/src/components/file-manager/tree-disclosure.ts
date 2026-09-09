import { computed, ref } from 'vue'
import { useTimeoutFn } from '@vueuse/core'

// Only a slow load gets a spinner: fast expands go straight ▸ → ▾ with no
// intermediate frame, so the glyph never flashes on a local listing.
export const treeSpinnerDelayMs = 200

// The disclosure state machine shared by every tree-row surface (Explorer
// node, folder-picker node — see ./tree-row for the same single-home rule on
// the row shape). Loading state lives in the chevron slot via spinnerVisible;
// no surface may insert a transient child row for it (a placeholder row that
// collapses again is exactly the empty-folder height jolt this replaces).
// `load` reports success and must not reject; on failure the node stays
// unloaded so the next expand retries.
export function useTreeDisclosure(load: () => Promise<boolean>) {
  const expanded = ref(false)
  const loaded = ref(false)
  const slow = ref(false)
  const spinnerVisible = computed(() => expanded.value && slow.value)

  const spinnerDelay = useTimeoutFn(() => { slow.value = true }, treeSpinnerDelayMs, { immediate: false })

  async function reload() {
    spinnerDelay.start()
    try {
      if (await load()) loaded.value = true
    } finally {
      spinnerDelay.stop()
      slow.value = false
    }
  }

  async function expand() {
    expanded.value = true
    if (!loaded.value) await reload()
  }

  function toggle() {
    if (expanded.value) expanded.value = false
    else void expand()
  }

  return { expanded, loaded, spinnerVisible, expand, toggle, reload }
}
