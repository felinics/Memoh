<template>
  <div class="space-y-1.5">
    <div
      v-if="agents.length"
      class="space-y-1"
    >
      <button
        v-for="agent in agents"
        :key="agent.agent_id || agent.session_id"
        class="flex items-center gap-1.5 text-xs w-full text-left rounded-sm px-1 py-0.5 transition-colors"
        :class="agent.session_id ? 'cursor-pointer hover:bg-accent' : ''"
        @click="agent.session_id ? navigateToSession(agent.session_id) : undefined"
      >
        <CircleDot
          class="size-3 shrink-0"
          :class="statusClass(agent.status)"
        />
        <span class="font-mono text-foreground shrink-0">{{ agent.agent_id }}</span>
        <span class="text-muted-foreground truncate">{{ agent.status }}</span>
        <ExternalLink
          v-if="agent.session_id"
          class="size-3 text-muted-foreground/50 shrink-0 ml-auto"
        />
      </button>
    </div>

    <div
      v-else-if="result"
      class="space-y-1"
    >
      <button
        class="flex items-center gap-1.5 text-xs w-full text-left rounded-sm px-1 py-0.5 transition-colors"
        :class="result.session_id ? 'cursor-pointer hover:bg-accent' : ''"
        @click="result.session_id ? navigateToSession(result.session_id) : undefined"
      >
        <CircleDot
          class="size-3 shrink-0"
          :class="statusClass(result.status)"
        />
        <span
          v-if="result.agent_id"
          class="font-mono text-foreground shrink-0"
        >{{ result.agent_id }}</span>
        <span
          v-if="result.status"
          class="text-muted-foreground truncate"
        >{{ result.status }}</span>
        <ExternalLink
          v-if="result.session_id"
          class="size-3 text-muted-foreground/50 shrink-0 ml-auto"
        />
      </button>

      <div class="space-y-1 pt-1 border-t border-border/50">
        <p
          v-if="result.task_id"
          class="text-xs text-muted-foreground"
        >
          <span class="font-mono">{{ result.task_id }}</span>
        </p>
        <PreviewBox v-if="result.text">
          {{ result.text }}
        </PreviewBox>
        <p
          v-if="result.error"
          class="text-xs text-destructive"
        >
          {{ result.error }}
        </p>
      </div>
    </div>

    <EmptyRow v-else>
      {{ t('chat.tools.detail.noTasks') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CircleDot, ExternalLink } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useChatStore } from '@/store/chat-list'
import type { ToolCallBlock } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import EmptyRow from './tool-detail/empty-row.vue'
import PreviewBox from './tool-detail/preview-box.vue'

interface AgentResult {
  agent_id?: string
  session_id?: string
  task_id?: string
  status?: string
  text?: string
  error?: string
}

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()

function resolveResult(): Record<string, unknown> | null {
  if (!props.block.result) return null
  const result = props.block.result as Record<string, unknown>
  return (result.structuredContent as Record<string, unknown>) ?? result
}

const result = computed<AgentResult | null>(() => {
  const r = resolveResult()
  if (!r) return null
  if (Array.isArray(r.agents)) return null
  return r as AgentResult
})

const agents = computed<AgentResult[]>(() => {
  const r = resolveResult()
  if (!r || !Array.isArray(r.agents)) return []
  return r.agents as AgentResult[]
})

function statusClass(status?: string) {
  switch (status) {
    case 'completed':
      return 'text-success'
    case 'failed':
    case 'killed':
      return 'text-destructive'
    case 'running':
      return 'text-primary'
    case 'queued':
      return 'text-muted-foreground'
    default:
      return 'text-muted-foreground/70'
  }
}

function navigateToSession(sessionId: string) {
  if (!sessionId || !chatStore.currentBotId) return
  // Open (or focus) the spawned session in the subagent region to the right of
  // the conversation; activation selects it.
  workspaceTabs.openSubagentSession({ sessionId })
}
</script>
