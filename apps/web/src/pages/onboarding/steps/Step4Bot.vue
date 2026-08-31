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
import { ref, reactive, computed, watch } from 'vue'
import { FieldStack, InlineLoadingRow, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getModels, getProviders, getProvidersByIdModels, getMemoryProviders, getAcpProfiles, putModelsById, type AcpprofilePublicProfile } from '@memohai/sdk'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { storeToRefs } from 'pinia'
import { useOnboarding } from '@/composables/useOnboarding'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { defaultAclPreset } from '@/constants/acl-presets'
import { acpAgentDisplayName, normalizeACPAgentID, withACPMetadata, type ACPForm } from '@/utils/acp'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, directBotAgentMetadata } from '@/utils/bot-agent'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'
import AvatarEditDialog from '@/pages/bots/components/avatar-edit-dialog.vue'
import BotCreateTerminal from '@/pages/bots/components/bot-create-terminal.vue'
import ModelSelect from '@/pages/bots/components/model-select.vue'
import AgentTypePill from '@/pages/bots/components/agent-type-pill.vue'
import AcpSetupPanel, { type AcpSetupSelection } from '@/pages/bots/components/acp-setup-panel.vue'
import { MEMOH_AGENT_VALUE } from '@/pages/bots/components/agent-type'
import { useStepTransition } from '../useStepTransition'
import {
  clearOnboardingBotResult,
  readOnboardingProviderId,
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

const agentType = ref(MEMOH_AGENT_VALUE)
const acpError = ref('')
const acpSetupPanelRef = ref<InstanceType<typeof AcpSetupPanel> | null>(null)
const acpSelection = ref<AcpSetupSelection | null>(null)

const { data: acpProfileData } = useQuery({
  key: ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})
const acpProfiles = computed(() => acpProfileData.value?.items ?? [])
// codex / claude-code are direct runtimes with no ACP profile: no setup
// fields here, credentials are configured on the Bot's settings page.
const selectedDirectRuntime = computed(() => {
  const value = agentType.value
  return value === BOT_AGENT_RUNTIME_CODEX || value === BOT_AGENT_RUNTIME_CLAUDE_CODE ? value : ''
})
const selectedAcpProfile = computed<AcpprofilePublicProfile | null>(() => {
  if (agentType.value === MEMOH_AGENT_VALUE || selectedDirectRuntime.value) return null
  return acpProfiles.value.find(profile => normalizeACPAgentID(profile.id) === agentType.value) ?? null
})
const onboardingProviderId = readOnboardingProviderId()

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
  // Agent runtimes resolve their own model; only a Memoh-model bot needs one.
  if (selectedAcpProfile.value || selectedDirectRuntime.value || !onboardingProviderId) return true
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

  if (selectedDirectRuntime.value) return undefined

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
    ...(selectedDirectRuntime.value
      ? {
          agent: {
            name: acpAgentDisplayName(selectedDirectRuntime.value, selectedDirectRuntime.value),
            provider: selectedDirectRuntime.value,
            metadata: directBotAgentMetadata(selectedDirectRuntime.value),
          },
        }
      : selectedAcpProfile.value && {
          agent: {
            name: selectedAcpProfile.value.display_name?.trim() || normalizeACPAgentID(selectedAcpProfile.value.id),
            provider: normalizeACPAgentID(selectedAcpProfile.value.id),
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

  const stagedAgentId = selectedDirectRuntime.value || acpSelection.value?.agentId || ''
  writeOnboardingBotResult({
    botId,
    modelConfigured: !!form.chat_model_id && createResult.settingsApplied,
    ...(stagedAgentId && createResult.agentId && {
      agent: {
        agentId: stagedAgentId,
        botAgentId: createResult.agentId,
      },
    }),
  })
  if (store.setupError) {
    toast.error(store.setupError)
  } else if (!createResult.settingsApplied) {
    toast.error(t('common.saveFailed'))
  }

  void queryCache.invalidateQueries({ key: getBotsQueryKey() })

  leave(nextStep)
  store.reset()
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
        <div class="min-h-0 flex-1 overflow-y-auto -mx-2 px-2 -my-1 py-1">
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
              <p
                v-if="selectedDirectRuntime"
                class="text-sm text-muted-foreground"
              >
                {{ $t('bots.agentCreate.directSetupHint') }}
              </p>
              <template v-else-if="!selectedAcpProfile">
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

        <FooterNav
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

        <AvatarEditDialog
          v-model:open="avatarDialogOpen"
          v-model:avatar-url="form.avatar_url"
          :fallback-text="avatarFallback"
        />
      </StepFrame>
    </StepExitShell>
  </TooltipProvider>
</template>
