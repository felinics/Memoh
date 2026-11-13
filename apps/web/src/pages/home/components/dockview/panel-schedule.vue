<template>
  <div class="h-full w-full overflow-y-auto">
    <section
      v-if="hasActivated && currentBotId"
      class="mx-auto max-w-2xl px-6 py-8"
    >
      <header class="mb-6 px-2">
        <h1 class="text-lg font-semibold">
          {{ scheduleId ? t('bots.schedule.edit') : t('bots.schedule.create') }}
        </h1>
      </header>

      <InlineLoadingRow
        v-if="isLoading"
        class="px-2"
      >
        {{ t('common.loading') }}
      </InlineLoadingRow>

      <ScheduleEditor
        v-else
        :bot-id="currentBotId"
        :mode="scheduleId ? 'edit' : 'create'"
        :schedule="schedule"
        @cancel="params.api.close()"
        @delete="handleDelete"
        @saved="handleSaved"
      />
    </section>
    <div
      v-else-if="hasActivated"
      class="flex h-full items-center justify-center text-sm text-muted-foreground"
    >
      {{ t('chat.noBotSelected') }}
    </div>

    <ConfirmDeleteDialog
      :open="!!deleteTarget"
      :title="t('bots.schedule.deleteTitle')"
      :description="t(scheduleDeleteConfirmKey(deleteTarget), { name: deleteTarget?.name ?? '' })"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('bots.schedule.delete')"
      :loading="isDeleting"
      @update:open="(v) => { if (!v) deleteTarget = null }"
      @confirm="confirmDelete"
    />
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { ConfirmDeleteDialog, InlineLoadingRow, toast } from '@felinic/ui'
import {
  deleteBotsByBotIdScheduleById,
  getBotsByBotIdScheduleById,
} from '@memohai/sdk'
import type { ScheduleSchedule } from '@memohai/sdk'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { scheduleDeleteConfirmKey } from '@/utils/schedule-delete'
import { usePanelVisible } from './use-panel-visible'

const ScheduleEditor = defineAsyncComponent(withStylesheetRecovery(() => import('@/pages/bots/components/schedule-editor.vue').then(m => m.default)))

const props = defineProps<{
  params: {
    params: { scheduleId?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)

const visible = usePanelVisible(props.params.api)
const hasActivated = ref(visible.value)
watch(visible, (isVisible) => {
  if (isVisible) hasActivated.value = true
})

const scheduleId = ref(typeof props.params.params.scheduleId === 'string' ? props.params.params.scheduleId : undefined)
const schedule = ref<ScheduleSchedule | null>(null)
const isLoading = ref(false)
const deleteTarget = ref<ScheduleSchedule | null>(null)
const isDeleting = ref(false)

const parametersSub = props.params.api.onDidParametersChange((parameters) => {
  const next = parameters as { scheduleId?: unknown }
  scheduleId.value = typeof next.scheduleId === 'string' ? next.scheduleId : undefined
})

async function fetchSchedule() {
  if (!hasActivated.value) return
  const botId = currentBotId.value
  const id = scheduleId.value
  if (!botId || !id) {
    schedule.value = null
    props.params.api.setTitle(t('bots.schedule.title'))
    return
  }
  isLoading.value = true
  try {
    const { data } = await getBotsByBotIdScheduleById({
      path: { bot_id: botId, id },
      throwOnError: true,
    })
    schedule.value = data ?? null
    props.params.api.setTitle(schedule.value?.name?.trim() || t('bots.schedule.title'))
  } catch (error) {
    schedule.value = null
    props.params.api.setTitle(t('bots.schedule.title'))
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.loadFailed')))
  } finally {
    isLoading.value = false
  }
}

async function handleSaved() {
  toast.success(t('bots.schedule.saveSuccess'))
  await fetchSchedule()
}

function handleDelete(item: ScheduleSchedule) {
  deleteTarget.value = item
}

async function confirmDelete() {
  const item = deleteTarget.value
  const botId = currentBotId.value
  if (!item?.id || !botId) return
  isDeleting.value = true
  try {
    await deleteBotsByBotIdScheduleById({
      path: { bot_id: botId, id: item.id },
      throwOnError: true,
    })
    toast.success(t('bots.schedule.deleteSuccess'))
    props.params.api.close()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.deleteFailed')))
  } finally {
    isDeleting.value = false
    deleteTarget.value = null
  }
}

watch([hasActivated, currentBotId, scheduleId], ([activated]) => {
  if (activated) void fetchSchedule()
}, { immediate: true })

onBeforeUnmount(() => {
  parametersSub.dispose()
})
</script>
