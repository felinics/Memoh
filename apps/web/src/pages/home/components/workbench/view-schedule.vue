<template>
  <div class="h-full w-full overflow-y-auto bg-surface-editor">
    <section
      v-if="hasActivated && effectiveBotId"
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
        :bot-id="effectiveBotId"
        :mode="scheduleId ? 'edit' : 'create'"
        :schedule="schedule"
        @cancel="workspaceTabs.closeTab(tabId)"
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
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { ConfirmDeleteDialog, InlineLoadingRow, toast } from '@felinic/ui'
import {
  deleteBotsByBotIdScheduleById,
  getBotsByBotIdScheduleById,
} from '@memohai/sdk'
import type { ScheduleSchedule } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { scheduleDeleteConfirmKey } from '@/utils/schedule-delete'
import { lazyPanelContent } from './lazy-panel-content'

const ScheduleEditor = lazyPanelContent(() => import('@/pages/bots/components/schedule-editor.vue').then(m => m.default))

const props = defineProps<{
  botId?: string
  tabId: string
  scheduleId?: string
  visible: boolean
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()
const { currentBotId } = storeToRefs(chatStore)
const effectiveBotId = computed(() => (props.botId ?? currentBotId.value ?? '').trim())

const hasActivated = ref(props.visible)
watch(() => props.visible, (isVisible) => {
  if (isVisible) hasActivated.value = true
})

const schedule = ref<ScheduleSchedule | null>(null)
const isLoading = ref(false)
const deleteTarget = ref<ScheduleSchedule | null>(null)
const isDeleting = ref(false)
let fetchSeq = 0
let unmounted = false

onBeforeUnmount(() => {
  unmounted = true
})

async function fetchSchedule() {
  if (!hasActivated.value || unmounted) return
  const seq = ++fetchSeq
  const botId = effectiveBotId.value
  const id = props.scheduleId
  if (!botId || !id) {
    isLoading.value = false
    schedule.value = null
    if (workspaceTabs.loadedBotId === botId) {
      workspaceTabs.updateScheduleTitle(props.tabId, '')
    }
    return
  }
  isLoading.value = true
  try {
    const { data } = await getBotsByBotIdScheduleById({
      path: { bot_id: botId, id },
      throwOnError: true,
    })
    if (unmounted || seq !== fetchSeq || effectiveBotId.value !== botId || props.scheduleId !== id) return
    schedule.value = data ?? null
    if (workspaceTabs.loadedBotId === botId) {
      workspaceTabs.updateScheduleTitle(props.tabId, schedule.value?.name?.trim() ?? '')
    }
  } catch (error) {
    if (unmounted || seq !== fetchSeq || effectiveBotId.value !== botId || props.scheduleId !== id) return
    schedule.value = null
    if (workspaceTabs.loadedBotId === botId) {
      workspaceTabs.updateScheduleTitle(props.tabId, '')
    }
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.loadFailed')))
  } finally {
    if (seq === fetchSeq) {
      isLoading.value = false
    }
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
  const botId = effectiveBotId.value
  if (!item?.id || !botId) return
  isDeleting.value = true
  try {
    await deleteBotsByBotIdScheduleById({
      path: { bot_id: botId, id: item.id },
      throwOnError: true,
    })
    toast.success(t('bots.schedule.deleteSuccess'))
    if (!unmounted && workspaceTabs.loadedBotId === botId) {
      workspaceTabs.closeTab(props.tabId)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.schedule.deleteFailed')))
  } finally {
    isDeleting.value = false
    deleteTarget.value = null
  }
}

watch([hasActivated, effectiveBotId, () => props.scheduleId], ([activated]) => {
  if (activated) void fetchSchedule()
}, { immediate: true })
</script>
