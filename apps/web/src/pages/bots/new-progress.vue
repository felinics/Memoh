<template>
  <section class="relative mx-auto flex min-h-[60vh] max-w-2xl flex-col justify-center px-4 py-10 lg:px-6">
    <div class="flex items-center gap-4">
      <Avatar class="size-12 rounded-full">
        <AvatarImage
          v-if="display?.avatar_url?.trim()"
          :src="display.avatar_url.trim()"
          :alt="displayName"
        />
        <AvatarFallback class="text-base">
          {{ avatarFallback }}
        </AvatarFallback>
      </Avatar>
      <div class="min-w-0">
        <h1 class="truncate text-lg font-semibold">
          {{ $t('bots.create.terminalTitle', { name: displayName }) }}
        </h1>
        <p class="mt-0.5 text-xs text-muted-foreground">
          {{ status === 'error' ? $t('bots.create.failedSubtitle') : $t('bots.create.terminalSubtitle') }}
        </p>
      </div>
    </div>

    <BotCreateTerminal
      :lines="lines"
      class="mt-6"
    />

    <div
      v-if="needsAuthorization && bot?.id && createdAgent"
      class="mt-6 space-y-6"
    >
      <CreatedAgentSetup
        :key="bot.id + createdAgent.runtime"
        :bot-id="bot.id"
        :agent-id="createdAgent.id ?? ''"
        :runtime="createdAgent.runtime ?? ''"
        @status="authorization = $event"
      />
      <div class="flex justify-end">
        <Button
          :disabled="!authorization.authorized || authorization.busy"
          @click="goToBot"
        >
          {{ $t('onboarding.next') }}
        </Button>
      </div>
    </div>

    <div
      v-if="status === 'error'"
      class="mt-6 flex justify-end gap-3"
    >
      <Button
        variant="outline"
        @click="handleBack"
      >
        {{ $t('bots.create.back') }}
      </Button>
      <Button
        :disabled="status !== 'error'"
        @click="handleRetry"
      >
        {{ $t('bots.create.retry') }}
      </Button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { Avatar, AvatarImage, AvatarFallback, Button, toast } from '@felinic/ui'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'
import { readCreatedAgentSession, writeCreatedAgentSession } from './created-agent-session'
import CreatedAgentSetup from './components/created-agent-setup.vue'
import BotCreateTerminal from './components/bot-create-terminal.vue'

const router = useRouter()
const { t } = useI18n()
const queryCache = useQueryCache()
const store = useBotCreateProgressStore()
const { status, lines, display, bot, setupError, createdAgent } = storeToRefs(store)
if (store.status === 'idle') {
  const saved = readCreatedAgentSession()
  if (saved) {
    store.bot = { id: saved.botId, name: saved.botName }
    store.display = { display_name: saved.displayName }
    store.createdAgent = { id: saved.agentId, runtime: saved.runtime }
    store.setupError = saved.setupError
    store.status = 'ready'
  }
}

const authorization = ref({ authorized: false, busy: false })
const needsAuthorization = computed(() => status.value === 'ready' && ['codex', 'claude-code'].includes(createdAgent.value?.runtime ?? ''))

const displayName = computed(() => display.value?.display_name || '')
const avatarFallback = useAvatarInitials(() => displayName.value)

let navigated = false
let readyRedirectTimer: number | null = null

function clearReadyRedirectTimer() {
  if (readyRedirectTimer === null) return
  window.clearTimeout(readyRedirectTimer)
  readyRedirectTimer = null
}

function clearTimers() {
  clearReadyRedirectTimer()
}

async function goToBot() {
  if (navigated) return
  clearReadyRedirectTimer()
  navigated = true
  const target = bot.value?.name ?? bot.value?.id
  if (setupError.value) {
    toast.error(setupError.value)
  } else {
    toast.success(t('bots.createBotSuccess'))
  }
  void queryCache.invalidateQueries({ key: getBotsQueryKey() })
  try {
    await (target
      ? router.replace({ name: 'bot-detail', params: { botName: target } })
      : router.replace({ name: 'bots' }))
  } catch {
    // Navigation aborted/failed — keep the page as-is rather than blanking it.
    return
  }
  // Reset only after navigation has committed, so the still-mounted terminal
  // never flashes empty before the view swaps.
  writeCreatedAgentSession(null)
  store.reset()
}

function scheduleReadyRedirect() {
  clearReadyRedirectTimer()
  if (needsAuthorization.value) return
  // Brief pause so the final "ready" line is visible before redirecting.
  readyRedirectTimer = window.setTimeout(() => {
    readyRedirectTimer = null
    void goToBot()
  }, 700)
}

function guardLiveProgress() {
  if (status.value === 'idle') {
    router.replace({ name: 'bot-new' })
    return
  }
  if (status.value === 'ready' && !navigated) {
    scheduleReadyRedirect()
  }
}

watch(
  status,
  (value) => {
    if (value === 'ready') {
      const runtime = createdAgent.value?.runtime
      if (bot.value?.id && (runtime === 'codex' || runtime === 'claude-code')) {
        writeCreatedAgentSession({ botId: bot.value.id, botName: bot.value.name ?? '',
          displayName: displayName.value, agentId: createdAgent.value?.id ?? '', runtime,
          setupError: setupError.value })
      }
      scheduleReadyRedirect()
    } else {
      if (value === 'creating') writeCreatedAgentSession(null)
      clearReadyRedirectTimer()
    }
  },
  { immediate: true },
)

onMounted(() => {
  // Direct navigation or a refresh drops the in-memory stream, so send the user
  // back to the form rather than showing an empty terminal.
  guardLiveProgress()
})

onActivated(guardLiveProgress)
onDeactivated(clearTimers)
onBeforeUnmount(clearTimers)

function handleRetry() {
  navigated = false
  void store.retry()
}

function handleBack() {
  store.reset()
  router.replace({ name: 'bot-new' })
}
</script>
