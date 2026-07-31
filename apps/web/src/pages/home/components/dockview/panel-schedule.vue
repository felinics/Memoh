<template>
  <div class="h-full w-full overflow-y-auto">
    <section
      v-if="currentBotId"
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
      v-else
      class="flex h-full items-center justify-center text-sm text-muted-foreground"
    >
      {{ t('chat.noBotSelected') }}
    </div>

    <ConfirmDeleteDialog
      :open="!!deleteTarget"
      :title="t('bots.schedule.deleteTitle')"
      :description="t('bots.schedule.deleteConfirm', { name: deleteTarget?.name ?? '' })"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('bots.schedule.delete')"
      :loading="isDeleting"
      @update:open="(v) => { if (!v) deleteTarget = null }"
      @confirm="confirmDelete"
    />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { ConfirmDeleteDialog, InlineLoadingRow, toast } from '@felinic/ui'
import { useQueryCache } from '@pinia/colada'
import {
  deleteBotsByBotIdScheduleById,
  getBotsByBotIdScheduleById,
} from '@memohai/sdk'
import type { ScheduleSchedule } from '@memohai/sdk'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import ScheduleEditor from '@/pages/bots/components/schedule-editor.vue'

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
const queryCache = useQueryCache()

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

function invalidateScheduleList() {
  const botId = currentBotId.value
  if (!botId) return
  queryCache.invalidateQueries({ key: ['bot-schedule', botId] })
}

async function handleSaved() {
  toast.success(t('bots.schedule.saveSuccess'))
  invalidateScheduleList()
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
    invalidateScheduleList()
    props.params.api.close()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.deleteFailed')))
  } finally {
    isDeleting.value = false
    deleteTarget.value = null
  }
}

watch([currentBotId, scheduleId], () => {
  void fetchSchedule()
}, { immediate: true })

onBeforeUnmount(() => {
  parametersSub.dispose()
})
</script>
