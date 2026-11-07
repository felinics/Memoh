<template>
  <!-- Stands in for a panel whose code is still downloading, or failed to
       download (see lazyPanel in chat-workspace.vue). Retry downloads the
       code again in place; the page is not reloaded, since other open panels
       may hold unsaved edits. -->
  <DockPanelFrame>
    <PanePlaceholder v-if="failed">
      {{ t('common.loadFailed') }}
      <template #action>
        <Button
          variant="outline"
          @click="retry?.()"
        >
          {{ t('common.retry') }}
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

<script lang="ts">
import type { InjectionKey } from 'vue'

/** Provided by each lazily loaded panel: mounts it again, which downloads its code again. */
export const retryPanelLoadKey: InjectionKey<() => void> = Symbol('retry-panel-load')
</script>

<script setup lang="ts">
import { inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, PanePlaceholder } from '@felinic/ui'
import DockPanelFrame from './panel-frame.vue'

defineProps<{
  failed?: boolean
}>()

const { t } = useI18n()

const retry = inject(retryPanelLoadKey, undefined)
</script>
