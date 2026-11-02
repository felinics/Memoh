<template>
  <!-- What a schedule card expands into: one row per run, each row the
       conversation that run created. Titles line up with the card's own name
       (12px card gutter = 4px list inset + 8px row inset). -->
  <div class="flex flex-col gap-px border-t border-border p-1">
    <button
      v-for="run in runs"
      :key="run.log_id"
      type="button"
      data-slot="schedule-run-row"
      :class="rowClass"
      :data-ui-selected="run.session_id && run.session_id === sessionId ? '' : undefined"
      :disabled="!run.session_id"
      :title="runTitle(run)"
      @click="open(run)"
    >
      <span
        class="min-w-0 flex-1 truncate text-label"
        :class="[
          run.session_id ? 'text-foreground' : 'text-muted-foreground',
          unreadKind(run) ? 'font-semibold' : '',
        ]"
      >
        {{ run.title?.trim() || scheduleName }}
      </span>
      <span class="flex shrink-0 items-center gap-1 text-caption tabular-nums text-muted-foreground">
        <Spinner
          v-if="isRunning(run)"
          class="size-3"
          :aria-label="t('chat.sessionStreaming')"
        />
        <AttentionDot
          v-else-if="unreadKind(run)"
          :kind="unreadKind(run)!"
          :session-id="run.session_id"
          :attention-at="unread.entries[run.session_id ?? '']?.attentionAt"
        />
        <CircleAlert
          v-else-if="run.status === 'error'"
          class="size-3 text-destructive"
          role="img"
          :aria-label="t('bots.schedule.sessions.failed')"
        />
        {{ formatStartedAt(run.started_at) }}
      </span>
    </button>

    <!-- Only the first load and an explicit Show more spin; a live refresh
         swaps the rows in place without a spinner flashing under them. -->
    <div
      v-if="loading && (!loaded || loadingMore)"
      class="flex justify-center py-1.5"
    >
      <Spinner class="size-4" />
    </div>

    <!-- A failed page keeps the rows already shown; this row says the last
         attempt failed and offers it again. -->
    <TextButton
      v-else-if="error"
      :class="footerActionClass"
      @click="retry"
    >
      {{ t('chat.actions.retry') }}
    </TextButton>

    <TextButton
      v-else-if="nextCursor"
      :class="footerActionClass"
      @click="loadMore"
    >
      {{ t('chat.showMoreSessions') }}
    </TextButton>

    <p
      v-else-if="loaded && runs.length === 0"
      class="px-2 py-1.5 text-xs text-muted-foreground"
    >
      {{ t('bots.schedule.sessions.empty') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { CircleAlert } from 'lucide-vue-next'
import { Spinner, TextButton } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useUnreadStore } from '@/store/unread'
import AttentionDot from './attention-dot.vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { fetchScheduleRunSessions, type ScheduleRunSession } from '@/composables/api/useScheduleRuns'
import { formatCalendarTime } from '@/utils/date-time'

const props = defineProps<{
  botId: string
  scheduleId: string
  /** Fallback row title, the name every run session starts with. */
  scheduleName: string
  /** Bumps whenever the server reports a change to this bot's schedules. */
  revision: number
}>()

const { t, locale } = useI18n()
const chatStore = useChatStore()
const { sessionId } = storeToRefs(chatStore)
const workspaceTabs = useWorkspaceTabsStore()
const unread = useUnreadStore()

/** The run's session is unread: it marks the row the way a Recents row is marked. */
function unreadKind(run: ScheduleRunSession) {
  return run.session_id ? unread.kindOf(run.session_id) : null
}

/** Runs a card shows before it asks; one schedule must not bury the cards below it. */
const PAGE_SIZE = 4
const MAX_REFRESH = 100

// Hover on the sidebar's own overlay rung, selection through data-ui-selected —
// the same pair session-item.vue uses for the session rows elsewhere.
const rowClass = 'flex min-h-[1.875rem] w-full cursor-pointer items-center gap-2 rounded-menu px-2 text-left transition-colors hover:bg-[color:var(--sidebar-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:hover:bg-transparent' /* ui-allow-style: sidebar rows are a local row system (see ui-owners) — same hover token as session-item.vue */

// 2px + the text button's own 6px inset + the list's 4px inset = the card's
// 12px gutter, so "Show more" starts where the run titles start.
const footerActionClass = 'ml-0.5 self-start text-xs'

const runs = ref<ScheduleRunSession[]>([])
const nextCursor = ref<string | null>(null)
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)
const error = ref(false)
let requestSeq = 0

/**
 * Reloads from the newest run while keeping the window the user already
 * revealed, so a live refresh never collapses "Show more" back to one page.
 */
async function reload() {
  const seq = ++requestSeq
  loading.value = true
  error.value = false
  try {
    const page = await fetchScheduleRunSessions(props.botId, props.scheduleId, {
      limit: Math.min(Math.max(runs.value.length, PAGE_SIZE), MAX_REFRESH),
    })
    if (seq !== requestSeq) return
    runs.value = page.items
    nextCursor.value = page.nextCursor
    loaded.value = true
  } catch {
    if (seq === requestSeq) error.value = true
  } finally {
    if (seq === requestSeq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

async function loadMore() {
  const cursor = nextCursor.value
  if (!cursor || loading.value) return
  const seq = ++requestSeq
  loading.value = true
  loadingMore.value = true
  error.value = false
  try {
    const page = await fetchScheduleRunSessions(props.botId, props.scheduleId, {
      limit: PAGE_SIZE * 2,
      cursor,
    })
    if (seq !== requestSeq) return
    const known = new Set(runs.value.map(run => run.log_id))
    runs.value = [...runs.value, ...page.items.filter(run => !known.has(run.log_id))]
    nextCursor.value = page.nextCursor
  } catch {
    if (seq === requestSeq) error.value = true
  } finally {
    if (seq === requestSeq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

function retry() {
  if (loaded.value && nextCursor.value) void loadMore()
  else void reload()
}

watch(
  () => [props.botId, props.scheduleId, props.revision] as const,
  () => { void reload() },
  { immediate: true },
)

function isRunning(run: ScheduleRunSession): boolean {
  return !run.completed_at
}

function formatStartedAt(value: string | undefined): string {
  return formatCalendarTime(value, { locale: locale.value, hour12: false })
}

function runTitle(run: ScheduleRunSession): string {
  if (!run.session_id) return t('bots.schedule.sessions.notStarted')
  if (run.status === 'error') return t('bots.schedule.sessions.failed')
  return ''
}

function open(run: ScheduleRunSession) {
  if (!run.session_id) return
  workspaceTabs.openSessionChat({
    sessionId: run.session_id,
    title: run.title?.trim() || props.scheduleName,
  })
}
</script>
