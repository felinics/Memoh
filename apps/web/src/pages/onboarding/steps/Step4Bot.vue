<script setup lang="ts">
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
  Button,
  InputGroup,
  InputGroupInput,
  InputGroupAddon,
  InputGroupButton,
  Label,
  Separator,
  SettingsSection,
  Spinner,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@felinic/ui'
import { SquarePen, CircleHelp, Bot, Dices } from 'lucide-vue-next'
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { FieldStack, InlineLoadingRow, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getModels, getProviders, getProvidersByIdModels, getMemoryProviders, putModelsById } from '@memohai/sdk'
import { useOnboarding } from '@/composables/useOnboarding'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { defaultAclPreset } from '@/constants/acl-presets'
import { randomCatName } from '@/constants/bot-name-presets'
import { externalAgentDisplayName } from '@/utils/external-agent'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, directBotAgentMetadata } from '@/utils/bot-agent'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'
import AvatarEditDialog from '@/pages/bots/components/avatar-edit-dialog.vue'
import ModelSelect from '@/pages/bots/components/model-select.vue'
import AgentTypePill from '@/pages/bots/components/agent-type-pill.vue'
import AgentAuthorization from '@/pages/bots/components/agent-authorization.vue'
import { readAgentAuthorizationDraft } from '@/composables/useAgentAuthorization'
import { MEMOH_AGENT_VALUE } from '@/pages/bots/components/agent-type'
import { useStepTransition } from '../useStepTransition'
import {
  clearOnboardingBotResult,
  readOnboardingProviderId,
  readOnboardingBotResult,
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
onMounted(() => { if (readOnboardingBotResult()) nextStep() })

const store = useBotCreateProgressStore()

const authorizationKey = 'memoh:onboarding:agent-authorization'
const agentType = ref(readAgentAuthorizationDraft(authorizationKey)?.runtime ?? MEMOH_AGENT_VALUE)
const account = ref({ ready: false, busy: false, id: '', auth: '' })
const accountPanel = ref<InstanceType<typeof AgentAuthorization> | null>(null)
const selectedDirectRuntime = computed(() => {
  const value = agentType.value
  return value === BOT_AGENT_RUNTIME_CODEX || value === BOT_AGENT_RUNTIME_CLAUDE_CODE ? value : ''
})
const onboardingProviderId = readOnboardingProviderId()

const form = reactive({
  // Prefill with a random cat-name preset so naming isn't a blocker; the dice
  // button re-rolls. Runs once per mount — a user-cleared field stays empty.
  display_name: randomCatName(),
  avatar_url: '',
  chat_model_id: '',
  memory_provider_id: '',
})

const avatarDialogOpen = ref(false)
const avatarFallback = useAvatarInitials(() => form.display_name || '')

function rollRandomName() {
  form.display_name = randomCatName(form.display_name)
}

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
  if (selectedDirectRuntime.value && (!account.value.ready || account.value.busy)) return false
  if (!form.display_name.trim()) return false
  // Agent runtimes resolve their own model; only a Memoh-model bot needs one.
  if (selectedDirectRuntime.value || !onboardingProviderId) return true
  if (onboardingModelsStatus.value !== 'success') return false
  return !!form.chat_model_id
})

const isContainerSubmitting = computed(() => submitting.value)

const ctaLabel = computed(() => {
  if (isContainerSubmitting.value) return t('onboarding.bot.preparingEnvironment')
  return t('onboarding.next')
})

async function handleSubmit() {
  if (!canSubmit.value || submitting.value) return

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

  // The dedicated progress step owns the remaining creation and installation.
  accountPanel.value?.handoff()
  void store.start({
    display_name: form.display_name.trim(),
    avatar_url: form.avatar_url.trim() || undefined,
    timezone: undefined,
    is_active: true,
    acl_preset: defaultAclPreset,
    wait_for_ready: true,
  }, {
    onboarding: true,
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
            name: externalAgentDisplayName(selectedDirectRuntime.value, selectedDirectRuntime.value),
            provider: selectedDirectRuntime.value,
            metadata: { ...directBotAgentMetadata(selectedDirectRuntime.value), auth: account.value.auth },
            authorizationId: account.value.id,
          },
        }
      : {}),
  })
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
        <div class="min-h-0 flex-1 overflow-y-auto -mx-2 px-2 -my-1 py-1">
          <form
            :inert="submitting"
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
                    <InputGroup class="overflow-hidden">
                      <InputGroupInput
                        v-model="form.display_name"
                        type="text"
                        :placeholder="$t('bots.displayNamePlaceholder')"
                      />
                      <InputGroupAddon align="inline-end">
                        <InputGroupButton
                          size="icon-xs"
                          variant="quiet"
                          type="button"
                          :aria-label="$t('onboarding.bot.randomName')"
                          :title="$t('onboarding.bot.randomName')"
                          @click="rollRandomName"
                        >
                          <Dices />
                        </InputGroupButton>
                      </InputGroupAddon>
                    </InputGroup>
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
                :profiles="[]"
                class="mb-3"
              />
              <SettingsSection v-if="selectedDirectRuntime">
                <AgentAuthorization
                  :key="selectedDirectRuntime"
                  ref="accountPanel"
                  :runtime="selectedDirectRuntime"
                  :storage-key="authorizationKey"
                  @status="account = $event"
                />
              </SettingsSection>
              <template v-if="agentType === MEMOH_AGENT_VALUE">
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
            </div>

            <HintBox
              class="mt-6 transition-all duration-[350ms] ease-out delay-[200ms]"
              :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
            >
              {{ $t('bots.createBotWaitHint') }}
            </HintBox>
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
