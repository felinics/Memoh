<template>
  <!-- Stands in for a panel whose code is still downloading, or failed to
       download (see lazyPanel in chat-workspace.vue). -->
  <DockPanelFrame>
    <PanelLoadState :failed="failed" />
  </DockPanelFrame>
</template>

<script setup lang="ts">
import DockPanelFrame from './panel-frame.vue'
import PanelLoadState from './panel-load-state.vue'

defineProps<{
  failed?: boolean
}>()
</script>
