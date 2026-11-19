<template>
  <div
    v-if="node.kind === 'split'"
    :key="node.id"
    ref="splitEl"
    class="flex h-full min-h-0 w-full min-w-0 flex-1"
    :class="node.axis === 'row' ? 'flex-row' : 'flex-col'"
  >
    <div
      class="flex min-h-0 min-w-0 overflow-hidden"
      :style="firstStyle"
    >
      <WorkbenchTree
        :key="node.first.id"
        :node="node.first"
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
      @dblclick.stop="store.setSplitRatio(node.id, 0.5)"
    />

    <div
      class="flex min-h-0 min-w-0 overflow-hidden"
      :style="secondStyle"
    >
      <WorkbenchTree
        :key="node.second.id"
        :node="node.second"
      />
    </div>
  </div>

  <section
    v-else
    :key="node.id"
    class="flex h-full min-h-0 w-full min-w-0 flex-1 flex-col bg-surface-editor"
    @pointerdown="store.activateGroup(node.id)"
  >
    <WorkbenchTabBar :group="node" />
    <div
      class="relative min-h-0 min-w-0 flex-1"
      :data-workbench-slot-group-id="node.id"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { startWorkbenchSashPointerDrag } from '@/workbench/drag'
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

const store = useWorkspaceTabsStore()
const splitEl = ref<HTMLElement | null>(null)
const sashDragging = ref(false)
let cancelSashDrag: (() => void) | null = null

function splitChildStyle(ratio: number) {
  if (props.node.kind !== 'split') return undefined
  return props.node.axis === 'row'
    ? { flex: `${ratio} 1 0%`, minWidth: `min(${WORKBENCH_MIN_COL_PX}px, 45%)` }
    : { flex: `${ratio} 1 0%`, minHeight: `min(${WORKBENCH_MIN_ROW_PX}px, 45%)` }
}

const firstStyle = computed(() =>
  props.node.kind === 'split' ? splitChildStyle(clampRatio(props.node.ratio)) : undefined,
)
const secondStyle = computed(() =>
  props.node.kind === 'split' ? splitChildStyle(1 - clampRatio(props.node.ratio)) : undefined,
)

function onSashPointerDown(event: PointerEvent) {
  if (props.node.kind !== 'split') return
  const axis = props.node.axis
  const nextCancel = startWorkbenchSashPointerDrag({
    event,
    captureEl: event.currentTarget as HTMLElement | null,
    onStart: () => {
      sashDragging.value = true
      store.panelDragging = true
      document.body.style.cursor = axis === 'row' ? 'col-resize' : 'row-resize'
    },
    onMove: (moveEvent) => {
      if (props.node.kind !== 'split' || !splitEl.value) return
      const rect = splitEl.value.getBoundingClientRect()
      if (props.node.axis === 'row') {
        if (rect.width <= 0) return
        store.setSplitRatio(props.node.id, clampSplitRatioForSize((moveEvent.clientX - rect.left) / rect.width, 'row', rect.width))
      } else {
        if (rect.height <= 0) return
        store.setSplitRatio(props.node.id, clampSplitRatioForSize((moveEvent.clientY - rect.top) / rect.height, 'col', rect.height))
      }
    },
    onEnd: () => {
      cancelSashDrag = null
      sashDragging.value = false
      store.panelDragging = false
      document.body.style.cursor = ''
    },
  })
  if (nextCancel) cancelSashDrag = nextCancel
}

watch(
  () => store.isMobile,
  (mobile) => {
    if (mobile) cancelSashDrag?.()
  },
)

onBeforeUnmount(() => {
  cancelSashDrag?.()
})
</script>
