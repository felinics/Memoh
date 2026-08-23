<script setup lang="ts">
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
  Button,
  Input,
  Label,
  Separator,
  Spinner,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@felinic/ui'
import { SquarePen, CircleHelp, Bot } from 'lucide-vue-next'
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { DeviceCodePanel, FieldStack, InlineLoadingRow, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getModels, getProviders, getProvidersByIdModels, getMemoryProviders, getAcpProfiles, putBotsByBotIdSettings, putModelsById, type AcpprofilePublicProfile } from '@memohai/sdk'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { storeToRefs } from 'pinia'
import { useOnboarding } from '@/composables/useOnboarding'
import { useACPOAuth } from '@/composables/useACPOAuth'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { defaultAclPreset } from '@/constants/acl-presets'
import { acpAgentDisplayName, acpAgentIcon, isClaudeCodeAgent, isCodexAgent, normalizeACPAgentID, withACPMetadata, type ACPForm } from '@/utils/acp'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'
import AvatarEditDialog from '@/pages/bots/components/avatar-edit-dialog.vue'
import BotCreateTerminal from '@/pages/bots/components/bot-create-terminal.vue'
import ModelSelect from '@/pages/bots/components/model-select.vue'
import AgentTypePill from '@/pages/bots/components/agent-type-pill.vue'
import AcpSetupPanel from '@/pages/bots/components/acp-setup-panel.vue'
import { MEMOH_AGENT_VALUE } from '@/pages/bots/components/agent-type'
import { useStepTransition, nextFrame } from '../useStepTransition'
import {
  clearOnboardingBotResult,
  markOnboardingOAuthComplete,
  readOnboardingOAuthResume,
  readOnboardingProviderId,
  skipOnboardingOAuth,
  writeOnboardingBotResult,
} from '../session'
import { mergeOnboardingModels } from './provider-setup'
import StepFrame from '../components/step-frame.vue'
import StepExitShell from '../components/step-exit-shell.vue'
import HintBox from '../components/hint-box.vue'
import FooterNav from '../components/footer-nav.vue'

const { t } = useI18n()
const { nextStep, prevStep } = useOnboarding()
const queryCache = useQueryCache()
const { visible, exiting, leave } = useStepTransition()

const submitting = ref(false)

const store = useBotCreateProgressStore()
const { lines: terminalLines, status: createStatus } = storeToRefs(store)

const oauthResume = readOnboardingOAuthResume()
const agentType = ref(oauthResume?.agentId ?? MEMOH_AGENT_VALUE)
const acpError = ref('')
const acpSetupPanelRef = ref<InstanceType<typeof AcpSetupPanel> | null>(null)
const acpSelection = ref(oauthResume
  ? { agentId: oauthResume.agentId, setupMode: 'oauth', managed: {} }
  : null)

const { data: acpProfileData } = useQuery({
  key: ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})
const acpProfiles = computed(() => acpProfileData.value?.items ?? [])
const selectedAcpProfile = computed<AcpprofilePublicProfile | null>(() => {
  if (agentType.value === MEMOH_AGENT_VALUE) return null
  return acpProfiles.value.find(profile => normalizeACPAgentID(profile.id) === agentType.value) ?? null
})
const onboardingProviderId = readOnboardingProviderId()
const acpAgentId = computed(() => acpSelection.value?.agentId ?? '')
const acpAgentName = computed(() => acpAgentDisplayName(acpAgentId.value))

// OAuth runs only after the bot + workspace exist, so it lives in a post-create
// phase of this step (bot-scoped endpoints have no user-scoped equivalent).
const oauthPhase = ref<'idle' | 'pending'>('idle')
const oauthVisible = ref(false)
const oauthBotId = ref('')
const oauthBotAgentId = ref(oauthResume?.botAgentId ?? '')
const oauthLeaving = ref(false)
const claudeCode = ref('')
const {
  codexStatus,
  authorizingCodexDevice,
  codexAuthorizing,
  codexDeviceSession,
  codexDevicePending,
  claudeStatus,
  authorizingClaude,
  exchangingClaude,
  claudeSessionId,
  loadCodexStatus,
  loadClaudeStatus,
  authorizeCodexDevice,
  cancelCodexDeviceAuthorization,
  authorizeClaude,
  exchangeClaude,
} = useACPOAuth(() => oauthBotId.value)

