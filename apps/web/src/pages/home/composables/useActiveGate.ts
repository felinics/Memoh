import { onActivated, onDeactivated, ref } from 'vue'

// Whether the owning component is the active instance of a KeepAlive. A
// deactivated component keeps its state but its effects keep running, so
// heavy derivations gate on this to stop paying for a hidden tab.
export function useActiveGate() {
  const active = ref(true)
  onActivated(() => {
    active.value = true
  })
  onDeactivated(() => {
    active.value = false
  })
  return active
}
