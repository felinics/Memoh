<template>
  <div>
    <div
      v-for="session in sessions"
      :key="session.id"
      class="pb-0.5 pl-4"
    >
      <SessionItem
        :session="session"
        :is-active="sessionId === session.id"
        :streaming="chatStore.isSessionStreaming(currentBotId, session.id)"
        @select="emit('select', $event)"
        @open-new-tab="emit('open-new-tab', $event)"
        @rename="emit('rename', $event)"
        @delete="emit('delete', $event)"
      />
    </div>

    <div
      v-if="showSentinel"
      ref="loadMoreSentinel"
      class="h-px w-full pl-4"
      :data-testid="`folder-load-more-${workdirId}`"
    />

    <div
      v-if="paging.loading"
      class="flex justify-center py-2 pl-4"
    >
      <Spinner class="size-4" />
    </div>

    <!-- Auto-prefetch stops after a failed page (it made no progress) and a
         folder short of its viewport has no sentinel to kick it again — this
         row is the only way forward. -->
    <div
      v-else-if="paging.error"
      class="pb-0.5 pl-4"
    >
      <TextButton
        class="text-xs"
        :data-testid="`folder-load-retry-${workdirId}`"
        @click="retryLoad"
      >
        {{ t('chat.retry') }}
      </TextButton>
    </div>

    <div
      v-else-if="paging.loaded && sessions.length === 0"
      class="px-3 py-2 pl-4 text-xs text-muted-foreground"
    >
      {{ t('chat.noSessions') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, toRef } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { Spinner, TextButton } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import type { SessionSummary } from '@/composables/api/useChat'
import SessionItem from './session-item.vue'
import { useSidebarInfiniteScroll } from './use-sidebar-infinite-scroll'

const props = defineProps<{
  workdirId: string
  scrollEl: HTMLElement | null
}>()

const emit = defineEmits<{
  select: [session: SessionSummary]
  'open-new-tab': [session: SessionSummary]
  rename: [session: SessionSummary]
  delete: [session: SessionSummary]
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const { sessionId, currentBotId } = storeToRefs(chatStore)

const sessions = computed(() => chatStore.workdirSessionsFor(props.workdirId))
const paging = computed(() => chatStore.workdirSessionsState(props.workdirId))

const scrollElRef = toRef(props, 'scrollEl')

const { loadMoreSentinel, showSentinel } = useSidebarInfiniteScroll({
  scrollEl: scrollElRef,
  hasMore: computed(() => paging.value.hasMore),
  loading: computed(() => paging.value.loading),
  loadMore: () => chatStore.loadMoreWorkdirSessions(props.workdirId),
  // A permission-filtered page can return zero rows with an advanced cursor —
  // count/height alone would read that as "no progress" and stall paging.
  progressCursor: computed(() => paging.value.cursor),
  itemCount: computed(() => sessions.value.length),
})

function retryLoad() {
  if (paging.value.loaded) void chatStore.loadMoreWorkdirSessions(props.workdirId)
  else void chatStore.ensureWorkdirSessions(props.workdirId)
}
</script>
