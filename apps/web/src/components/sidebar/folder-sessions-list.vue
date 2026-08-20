<template>
  <div>
    <div
      v-for="session in visibleSessions"
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
      v-if="paging.loading"
      class="flex justify-center py-2 pl-4"
    >
      <Spinner class="size-4" />
    </div>

    <!-- A failed page also leaves the Show more button below, but this row is
         the one that says the last attempt failed rather than "there is more". -->
    <div
      v-else-if="paging.error"
      class="pb-0.5 pl-4"
    >
      <TextButton
        :class="footerActionClass"
        :data-testid="`folder-load-retry-${workdirId}`"
        @click="retryLoad"
      >
        {{ t('chat.actions.retry') }}
      </TextButton>
    </div>

    <!-- Explicit Show more, NOT infinite scroll: folders now flow inside the
         sessions panel's single scroll container, so a folder that grew
         whenever its tail neared the viewport would keep pushing Recents (and
         every folder below it) out of reach. Each click reveals one more page
         of rows and only touches the network when the revealed window runs
         past what has been fetched. -->
    <div
      v-else-if="canShowMore"
      class="pb-0.5 pl-4"
    >
      <TextButton
        :class="footerActionClass"
        :data-testid="`folder-show-more-${workdirId}`"
        @click="showMore"
      >
        {{ t('chat.showMoreSessions') }}
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
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { Spinner, TextButton } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import type { SessionSummary } from '@/composables/api/useChat'
import SessionItem from './session-item.vue'

const props = defineProps<{
  workdirId: string
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

// Show more / Retry sit under the folder's rows and read as list footers, so
// their labels line up with the session titles above them.
const footerActionClass = 'ml-[5px] text-xs' /* ui-allow-px: 5px + the text button's own 6px inset = the 11px sidebar row gutter, so the label lands on the session titles' x */

/** Rows an expanded folder shows before it asks. One folder must never be able
 *  to bury the folders under it — or Recents — under its own history. */
const FOLDER_VISIBLE_STEP = 8

const visibleCount = ref(FOLDER_VISIBLE_STEP)
watch(() => props.workdirId, () => {
  visibleCount.value = FOLDER_VISIBLE_STEP
})

const visibleSessions = computed(() => sessions.value.slice(0, visibleCount.value))

// A fetched page is much larger than the reveal step, so "more" usually means
// rows already in the store; hasMore only matters once the window catches up.
const canShowMore = computed(() => (
  sessions.value.length > visibleCount.value || paging.value.hasMore
))

function showMore() {
  visibleCount.value += FOLDER_VISIBLE_STEP
  if (sessions.value.length < visibleCount.value && paging.value.hasMore) {
    void chatStore.loadMoreWorkdirSessions(props.workdirId)
  }
}

function retryLoad() {
  if (paging.value.loaded) void chatStore.loadMoreWorkdirSessions(props.workdirId)
  else void chatStore.ensureWorkdirSessions(props.workdirId)
}
</script>
