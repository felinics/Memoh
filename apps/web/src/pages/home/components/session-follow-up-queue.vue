<template>
  <div
    v-if="hasItems"
    data-session-follow-up-queue
    class="order-first w-full basis-full px-1 pb-1"
  >
    <div
      ref="list"
      class="space-y-1"
    >
      <QueueItem
        v-for="item in items"
        :key="item.item_id"
        :item="item"
        :busy="isBusy(item)"
        @save="save(item, $event)"
        @steer="steer(item)"
        @remove="remove(item)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Sortable from 'sortablejs'
import { toast } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useSessionFollowUpQueue, type EditableFollowUpQueueItem } from './use-session-follow-up-queue'
import QueueItem from './session-follow-up-queue-item.vue'

const props = defineProps<{
  botId: string
  sessionId: string
  active?: boolean
  /**
   * Opaque change signal. The parent derives it from the runtime projection
   * (steer claims/applies, run terminal transitions) and from its own
   * enqueues so the list refreshes on events rather than on a timer.
   */
  refreshKey?: unknown
}>()

const { t } = useI18n()
const { items, hasItems, busy, refresh, update, remove: removeItem, steer: steerItem, reorder } = useSessionFollowUpQueue(
  () => props.botId,
  () => props.sessionId,
  () => props.active ?? false,
  () => props.refreshKey,
)
const list = ref<HTMLElement | null>(null)
let sortable: Sortable | null = null

const isBusy = (item: EditableFollowUpQueueItem) => !!item.item_id && busy.value.has(item.item_id)
const save = async (item: EditableFollowUpQueueItem, text: string) => {
  item.text = text
  await update(item)
}
const remove = async (item: EditableFollowUpQueueItem) => {
  await removeItem(item)
}
const steer = async (item: EditableFollowUpQueueItem) => {
  try {
    await steerItem(item)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('chat.queue.steerFailed')))
  }
}

async function rebuildSortable() {
  sortable?.destroy()
  sortable = null
  await nextTick()
  if (!list.value) return
  sortable = Sortable.create(list.value, {
    animation: 120,
    direction: 'vertical',
    handle: '[data-queue-handle]',
    draggable: '[data-queue-item]',
    forceFallback: true,
    fallbackOnBody: true,
    onEnd: async event => {
      if (event.oldIndex == null || event.newIndex == null || event.oldIndex === event.newIndex) return
      try {
        await reorder(event.oldIndex, event.newIndex)
      } catch {
        await refresh()
      }
    },
  })
}

watch(items, rebuildSortable, { flush: 'post' })
onBeforeUnmount(() => sortable?.destroy())
</script>
