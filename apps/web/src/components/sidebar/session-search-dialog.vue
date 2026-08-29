<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogContent
      class="gap-0 overflow-hidden p-0 sm:max-w-lg"
      :show-close-button="false"
    >
      <DialogHeader class="sr-only">
        <DialogTitle>{{ t('chat.searchSessions') }}</DialogTitle>
        <DialogDescription>{{ t('chat.searchSessionPlaceholder') }}</DialogDescription>
      </DialogHeader>

      <div class="flex items-center gap-1 border-b border-border pl-3 pr-1.5">
        <Search class="size-4 shrink-0 text-muted-foreground" />
        <input
          ref="inputRef"
          v-model="query"
          class="h-11 min-w-0 flex-1 bg-transparent px-2 text-sm text-foreground outline-none placeholder:text-muted-foreground"
          :placeholder="t('chat.searchSessions')"
          @keydown.enter.prevent="selectFirst"
        >
        <DialogClose as-child>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Close"
          >
            <X />
          </Button>
        </DialogClose>
      </div>

      <ScrollArea class="max-h-[60vh]">
        <div class="flex flex-col gap-0.5 p-1.5">
          <button
            v-for="session in results"
            :key="session.id"
            type="button"
            class="flex h-9 items-center gap-2 rounded-md px-2.5 text-left hover:bg-[color:var(--ui-hover)]"
            @click="handleSelect(session)"
          >
            <component
              :is="iconOf(session)"
              class="size-3.5 shrink-0 text-muted-foreground"
            />
            <span class="min-w-0 flex-1 truncate text-xs text-foreground">
              {{ session.title || t('chat.untitledSession') }}
            </span>
          </button>

          <div
            v-if="!results.length"
            class="px-3 py-8 text-center text-xs text-muted-foreground"
          >
            {{ query.trim() ? t('chat.noSearchResults') : t('chat.noSessions') }}
          </div>
        </div>
      </ScrollArea>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch, type Component } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { Clock, GitBranch, MessageCircle, MessageSquare, Search, X } from 'lucide-vue-next'
import {
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  ScrollArea,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { sortByRecency } from '@/store/chat-list.utils'
import type { SessionSummary } from '@/composables/api/useChat'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()
const { sessions } = storeToRefs(chatStore)

const query = ref('')
const inputRef = ref<HTMLInputElement | null>(null)

const results = computed<SessionSummary[]>(() => {
  const q = query.value.trim().toLowerCase()
  const list = q
    ? sessions.value.filter(session =>
      (session.title ?? '').toLowerCase().includes(q)
      || (session.id ?? '').toLowerCase().includes(q),
    )
    : sessions.value
  return sortByRecency(list).slice(0, 50)
})

function iconOf(session: SessionSummary): Component {
  switch (session.type) {
    case 'schedule': return Clock
    case 'subagent': return GitBranch
    case 'discuss': return MessageCircle
    default: return MessageSquare
  }
}

function handleSelect(session: SessionSummary) {
  const title = (session.title ?? '').trim() || t('chat.untitledSession')
  // Subagent sessions live in the region right of the conversation (the same
  // split the live Desktop uses); everything else opens as a regular chat tab.
  if (session.type === 'subagent') {
    workspaceTabs.openSubagentSession({ sessionId: session.id, title })
  }
  else {
    workspaceTabs.openSessionChat({ sessionId: session.id, title })
  }
  emit('update:open', false)
}

function selectFirst() {
  const first = results.value[0]
  if (first) handleSelect(first)
}

watch(() => props.open, (isOpen) => {
  if (isOpen) {
    query.value = ''
    void nextTick(() => inputRef.value?.focus())
  }
})
</script>
