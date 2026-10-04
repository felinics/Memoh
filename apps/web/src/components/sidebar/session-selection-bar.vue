<template>
  <!-- Action bar for session multi-select, directly above the list it acts on.
       Selection starts from a row's menu (session-item.vue), never from a
       standing button, so this row only exists while something is checked.
       It is the same group-label row as the panel's other headers
       (SidebarPanelHeader); the red action lives in the confirm dialog. -->
  <SidebarPanelHeader
    v-if="selection.active.value"
    :label="t('chat.selectedSessions', { count: selection.selectedIds.value.size })"
    :label-class="labelClass"
    class="px-2 pb-0.5 pt-1"
  >
    <TextButton
      class="text-xs"
      @click="openDelete"
    >
      {{ t('common.delete') }}
    </TextButton>
    <TextButton
      class="text-xs"
      @click="selection.clear()"
    >
      {{ t('common.cancel') }}
    </TextButton>
  </SidebarPanelHeader>

  <!-- Outside the v-if: a full delete empties the selection and removes the
       bar, which must not cut the dialog's close transition short. -->
  <ConfirmDeleteDialog
    v-model:open="deleteOpen"
    :title="t('chat.deleteSessions')"
    :description="t('chat.deleteSessionsConfirm', { count: pendingIds.length })"
    :cancel-label="t('common.cancel')"
    :confirm-label="t('common.delete')"
    :loading="deleting"
    @confirm="handleDelete"
  />
</template>

<script setup lang="ts">
import { inject, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { ConfirmDeleteDialog, TextButton, toast } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import SidebarPanelHeader from './panel-header.vue'
import { SessionSelectionKey } from './session-selection'

const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)

const injectedSelection = inject(SessionSelectionKey)
if (!injectedSelection) throw new Error('SessionSelectionBar must be used within the sessions panel')
const selection = injectedSelection

const labelClass = 'pl-[11px]' /* ui-allow-px: the sidebar row gutter (session-item.vue), so the count starts on the session titles' x */

const deleteOpen = ref(false)
const deleting = ref(false)
// Snapshot on open: the dialog states the count the user agreed to, and the
// live selection shrinks while the deletes run.
const pendingIds = ref<string[]>([])

function openDelete() {
  pendingIds.value = [...selection.selectedIds.value]
  deleteOpen.value = true
}

async function handleDelete() {
  const ids = pendingIds.value
  const botId = currentBotId.value
  if (ids.length === 0 || deleting.value) return
  deleting.value = true
  try {
    const { failed, error } = await chatStore.removeSessions(ids)
    deleteOpen.value = false
    // Switching bots mid-batch already cleared the selection; don't revive it.
    if (currentBotId.value !== botId) return
    // Failures stay checked, so retrying is one more Delete.
    selection.retain(failed)
    if (failed.length > 0) {
      toast.error(t('chat.deleteSessionsFailed', { count: failed.length }), {
        description: resolveApiErrorMessage(error, '') || undefined,
      })
    }
  } finally {
    deleting.value = false
  }
}
</script>
