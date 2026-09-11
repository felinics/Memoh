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
          {{ (status === 'error' || status === 'setup-error') ? $t('bots.create.failedSubtitle') : $t('bots.create.terminalSubtitle') }}
        </p>
      </div>
    </div>

    <BotCreateTerminal
      :lines="lines"
      class="mt-6"
    />

    <div
      v-if="status === 'error' || status === 'setup-error'"
      class="mt-6 flex justify-end gap-3"
    >
      <Button
        v-if="status === 'error'"
        variant="outline"
        @click="handleBack"
      >
        {{ $t('bots.create.back') }}
      </Button>
      <Button
        @click="handleRetry"
      >
        {{ $t('bots.create.retry') }}
      </Button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { Avatar, AvatarImage, AvatarFallback, Button, toast } from '@felinic/ui'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'
import { readCreatedAgentSession, writeCreatedAgentSession } from './created-agent-session'
import { useOnboarding } from '@/composables/useOnboarding'
import { readOnboardingBotResult, writeOnboardingBotResult } from '@/pages/onboarding/session'
import BotCreateTerminal from './components/bot-create-terminal.vue'

const props = defineProps<{ onboarding?: boolean }>()
const onboardingSteps = useOnboarding()
const router = useRouter()
const { t } = useI18n()
const queryCache = useQueryCache()
const store = useBotCreateProgressStore()
const { status, lines, display, bot, setupError, createdAgent, authorizationId, modelConfigured } = storeToRefs(store)
if (store.status === 'idle') {
  const saved = readCreatedAgentSession(props.onboarding)
  const previous = props.onboarding ? readOnboardingBotResult() : null
  if (saved) store.restore(saved, props.onboarding)
  else if (previous?.agent && ['codex', 'claude-code'].includes(previous.agent.agentId)) {
    store.restore({ botId: previous.botId, botName: '', displayName: '', agentId: previous.agent.botAgentId,
      runtime: previous.agent.agentId as 'codex' | 'claude-code', authorizationId: previous.agent.authorizationId, setupError: null }, true)
  } else if (previous) {
    store.bot = { id: previous.botId }
    store.modelConfigured = previous.modelConfigured
    store.status = 'ready'
  }
}

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
  if (props.onboarding && bot.value?.id) {
    const runtime = createdAgent.value?.runtime
    writeOnboardingBotResult({ botId: bot.value.id, modelConfigured: modelConfigured.value,
      ...((runtime === 'codex' || runtime === 'claude-code') && { agent: {
        agentId: runtime, botAgentId: createdAgent.value?.id ?? '', authorizationId: authorizationId.value,
      } }),
    })
    void queryCache.invalidateQueries({ key: getBotsQueryKey() })
    onboardingSteps.nextStep()
    store.reset()
    return
  }
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
  // Brief pause so the final "ready" line is visible before redirecting.
  readyRedirectTimer = window.setTimeout(() => {
    readyRedirectTimer = null
    void goToBot()
  }, 700)
}

function guardLiveProgress() {
  if (status.value === 'idle') {
    if (props.onboarding) onboardingSteps.prevStep()
    else router.replace({ name: 'bot-new' })
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
      scheduleReadyRedirect()
    } else {
      clearReadyRedirectTimer()
    }
  },
  { immediate: true },
)

onMounted(() => {
  // Restored Agent setup resumes on the same Bot; an empty route returns to the form.
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
  if (props.onboarding) onboardingSteps.prevStep()
  else router.replace({ name: 'bot-new' })
}
</script>
