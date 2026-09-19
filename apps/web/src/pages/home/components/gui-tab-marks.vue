<template>
  <div
    v-if="marks.length > 0"
    class="flex flex-wrap items-center gap-1.5"
    role="group"
    :aria-label="t('chat.guiMarks.ariaLabel')"
  >
    <div
      v-for="mark in marks"
      :key="mark.key"
      class="flex min-w-0 max-w-full items-center gap-1 rounded-lg border border-border bg-card px-2 py-1 text-xs"
      :class="mark.status === 'active' ? '' : 'opacity-60'"
      :title="markTitle(mark)"
    >
      <component
        :is="mark.kind === 'handoff' ? Hand : PackageCheck"
        class="size-3.5 shrink-0"
        :class="mark.kind === 'handoff' ? 'text-warning' : 'text-primary'"
        aria-hidden="true"
      />
      <span class="shrink-0 font-medium">
        {{ mark.kind === 'handoff' ? t('chat.guiMarks.handoff') : t('chat.guiMarks.deliverable') }}
      </span>
      <span class="truncate text-muted-foreground">
        {{ markLabel(mark) }}
      </span>
      <span
        v-if="mark.status !== 'active'"
        class="shrink-0 rounded-md bg-muted px-1 text-[11px] text-muted-foreground"
      >
        {{ mark.status === 'closed' ? t('chat.guiMarks.closed') : t('chat.guiMarks.unknown') }}
      </span>
      <Button
        v-if="mark.status === 'active'"
        type="button"
        size="sm"
        variant="ghost"
        class="h-6 gap-1 px-1.5"
        :title="t('chat.guiMarks.openTitle')"
        :disabled="openingKey === mark.key"
        @click="openMark(mark)"
      >
        <ExternalLink
          class="size-3"
          aria-hidden="true"
        />
        {{ t('chat.guiMarks.open') }}
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        class="size-6 shrink-0"
        :title="t('chat.guiMarks.dismiss')"
        :aria-label="t('chat.guiMarks.dismiss')"
        @click="dismissMark(mark)"
      >
        <X
          class="size-3"
          aria-hidden="true"
        />
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
// Deliverable / handoff marks the agent placed on workspace browser tabs.
// They are persisted with the session (so a reload still shows them) and
// "Open" brings the tab to the front of the workspace browser, then shows
// the Desktop pane so the user actually sees it. A mark whose tab is gone is
// kept as "closed" instead of vanishing.
import { computed, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { Button, toast } from '@felinic/ui'
import { ExternalLink, Hand, PackageCheck, X } from 'lucide-vue-next'
import {
  getBotsByBotIdSessionsBySessionIdGuiMarks,
  postBotsByBotIdSessionsBySessionIdGuiMarksDismiss,
  postBotsByBotIdSessionsBySessionIdGuiMarksOpen,
} from '@memohai/sdk'
import type { HandlersGuiTabMark } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { installGuiMarksInvalidation } from './gui-tab-marks-query'

const props = defineProps<{
  botId?: string | null
  sessionId?: string | null
  visible?: boolean
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const workspaceTabs = useWorkspaceTabsStore()
const { streamingSessionIds } = storeToRefs(useChatStore())

// Refresh the list whenever a session's turn ends: that is when the agent's
// mark tool calls have been persisted.
installGuiMarksInvalidation(streamingSessionIds, queryCache)

const { data, refetch } = useQuery({
  key: () => ['gui-marks', props.botId ?? '', props.sessionId ?? ''],
  query: async ({ signal }) => {
    const { data } = await getBotsByBotIdSessionsBySessionIdGuiMarks({
      path: { bot_id: props.botId!, session_id: props.sessionId! },
      signal,
      throwOnError: true,
    })
    return data.marks ?? []
  },
  enabled: () => !!props.botId && !!props.sessionId && props.visible !== false,
  refetchOnWindowFocus: false,
})

const marks = computed<HandlersGuiTabMark[]>(() => data.value ?? [])
const openingKey = ref<string | null>(null)

function markLabel(mark: HandlersGuiTabMark): string {
  return mark.note || mark.title || mark.session_name || mark.url || mark.tab_id || ''
}

function markTitle(mark: HandlersGuiTabMark): string {
  return [mark.session_name, mark.title, mark.url].filter(Boolean).join(' · ')
}

async function openMark(mark: HandlersGuiTabMark) {
  if (!props.botId || !props.sessionId || !mark.key) return
  openingKey.value = mark.key
  try {
    await postBotsByBotIdSessionsBySessionIdGuiMarksOpen({
      path: { bot_id: props.botId, session_id: props.sessionId },
      body: { key: mark.key },
      throwOnError: true,
    })
    // Show the Desktop pane (focusing an existing one) so the activated tab
    // is actually visible to the user.
    workspaceTabs.openDisplay()
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('chat.guiMarks.openFailed')))
    await refetch()
  }
  finally {
    openingKey.value = null
  }
}

async function dismissMark(mark: HandlersGuiTabMark) {
  if (!props.botId || !props.sessionId || !mark.key) return
  try {
    await postBotsByBotIdSessionsBySessionIdGuiMarksDismiss({
      path: { bot_id: props.botId, session_id: props.sessionId },
      body: { key: mark.key },
      throwOnError: true,
    })
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('chat.guiMarks.openFailed')))
  }
  await refetch()
}
</script>