onMounted(() => {
  if (oauthResume) enterOAuthPhase(oauthResume.botId)
})

const form = reactive({
  display_name: '',
  avatar_url: '',
  chat_model_id: '',
  memory_provider_id: '',
})

const avatarDialogOpen = ref(false)
const avatarFallback = useAvatarInitials(() => form.display_name || '')

const { data: memoryProviderData } = useQuery({
  key: ['memory-providers'],
  query: async () => {
    const { data } = await getMemoryProviders({ throwOnError: true })
    return data
  },
})

const memoryProviders = computed(() => memoryProviderData.value ?? [])

watch(memoryProviders, (list) => {
  if (form.memory_provider_id) return
  const builtin = list.find(p => p.provider === 'builtin')
  if (builtin?.id) {
    form.memory_provider_id = builtin.id
  }
}, { immediate: true })

const { data: modelData } = useQuery({
  key: ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const {
  data: onboardingModelData,
  status: onboardingModelsStatus,
  isLoading: onboardingModelsLoading,
  refresh: refreshOnboardingModels,
} = useQuery({
  key: () => ['onboarding-provider-models', onboardingProviderId],
  query: async () => {
    if (!onboardingProviderId) return []
    const { data } = await getProvidersByIdModels({
      path: { id: onboardingProviderId },
      throwOnError: true,
    })
    return data ?? []
  },
})

const { data: providerData } = useQuery({
  key: ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

const models = computed(() => mergeOnboardingModels(
  modelData.value ?? [],
  onboardingModelData.value ?? [],
))
const providers = computed(() => providerData.value ?? [])

const canSubmit = computed(() => {
  if (!form.display_name.trim()) return false
  if (selectedAcpProfile.value || !onboardingProviderId) return true
  if (onboardingModelsStatus.value !== 'success') return false
  return !!form.chat_model_id
})

const isContainerSubmitting = computed(() => submitting.value)

const ctaLabel = computed(() => {
  if (isContainerSubmitting.value) return t('onboarding.bot.preparingEnvironment')
  return t('onboarding.next')
})

function buildMetadata(): Record<string, unknown> | undefined {
  let metadata: Record<string, unknown> = {}

  const selection = acpSelection.value
  if (selection) {
    const acpForm: ACPForm = {
      agents: {
        [selection.agentId]: {
          enabled: true,
          setup_mode: selection.setupMode,
          managed: selection.setupMode === 'api_key' ? selection.managed : {},
        },
      },
    }
    metadata = withACPMetadata(metadata, acpForm, acpProfiles.value)
  }

  return Object.keys(metadata).length > 0 ? metadata : undefined
}

async function handleSubmit() {
  if (!canSubmit.value || submitting.value) return

  if (selectedAcpProfile.value) {
    const panel = acpSetupPanelRef.value
    const missing = panel?.missingRequiredField()
    if (missing) {
      acpError.value = t('bots.agentCreate.requiredError', { field: missing.label || missing.id || '' })
      return
    }
    const selection = panel?.selection()
    if (!selection) return
    acpSelection.value = selection
  } else {
    acpSelection.value = null
  }

  clearOnboardingBotResult()
  submitting.value = true

  const selectedModel = models.value.find(model => model.id === form.chat_model_id)
  if (selectedModel?.id && !selectedModel.enable) {
    try {
      await putModelsById({
        path: { id: selectedModel.id },
        body: {
          model_id: selectedModel.model_id,
          name: selectedModel.name,
          provider_id: selectedModel.provider_id,
          type: selectedModel.type,
          config: selectedModel.config,
          enable: true,
        },
        throwOnError: true,
      })
      void queryCache.invalidateQueries({ key: ['models'] })
      void queryCache.invalidateQueries({ key: ['all-models'] })
    } catch {
      toast.error(t('common.saveFailed'))
      submitting.value = false
      return
    }
  }

  // The store drives the inline terminal reactively while we await completion.
  const createResult = await store.start({
    display_name: form.display_name.trim(),
    avatar_url: form.avatar_url.trim() || undefined,
    timezone: undefined,
    is_active: true,
    acl_preset: defaultAclPreset,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    metadata: buildMetadata() as any,
    wait_for_ready: true,
  }, {
    display: {
      display_name: form.display_name.trim(),
      avatar_url: form.avatar_url.trim() || undefined,
    },
    settings: {
      chat_model_id: form.chat_model_id || undefined,
      memory_provider_id: form.memory_provider_id || undefined,
    },
    ...(selectedAcpProfile.value && {
      agent: {
        name: selectedAcpProfile.value.display_name?.trim() || normalizeACPAgentID(selectedAcpProfile.value.id),
        provider: normalizeACPAgentID(selectedAcpProfile.value.id),
        deferDefault: acpSelection.value?.setupMode === 'oauth',
      },
    }),
  })
  submitting.value = false

  if (store.status === 'error') {
    toast.error(store.setupError ?? t('common.saveFailed'))
    store.reset()
    return
  }

  const botId = store.bot?.id
  if (!botId) {
    toast.error(store.setupError ?? t('common.saveFailed'))
    store.reset()
    return
  }

  if (acpSelection.value && (!createResult.agentApplied || !createResult.agentId)) {
    acpSelection.value = null
  }

  writeOnboardingBotResult({
    botId,
    modelConfigured: !!form.chat_model_id && createResult.settingsApplied,
    ...(acpSelection.value && {
      acp: {
        agentId: acpSelection.value.agentId,
        botAgentId: createResult.agentId!,
        oauthPending: acpSelection.value.setupMode === 'oauth',
      },
    }),
  })
  if (store.setupError) {
    toast.error(store.setupError)
  } else if (!createResult.settingsApplied) {
    toast.error(t('common.saveFailed'))
  }

  void queryCache.invalidateQueries({ key: getBotsQueryKey() })

  // OAuth runs after the workspace is ready so the managed token can be
  // written into the bot-scoped configuration.
  if (acpSelection.value?.setupMode === 'oauth') {
    store.reset()
    enterOAuthPhase(botId, createResult.agentId!)
    return
  }

  leave(nextStep)
  store.reset()
}

function enterOAuthPhase(botId: string, botAgentId = oauthBotAgentId.value) {
  oauthBotId.value = botId
  oauthBotAgentId.value = botAgentId
  oauthPhase.value = 'pending'
  claudeCode.value = ''
  oauthVisible.value = false
  nextFrame(() => {
    oauthVisible.value = true
  })
  if (isCodexAgent(acpAgentId.value)) void loadCodexStatus()
  if (isClaudeCodeAgent(acpAgentId.value)) void loadClaudeStatus()
}

const oauthAuthorized = computed(() => {
  if (isCodexAgent(acpAgentId.value)) {
    return !!codexStatus.value?.has_token ||
      codexDeviceSession.value?.status === 'success' ||
      !!codexDeviceSession.value?.has_token
  }
  if (isClaudeCodeAgent(acpAgentId.value)) return !!claudeStatus.value?.has_token
  return false
})

// 码还能用(或刚过期、可就地重取)时才展示面板;error/cancelled 只由状态行和
// toast 交代 —— 留一张废码在页面上只会误导。
const codexDevicePanel = computed(() => {
  const session = codexDeviceSession.value
  if (!session || session.bot_id !== oauthBotId.value || session.has_token) return null
  const usable = session.status === 'pending' || session.status === 'writing' || session.status === 'expired'
  return usable ? session : null
})

const codexDeviceExpired = computed(() =>
  !!codexDeviceSession.value &&
  codexDeviceSession.value.bot_id === oauthBotId.value &&
  codexDeviceSession.value.status === 'expired',
)

const oauthStatusText = computed(() => {
  if (oauthAuthorized.value) return t('onboarding.bot.acp.oauthAuthorized')
  if (codexDevicePending.value) return t('provider.oauth.status.pendingDevice')
  if (codexDeviceExpired.value) return t('onboarding.bot.acp.oauthDeviceExpired')
  return t('onboarding.bot.acp.oauthNotAuthorized')
})

const oauthStatusTextClass = computed(() =>
  oauthAuthorized.value || codexDevicePending.value
    ? 'text-muted-foreground'
    : 'text-destructive',
)

async function authorizeCodexDeviceFlow() {
  const ok = await authorizeCodexDevice()
  if (!ok) toast.error(t('onboarding.bot.acp.oauthExchangeFailed'))
}

async function cancelCodexDeviceFlow() {
  await cancelCodexDeviceAuthorization()
}

watch(() => codexDeviceSession.value?.status, (status, previousStatus) => {
  if (!status || status === previousStatus) return
  if (status === 'success') {
    toast.success(t('onboarding.bot.acp.oauthSuccess'))
    return
  }
  if (status === 'expired') {
    toast.error(t('onboarding.bot.acp.oauthDeviceExpired'))
    return
  }
  if (status === 'error') {
    toast.error(codexDeviceSession.value?.error || t('onboarding.bot.acp.oauthDeviceFailed'))
  }
})

async function authorizeClaudeFlow() {
  const ok = await authorizeClaude()
  if (ok === false) toast.error(t('onboarding.bot.acp.oauthExchangeFailed'))
}

async function exchangeClaudeFlow() {
  const ok = await exchangeClaude(claudeCode.value)
  if (ok) {
    claudeCode.value = ''
    toast.success(t('onboarding.bot.acp.oauthSuccess'))
  } else {
    toast.error(t('onboarding.bot.acp.oauthExchangeFailed'))
  }
}

async function continueFromOAuth() {
  if (!oauthAuthorized.value || oauthLeaving.value) return
  oauthLeaving.value = true
  try {
    await putBotsByBotIdSettings({
      path: { bot_id: oauthBotId.value },
      body: { default_bot_agent_id: oauthBotAgentId.value },
      throwOnError: true,
    })
    void queryCache.invalidateQueries({ key: ['bot-settings', oauthBotId.value] })
    markOnboardingOAuthComplete()
    leave(nextStep)
  } catch {
    oauthLeaving.value = false
    toast.error(t('common.saveFailed'))
  }
}

async function skipOAuth() {
  if (oauthLeaving.value) return
  oauthLeaving.value = true
  // User skipped OAuth — clear ACP selection so the completion step does not
  // redirect with ?acp=<agent>. Starting an ACP session without a token would
  // fail on the first prompt; the user can authorize later via bot settings.
  if (codexDevicePending.value) await cancelCodexDeviceAuthorization()
  skipOnboardingOAuth()
  leave(nextStep)
}
</script>

<template>
  <TooltipProvider :delay-duration="0">
    <StepExitShell :exiting="exiting">
      <StepFrame
        :title="t('onboarding.bot.title')"
        title-class="mb-8"
        :visible="visible"
      >
        <div
          v-show="oauthPhase !== 'pending'"
          class="min-h-0 flex-1 overflow-y-auto -mx-2 px-2 -my-1 py-1"
        >
          <form
            @submit.prevent="handleSubmit"
          >
            <div
              class="transition-all duration-[350ms] ease-out delay-[60ms]"
              :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
            >
              <div class="flex items-center gap-4">
                <div class="group/avatar relative size-16 shrink-0 rounded-full overflow-hidden cursor-pointer border border-border">
                  <Avatar class="size-16 rounded-full">
                    <AvatarImage
                      v-if="form.avatar_url?.trim()"
                      :src="form.avatar_url.trim()"
                      :alt="form.display_name"
                    />
                    <AvatarFallback class="text-xl text-muted-foreground">
                      <Bot
                        v-if="!form.display_name.trim()"
                        class="size-7"
                      />
                      <template v-else>
                        {{ avatarFallback }}
                      </template>
                    </AvatarFallback>
                  </Avatar>
                  <button
                    type="button"
                    class="absolute inset-0 flex items-center justify-center rounded-full bg-black/40 opacity-0 transition-opacity group-hover/avatar:opacity-100"
                    :title="$t('common.edit')"
                    :aria-label="$t('common.edit')"
                    @click="avatarDialogOpen = true"
                  >
                    <SquarePen class="size-6 text-white" />
                  </button>
                </div>
                <div class="flex-1 min-w-0">
                  <FieldStack>
                    <template #label>
                      <Label>
                        {{ $t('bots.displayName') }}
                        <span
                          v-if="!form.display_name.trim()"
                          class="text-destructive"
                        >*</span>
                      </Label>
                    </template>
                    <Input
                      v-model="form.display_name"
                      type="text"
                      :placeholder="$t('bots.displayNamePlaceholder')"
                    />
                  </FieldStack>
                </div>
              </div>
            </div>

            <div
              class="transition-all duration-[350ms] ease-out delay-[100ms]"
              :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
            >
              <Separator class="my-6" />
            </div>

            <div
              class="transition-all duration-[350ms] ease-out delay-[120ms]"
              :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
            >
              <AgentTypePill
                v-model="agentType"
                :profiles="acpProfiles"
                class="mb-3"
              />
              <template v-if="!selectedAcpProfile">
                <div class="mb-2 flex items-center gap-2">
                  <Label>{{ $t('bots.settings.chatModel') }}</Label>
                  <Tooltip>
                    <TooltipTrigger as-child>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        class="size-5 text-muted-foreground hover:text-foreground"
                      >
                        <CircleHelp class="size-3.5" />
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent class="max-w-80 text-left leading-relaxed">
                      {{ $t('onboarding.bot.model.hint') }}
                    </TooltipContent>
                  </Tooltip>
                </div>
                <InlineLoadingRow
                  v-if="onboardingProviderId && onboardingModelsStatus === 'pending'"
                  size="sm"
                >
                  {{ $t('onboarding.bot.model.loading') }}
                </InlineLoadingRow>
                <div
                  v-else-if="onboardingProviderId && onboardingModelsStatus === 'error'"
                  class="flex items-center justify-between gap-3"
                >
                  <p class="text-sm text-destructive">
                    {{ $t('onboarding.bot.model.loadFailed') }}
                  </p>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    :disabled="onboardingModelsLoading"
                    @click="refreshOnboardingModels()"
                  >
                    <Spinner v-if="onboardingModelsLoading" />
                    {{ $t('onboarding.bot.model.retry') }}
                  </Button>
                </div>
                <ModelSelect
                  v-else
                  v-model="form.chat_model_id"
                  :models="models"
                  :providers="providers"
                  model-type="chat"
                  :placeholder="$t('onboarding.bot.model.selectPlaceholder')"
                />
              </template>
              <AcpSetupPanel
                v-else
                ref="acpSetupPanelRef"
                v-model:error-message="acpError"
                :profile="selectedAcpProfile"
                :oauth-hint="t('onboarding.bot.acp.deferredHint')"
              />
            </div>

            <HintBox
              class="mt-6 transition-all duration-[350ms] ease-out delay-[200ms]"
              :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
            >
              {{ $t('bots.createBotWaitHint') }}
            </HintBox>
            <div
              v-if="(createStatus === 'creating' || createStatus === 'error') && terminalLines.length"
              class="mt-3 transition-all duration-[350ms] ease-out delay-[220ms]"
              :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
            >
              <BotCreateTerminal :lines="terminalLines" />
            </div>
          </form>
        </div>

        <div
          v-if="oauthPhase === 'pending'"
          class="min-h-0 flex-1 overflow-y-auto -mx-2 px-2 -my-1 py-1"
        >
          <div
            class="flex items-center gap-3 transition-all duration-[350ms] ease-out"
            :class="oauthVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <component
              :is="acpAgentIcon(acpAgentId, true)"
              class="size-7 shrink-0"
            />
            <div>
              <h3 class="text-lg font-semibold">
                {{ t('onboarding.bot.acp.oauthTitle', { agent: acpAgentName }) }}
              </h3>
              <p
                class="text-xs"
                :class="oauthStatusTextClass"
              >
                {{ oauthStatusText }}
              </p>
            </div>
          </div>

          <p
            class="mt-4 text-sm text-muted-foreground leading-relaxed transition-all duration-[350ms] ease-out delay-[60ms]"
            :class="oauthVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            {{ t('onboarding.bot.acp.oauthDescription') }}
          </p>

          <div
            v-if="isCodexAgent(acpAgentId)"
            class="mt-5 space-y-3 transition-all duration-[350ms] ease-out delay-[100ms]"
            :class="oauthVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <!-- 签发码之前只有"登录";码在手时这颗按钮的语义变成"放弃这次授权",
                 所以是替换而不是并排多一颗。 -->
            <div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
              <Button
                v-if="!codexDevicePending"
                type="button"
                variant="outline"
                :disabled="codexAuthorizing"
                :loading="authorizingCodexDevice"
                @click="authorizeCodexDeviceFlow"
              >
                {{ t('onboarding.bot.acp.oauthAuthorizeChatGPT') }}
              </Button>
              <Button
                v-else
                type="button"
                variant="ghost"
                @click="cancelCodexDeviceFlow"
              >
                {{ t('common.cancel') }}
              </Button>
            </div>

            <div
              v-if="codexDevicePanel"
              class="rounded-md bg-accent p-4"
            >
              <DeviceCodePanel
                :code="codexDevicePanel.user_code"
                :verification-uri="codexDevicePanel.verification_url"
                :expires-at="codexDevicePanel.expires_at ?? ''"
                :hint="t('onboarding.bot.acp.oauthDeviceHint')"
                :retry-loading="authorizingCodexDevice"
                :copy-and-open-label="t('deviceCode.copyAndOpen')"
                :retry-label="t('deviceCode.retry')"
                :expired-label="t('deviceCode.codeExpired')"
                :expires-in-label="(time: string) => t('deviceCode.expiresIn', { time })"
                :copy-failed-message="t('deviceCode.copyFailed')"
                @retry="authorizeCodexDeviceFlow"
              />
            </div>
          </div>

          <div
            v-else-if="isClaudeCodeAgent(acpAgentId)"
            class="mt-5 space-y-3 transition-all duration-[350ms] ease-out delay-[100ms]"
            :class="oauthVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <Button
              type="button"
              variant="outline"
              class="h-10"
              :loading="authorizingClaude"
              @click="authorizeClaudeFlow"
            >
              {{ t('onboarding.bot.acp.oauthAuthorizeClaude') }}
            </Button>

            <div
              v-if="claudeSessionId && !oauthAuthorized"
              class="space-y-2"
            >
              <p class="text-xs text-muted-foreground leading-relaxed">
                {{ t('onboarding.bot.acp.oauthCodeHint') }}
              </p>
              <div class="flex flex-col gap-2 sm:flex-row">
                <Input
                  v-model="claudeCode"
                  :placeholder="t('onboarding.bot.acp.oauthCodePlaceholder')"
                  class="h-10 min-w-0 flex-1"
                />
                <Button
                  type="button"
                  class="h-10 shrink-0"
                  :loading="exchangingClaude"
                  @click="exchangeClaudeFlow"
                >
                  {{ t('onboarding.bot.acp.oauthExchange') }}
                </Button>
              </div>
            </div>
          </div>
        </div>

        <FooterNav
          v-if="oauthPhase !== 'pending'"
          class="delay-[220ms]"
          :visible="visible"
          :prev-label="t('onboarding.prev')"
          @prev="leave(prevStep)"
        >
          <template #next>
            <!-- CTA carries its own Transition + Spinner for the label swap
                 (preparingEnvironment ↔ next) — the owner's default next
                 button can't express a keyed label transition, so this stays
                 local via the #next escape hatch. -->
            <button
              type="button"
              class="inline-flex h-[2.625rem] min-w-[180px] items-center justify-center gap-2 rounded-lg bg-primary px-5 font-normal text-primary-foreground shadow-none transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:opacity-60 disabled:cursor-not-allowed"
              :disabled="!canSubmit || submitting"
              @click="handleSubmit"
            >
              <Transition
                mode="out-in"
                enter-active-class="transition-all duration-[160ms] ease-out"
                enter-from-class="opacity-0 translate-y-1"
                enter-to-class="opacity-100 translate-y-0"
                leave-active-class="transition-all duration-[140ms] ease-in"
                leave-from-class="opacity-100 translate-y-0"
                leave-to-class="opacity-0 -translate-y-1"
              >
                <span
                  :key="ctaLabel"
                  class="inline-flex items-center gap-2"
                >
                  <Spinner v-if="isContainerSubmitting" />
                  {{ ctaLabel }}
                </span>
              </Transition>
            </button>
          </template>
        </FooterNav>

        <FooterNav
          v-else
          class="delay-[140ms]"
          :visible="oauthVisible"
          :prev-label="t('onboarding.bot.acp.oauthSkip')"
          :next-label="t('onboarding.next')"
          :next-disabled="!oauthAuthorized"
          @prev="skipOAuth"
          @next="continueFromOAuth"
        />

        <AvatarEditDialog
          v-model:open="avatarDialogOpen"
          v-model:avatar-url="form.avatar_url"
          :fallback-text="avatarFallback"
        />
      </StepFrame>
    </StepExitShell>
  </TooltipProvider>
</template>
