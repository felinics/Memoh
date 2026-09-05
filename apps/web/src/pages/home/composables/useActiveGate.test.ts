// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, KeepAlive, nextTick, ref, type Ref } from 'vue'
import { useActiveGate } from './useActiveGate'

describe('useActiveGate', () => {
  it('follows KeepAlive activation of the owning component', async () => {
    let gate: Ref<boolean> | null = null
    const child = {
      setup() {
        gate = useActiveGate()
        return () => h('div')
      },
    }
    const show = ref(true)
    const Parent = defineComponent({
      setup() {
        return () => h(KeepAlive, null, show.value ? [h(child)] : [])
      },
    })
    const app = createApp(Parent)
    app.mount(document.createElement('div'))
    try {
      expect(gate!.value).toBe(true)
      show.value = false
      await nextTick()
      expect(gate!.value).toBe(false)
      show.value = true
      await nextTick()
      expect(gate!.value).toBe(true)
    } finally {
      app.unmount()
    }
  })
})
