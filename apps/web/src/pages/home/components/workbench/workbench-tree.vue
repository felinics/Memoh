<template>
  <div
    v-if="node.kind === 'split'"
    :key="node.id"
    ref="splitEl"
    class="flex h-full min-h-0 w-full min-w-0 flex-1"
    :class="node.axis === 'row' ? 'flex-row' : 'flex-col'"
    :data-workbench-split-id="node.id"
  >
    <div
      class="flex min-h-0 min-w-0 overflow-hidden"
      :style="firstStyle"
    >
      <WorkbenchTree
        :key="node.first.id"
        :node="node.first"
        @slot-mounted="emit('slotMounted', $event)"
        @slot-unmounted="emit('slotUnmounted', $event)"
      />
    </div>

    <div
      role="separator"
      :aria-orientation="node.axis === 'row' ? 'vertical' : 'horizontal'"
      data-workbench-sash
      :data-axis="node.axis"
      :data-dragging="sashDragging ? 'true' : undefined"
      class="workbench-sash"
      @pointerdown="onSashPointerDown"
      @pointermove="onSashPointerMove"
      @pointerup="onSashPointerEnd"
      @pointercancel="onSashPointerEnd"
      @dblclick.stop="store.setSplitRatio(node.id, 0.5)"
    />

    <div
      class="flex min-h-0 min-w-0 overflow-hidden"
      :style="secondStyle"
    >
      <WorkbenchTree
        :key="node.second.id"
        :node="node.second"
        @slot-mounted="emit('slotMounted', $event)"
        @slot-unmounted="emit('slotUnmounted', $event)"
      />
    </div>
  </div>

  <section
    v-else
    :key="node.id"
    class="flex h-full min-h-0 w-full min-w-0 flex-1 flex-col bg-surface-editor"
    :data-workbench-group-id="node.id"
    @pointerdown="store.activateGroup(node.id)"
  >
    <WorkbenchTabBar :group="node" />
    <div
      ref="slotEl"
      class="relative min-h-0 min-w-0 flex-1"
      :data-workbench-slot-group-id="node.id"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import {
  WORKBENCH_MIN_COL_PX,
  WORKBENCH_MIN_ROW_PX,
  clampRatio,
  clampSplitRatioForSize,
} from '@/workbench/tree'
import type { WorkbenchNode } from '@/workbench/types'
import WorkbenchTabBar from './workbench-tab-bar.vue'

defineOptions({ name: 'WorkbenchTree' })

const props = defineProps<{
  node: WorkbenchNode
}>()

const emit = defineEmits<{
  slotMounted: [payload: { groupId: string, el: HTMLElement }]
  slotUnmounted: [payload: { groupId: string, el: HTMLElement | null }]
}>()

const store = useWorkspaceTabsStore()
const splitEl = ref<HTMLElement | null>(null)
const slotEl = ref<HTMLElement | null>(null)
const sashDragging = ref(false)
let activePointerId: number | null = null
let registeredSlotGroupId: string | null = null
let registeredSlotEl: HTMLElement | null = null

const firstStyle = computed(() => {
  if (props.node.kind !== 'split') return undefined
  const r = clampRatio(props.node.ratio)
  return props.node.axis === 'row'
    ? { flex: `${r} 1 0%`, minWidth: `min(${WORKBENCH_MIN_COL_PX}px, 45%)` }
    : { flex: `${r} 1 0%`, minHeight: `min(${WORKBENCH_MIN_ROW_PX}px, 45%)` }
})

const secondStyle = computed(() => {
  if (props.node.kind !== 'split') return undefined
  const r = 1 - clampRatio(props.node.ratio)
  return props.node.axis === 'row'
    ? { flex: `${r} 1 0%`, minWidth: `min(${WORKBENCH_MIN_COL_PX}px, 45%)` }
    : { flex: `${r} 1 0%`, minHeight: `min(${WORKBENCH_MIN_ROW_PX}px, 45%)` }
})

watch(
  [() => (props.node.kind === 'group' ? props.node.id : null), slotEl],
  ([gid, el]) => {
    if (registeredSlotGroupId && (registeredSlotGroupId !== gid || registeredSlotEl !== el)) {
      emit('slotUnmounted', { groupId: registeredSlotGroupId, el: registeredSlotEl })
      registeredSlotGroupId = null
      registeredSlotEl = null
    }
    if (gid && el) {
      registeredSlotGroupId = gid
      registeredSlotEl = el
      emit('slotMounted', { groupId: gid, el })
    }
  },
  { immediate: true, flush: 'post' },
)

function finishSashDrag(targetEl?: HTMLElement | null) {
  if (!sashDragging.value) return
  if (targetEl && activePointerId !== null && targetEl.hasPointerCapture?.(activePointerId)) {
    try {
      targetEl.releasePointerCapture(activePointerId)
    } catch {
      // Ignore if pointer already released.
    }
  }
  activePointerId = null
  sashDragging.value = false
  store.panelDragging = false
  document.body.style.cursor = ''
}

function onSashPointerDown(event: PointerEvent) {
  if (props.node.kind !== 'split' || event.button !== 0) return
  event.preventDefault()
  event.stopPropagation()
  const el = event.currentTarget as HTMLElement
  activePointerId = event.pointerId
  el.setPointerCapture?.(event.pointerId)
  sashDragging.value = true
  store.panelDragging = true
  document.body.style.cursor = props.node.axis === 'row' ? 'col-resize' : 'row-resize'
}

function onSashPointerMove(event: PointerEvent) {
  if (!sashDragging.value || props.node.kind !== 'split' || !splitEl.value) return
  const rect = splitEl.value.getBoundingClientRect()
  if (props.node.axis === 'row') {
    if (rect.width <= 0) return
    const raw = (event.clientX - rect.left) / rect.width
    store.setSplitRatio(props.node.id, clampSplitRatioForSize(raw, 'row', rect.width))
  } else {
    if (rect.height <= 0) return
    const raw = (event.clientY - rect.top) / rect.height
    store.setSplitRatio(props.node.id, clampSplitRatioForSize(raw, 'col', rect.height))
  }
}

function onSashPointerEnd(event: PointerEvent) {
  finishSashDrag(event.currentTarget as HTMLElement | null)
}

onBeforeUnmount(() => {
  finishSashDrag()
  if (registeredSlotGroupId) {
    emit('slotUnmounted', { groupId: registeredSlotGroupId, el: registeredSlotEl })
    registeredSlotGroupId = null
    registeredSlotEl = null
  }
})
</script>
