<template>
  <ConfirmDeleteDialog
    v-model:open="deleteOpen"
    :title="t('chat.deleteSession')"
    :description="t('chat.deleteSessionConfirm')"
    :cancel-label="t('common.cancel')"
    :confirm-label="t('common.confirm')"
    :loading="deleteLoading"
    @confirm="handleDelete"
  />

  <Dialog v-model:open="renameOpen">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ t('chat.renameSession') }}</DialogTitle>
        <DialogDescription>{{ t('chat.renameSessionDescription') }}</DialogDescription>
      </DialogHeader>
      <form
        class="space-y-4"
        @submit.prevent="handleRename"
      >
        <Input
          v-model="renameTitle"
          :placeholder="t('chat.renameSessionPlaceholder')"
          :disabled="renameLoading"
          autofocus
        />
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            :disabled="renameLoading"
            @click="renameOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            type="submit"
            :disabled="!renameTitle.trim()"
            :loading="renameLoading"
          >
            {{ t('common.confirm') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
// Shared session rename/delete dialogs: the Recents list and the Folders
// section both surface SessionItem rows, so the dialogs behind their context
// menus live once here instead of being hand-copied per list.
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  ConfirmDeleteDialog,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  toast,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import type { SessionSummary } from '@/composables/api/useChat'
import type { SidebarSessionMode } from '@/store/chat-list.utils'
import { resolveApiErrorMessage } from '@/utils/api-error'

const { t } = useI18n()
const chatStore = useChatStore()

const renameOpen = ref(false)
const renameLoading = ref(false)
const renameTitle = ref('')
const pendingRename = ref<SessionSummary | null>(null)

const deleteOpen = ref(false)
const deleteLoading = ref(false)
const pendingDelete = ref<SessionSummary | null>(null)
const deleteFallbackMode = ref<SidebarSessionMode>('recent')

function openRename(session: SessionSummary) {
  pendingRename.value = session
  renameTitle.value = session.title?.trim() || ''
  renameOpen.value = true
}

function openDelete(session: SessionSummary, opts?: { fallbackMode?: SidebarSessionMode }) {
  pendingDelete.value = session
  deleteFallbackMode.value = opts?.fallbackMode ?? 'recent'
  deleteOpen.value = true
}

async function handleRename() {
  const target = pendingRename.value
  const title = renameTitle.value.trim()
  if (!target || !title || renameLoading.value) return
  renameLoading.value = true
  try {
    await chatStore.renameSession(target.id, title)
    renameOpen.value = false
    pendingRename.value = null
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('chat.renameSessionFailed')))
  } finally {
    renameLoading.value = false
  }
}

async function handleDelete() {
  const target = pendingDelete.value
  if (!target || deleteLoading.value) return
  deleteLoading.value = true
  try {
    await chatStore.removeSession(target.id, { fallbackMode: deleteFallbackMode.value })
    deleteOpen.value = false
    pendingDelete.value = null
  } finally {
    deleteLoading.value = false
  }
}

defineExpose({ openRename, openDelete })
</script>
