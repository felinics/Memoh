<template>
  <div class="relative flex h-full w-full flex-col bg-surface-editor">
    <div class="min-h-0 flex-1">
      <WorkbenchPanelLoadState :failed="failed" />
    </div>
  </div>
</template>

<script setup lang="ts">
import WorkbenchPanelLoadState from './workbench-panel-load-state.vue'

defineProps<{
  failed?: boolean
}>()
</script>
