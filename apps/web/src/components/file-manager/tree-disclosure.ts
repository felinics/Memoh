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
export function useTreeDisclosure(load: (background: boolean) => Promise<boolean>) {
  const expanded = ref(false)
  const loaded = ref(false)
  const slow = ref(false)
  const spinnerVisible = computed(() => expanded.value && slow.value)

  const spinnerDelay = useTimeoutFn(() => { slow.value = true }, treeSpinnerDelayMs, { immediate: false })

  let inFlight: Promise<void> | null = null
  let pending: boolean | null = null

  function run(background: boolean): Promise<void> {
    const current = (async () => {
      if (!background) spinnerDelay.start()
      try {
        if (await load(background)) loaded.value = true
      } finally {
        if (!background) {
          spinnerDelay.stop()
          slow.value = false
        }
      }
    })()
    inFlight = current
    return current.finally(() => {
      if (inFlight === current) inFlight = null
      if (pending !== null) {
        const next = pending
        pending = null
        void run(next)
      }
    })
  }

  function reload(requestedBackground: boolean | unknown = false): Promise<void> {
    const background = requestedBackground === true
    if (inFlight) {
      // Collapse a burst into one trailing request; a foreground refresh wins.
      pending = pending === null ? background : (pending && background)
      return inFlight
    }
    return run(background)
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
