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
        :steer-supported="steerSupported"
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
const { items, hasItems, steerSupported, busy, update, remove: removeItem, steer: steerItem, reorder } = useSessionFollowUpQueue(
  () => props.botId,
  () => props.sessionId,
  () => props.active ?? false,
  () => props.refreshKey,
)
const list = ref<HTMLElement | null>(null)
let sortable: Sortable | null = null
let drag: { parent: HTMLElement; children: HTMLElement[]; item: HTMLElement } | null = null
let disposed = false

// Sortable moves real DOM nodes; put them back before Vue patches its keyed
// list from the new data order (or from a server refresh).
function restoreDragOrder() {
  if (!drag) return
  for (const child of drag.children) {
    if (child.parentElement === drag.parent) drag.parent.appendChild(child)
  }
}

function destroySortable() {
  if (drag) {
    restoreDragOrder()
    // destroy() calls _onDrop without an event, which leaves fallback clones
    // in document.body. Capture/remove our drag artifacts before it clears
    // the global references. Never sweep other lists' drag elements.
    Sortable.ghost?.remove()
    Sortable.clone?.remove()
    drag.item.classList.remove('sortable-chosen', 'sortable-ghost')
    drag.item.style.willChange = ''
    drag = null
  }
  sortable?.destroy()
  sortable = null
}

const isBusy = (item: EditableFollowUpQueueItem) => !!item.item_id && busy.value.has(item.item_id)
const save = async (item: EditableFollowUpQueueItem, text: string) => {
  if (text.trim() === item.text.trim()) return
  item.text = text
  try {
    await update(item)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}
const remove = async (item: EditableFollowUpQueueItem) => {
  try {
    await removeItem(item)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}
const steer = async (item: EditableFollowUpQueueItem) => {
  try {
    await steerItem(item)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('chat.queue.steerFailed')))
  }
}

function syncSortable() {
  if (disposed || sortable?.el === list.value) return
  destroySortable()
  if (!list.value) return
  sortable = Sortable.create(list.value, {
    animation: 120,
    direction: 'vertical',
    handle: '[data-queue-handle]',
    draggable: '[data-queue-item]',
    forceFallback: true,
    fallbackOnBody: true,
    onChoose: event => {
      drag = {
        parent: event.from,
        children: Array.from(event.from.children) as HTMLElement[],
        item: event.item,
      }
    },
    onUnchoose: () => {
      // Clicking the handle without starting a drag has no onEnd callback.
      if (Sortable.active !== sortable) drag = null
    },
    onEnd: async event => {
      if (!drag) return
      restoreDragOrder()
      drag = null
      if (event.oldIndex == null || event.newIndex == null || event.oldIndex === event.newIndex) return
      try {
        await reorder(event.oldIndex, event.newIndex)
      } catch (error) {
        toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
      }
    },
  })
}

// Keep one instance for the mounted list. Ordinary refreshes must not tear
// down drag listeners; if data changes during a drag, cancel before Vue patches.
watch(items, () => {
  if (!drag) return
  destroySortable()
  void nextTick(syncSortable)
}, { flush: 'pre' })
watch(list, syncSortable, { flush: 'post' })
watch(() => [props.botId, props.sessionId], () => {
  destroySortable()
  void nextTick(syncSortable)
}, { flush: 'pre' })
onBeforeUnmount(() => {
  disposed = true
  destroySortable()
})
</script>
