<template>
  <div
    class="flex items-start gap-2 rounded-md border border-warning-border bg-warning-soft px-3 py-2 text-body text-warning-foreground"
  >
    <TriangleAlert class="mt-0.5 size-3.5 shrink-0" />
    <div class="min-w-0 flex-1 space-y-1">
      <p class="whitespace-pre-wrap break-words">
        {{ text }}
      </p>

      <!-- Live state of the background install the Server started. Rendered
           only when this session has seen the task's events; an install kicked
           off elsewhere leaves just the text and the link. -->
      <div
        v-if="task"
        class="flex min-w-0 items-center gap-1.5 text-muted-foreground"
      >
        <Spinner
          v-if="taskActive"
          class="size-3.5 shrink-0"
        />
        <CircleCheck
          v-else-if="taskStatus === 'completed'"
          class="size-3.5 shrink-0 text-success-foreground"
        />
        <CircleX
          v-else-if="taskStatus !== 'unknown'"
          class="size-3.5 shrink-0 text-destructive"
        />
        <TriangleAlert
          v-else
          class="size-3.5 shrink-0 text-warning-foreground"
        />
        <span class="shrink-0">{{ taskLabel }}</span>
        <BgTaskLiveStatus
          v-if="taskActive"
          :task="task"
          class="min-w-0 flex-1"
        />
      </div>

      <TextButton
        v-if="canManage && (botName || botId)"
        class="-ml-1.5"
        @click="openDependencies"
      >
        {{ t('chat.externalAgent.dependencyOpen') }}
      </TextButton>
    </div>
  </div>
</template>

<script setup lang="ts">
// Chat-side rendering of `agent_dependency_missing`. The text is
// rebuilt from the block's args through the same i18n key the Server names, so
// it follows the viewer's locale; the Server's content is the fallback when
// args are absent. The one action opens the bot's Dependencies tab — the
// install requires a manager to review and confirm it there.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { CircleCheck, CircleX, TriangleAlert } from 'lucide-vue-next'
import { Spinner, TextButton } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import type { ErrorBlock } from '@/store/chat-list'
import { isBackgroundTaskActive, normalizeBackgroundStatus } from '@/store/chat/background-tasks'
import { hasBotPermission } from '@/utils/bot-permissions'
import BgTaskLiveStatus from './bg-task-live-status.vue'
import { dependencyInstallationInProgress, dependencyMissingArgs } from './dependency-missing'

const props = defineProps<{
  block: ErrorBlock
  botId?: string
  sessionId?: string
  /** The bot's route name; falls back to the id, which the route also accepts. */
  botName?: string
}>()

const { t } = useI18n()
const router = useRouter()
const chatStore = useChatStore()

const args = computed(() => dependencyMissingArgs(props.block))

const canManage = computed(() => hasBotPermission(
  chatStore.bots.find(bot => bot.id === props.botId)?.current_user_permissions,
  'manage',
))

const text = computed(() => {
  const values = args.value
  if (values.dep_id) {
    return t(dependencyInstallationInProgress(values)
      ? 'chat.externalAgent.dependencyMissingInstalling'
      : 'chat.externalAgent.dependencyMissing', values)
  }
  return props.block.content
})

const task = computed(() => {
  const taskId = args.value.install_task_id ?? ''
  return taskId ? chatStore.backgroundTaskFor(taskId) : undefined
})
const taskStatus = computed(() => normalizeBackgroundStatus(task.value?.status, task.value?.event))
const taskActive = computed(() => isBackgroundTaskActive(task.value))
const taskLabel = computed(() => {
  if (taskStatus.value === 'unknown') return t('bots.dependencies.progress.unknownHint')
  if (taskActive.value) return t('chat.externalAgent.dependencyMissingProgress')
  if (taskStatus.value === 'completed') return t('chat.externalAgent.dependencyMissingDone')
  return t('chat.externalAgent.dependencyMissingFailed')
})

function openDependencies() {
  const botName = props.botName?.trim() || props.botId?.trim()
  if (!botName) return
  void router.push({
    name: 'bot-detail',
    params: { botName },
    query: { tab: 'apps', dependency_id: args.value.dep_id, session_id: props.sessionId || undefined },
  }).catch(() => {})
}
</script>
