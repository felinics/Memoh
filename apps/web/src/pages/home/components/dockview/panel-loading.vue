<template>
  <!-- Stands in for a panel whose code is still downloading, or failed to
       download (see panelComponents in chat-workspace.vue). A failure asks
       for a reload instead of reloading on its own: other open panels may
       hold unsaved edits. -->
  <DockPanelFrame>
    <PanePlaceholder v-if="failed">
      {{ t('common.loadFailed') }}
      <template #action>
        <Button
          variant="outline"
          @click="reload"
        >
          {{ t('common.refresh') }}
        </Button>
      </template>
    </PanePlaceholder>
    <PanePlaceholder
      v-else
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button, PanePlaceholder } from '@felinic/ui'
import DockPanelFrame from './panel-frame.vue'

defineProps<{
  failed?: boolean
}>()

const { t } = useI18n()

const reload = () => window.location.reload()
</script>
