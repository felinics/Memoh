<template>
  <!-- Stands in for a panel whose code is still downloading, or failed to
       download (see lazyPanel in chat-workspace.vue). Retry downloads the
       code again in place, without reloading the page, since other open
       panels may hold unsaved edits. Retry can't recover a chunk that a
       deployment has removed since this page loaded; reloading the page
       does, so it is offered too, but only once no file has unsaved edits:
       a reload would drop them without the save/discard prompt. -->
  <DockPanelFrame>
    <PanePlaceholder v-if="failed">
      {{ t('common.loadFailed') }}
      <template #action>
        <div class="flex flex-col items-center gap-2">
          <div class="flex gap-2">
            <Button
              variant="outline"
              @click="retry?.()"
            >
              {{ t('common.retry') }}
            </Button>
            <Button
              variant="ghost"
              :disabled="hasUnsavedFiles"
              @click="reloadPage"
            >
              {{ t('chat.panelLoad.reloadPage') }}
            </Button>
          </div>
          <p
            v-if="hasUnsavedFiles"
            class="max-w-xs text-body"
          >
            {{ t('chat.panelLoad.saveBeforeReload') }}
          </p>
        </div>
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
import { computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, PanePlaceholder } from '@felinic/ui'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import DockPanelFrame from './panel-frame.vue'

defineProps<{
  failed?: boolean
}>()

const { t } = useI18n()

const retry = inject(retryPanelLoadKey, undefined)

const workspaceTabs = useWorkspaceTabsStore()
const hasUnsavedFiles = computed(() => workspaceTabs.dirtyFileCount > 0)

function reloadPage() {
  window.location.reload()
}
</script>
