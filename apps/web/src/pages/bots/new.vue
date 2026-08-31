<template>
  <section class="relative mx-auto w-full max-w-2xl px-4 pt-2 pb-10 md:pt-4 md:pb-12 lg:px-6">
    <Tabs
      v-model="mode"
      class="mb-6"
    >
      <TabsList class="grid w-full grid-cols-2">
        <TabsTrigger value="create">
          {{ $t('bots.backup.createMode') }}
        </TabsTrigger>
        <TabsTrigger value="import">
          {{ $t('bots.backup.importMode') }}
        </TabsTrigger>
      </TabsList>
    </Tabs>

    <!-- Import from backup -->
    <div v-if="mode === 'import'">
      <p class="text-xs text-muted-foreground mb-4">
        {{ $t('bots.backup.importDescription') }}
      </p>
      <BotImportPanel
        show-cancel
        @imported="handleImported"
        @cancel="router.back()"
      />
    </div>

    <form
      v-else
      :aria-busy="isCreateFlowBlocked"
      :class="{ 'pointer-events-none select-none opacity-60': isCreateFlowBlocked }"
      @submit.prevent="handleSubmit"
    >
      <!-- Basic Info -->
      <div>
        <h3 class="text-sm font-medium mb-4">
          {{ $t('bots.steps.basicInfo') }}
        </h3>
        <div class="flex items-start gap-4">
          <div class="group/avatar relative size-16 shrink-0 rounded-full overflow-hidden cursor-pointer">
            <Avatar class="size-16 rounded-full">
              <AvatarImage
                v-if="form.avatar_url?.trim()"
                :src="form.avatar_url.trim()"
                :alt="form.display_name"
              />
              <AvatarFallback class="text-xl">
                {{ avatarFallback }}
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
            <Label class="mb-2">
              {{ $t('bots.displayName') }}
              <span class="text-destructive">*</span>
            </Label>
            <Input
              v-model="form.display_name"
              type="text"
              :placeholder="$t('bots.displayNamePlaceholder')"
            />
          </div>
        </div>

        <div class="mt-4">
          <Label class="mb-2">
            {{ $t('bots.name') }}
            <span class="text-destructive">*</span>
          </Label>
          <div class="relative">
            <Input
              v-model="form.name"
              type="text"
              autocapitalize="off"
              autocomplete="off"
              spellcheck="false"
              class="pr-9"
              :placeholder="$t('bots.namePlaceholder')"
              @input="handleNameInput"
            />
            <span class="absolute right-3 top-1/2 -translate-y-1/2">
              <LoaderCircle
                v-if="nameStatus === 'checking'"
                class="size-4 animate-spin text-muted-foreground"
              />
              <Check
                v-else-if="nameStatus === 'available'"
                class="size-4 text-success-foreground"
              />
              <X
                v-else-if="nameStatus === 'taken' || nameStatus === 'invalid' || nameStatus === 'reserved'"
                class="size-4 text-destructive"
              />
            </span>
          </div>
          <p
            class="mt-1 text-xs"
            :class="nameStatus === 'available'
              ? 'text-success-foreground'
              : (nameStatus === 'taken' || nameStatus === 'invalid' || nameStatus === 'reserved')
                ? 'text-destructive'
                : 'text-muted-foreground'"
          >
            {{ nameStatusMessage || $t('bots.nameHint') }}
          </p>
        </div>
      </div>

      <Separator class="my-6" />

      <!-- Security Policy -->
      <div>
        <h3 class="text-sm font-medium mb-4">
          {{ $t('bots.steps.security') }}
        </h3>
        <div class="flex flex-col gap-3">
          <div class="mb-2 flex items-center gap-2">
            <Label>
              {{ $t('bots.aclPreset') }}
              <span class="text-destructive">*</span>
            </Label>
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
                {{ $t('bots.aclPresetHelp') }}
              </TooltipContent>
            </Tooltip>
          </div>
          <Select v-model="form.acl_preset">
            <SelectTrigger class="w-full">
              <SelectValue :placeholder="$t('bots.aclPreset')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="preset in aclPresetOptions"
                :key="preset.value"
                :value="preset.value"
              >
                {{ $t(preset.titleKey) }}
              </SelectItem>
            </SelectContent>
          </Select>
          <p
            v-if="aclDescription"
            class="text-xs text-muted-foreground"
          >
            {{ aclDescription }}
          </p>
        </div>
      </div>

      <Separator class="my-6" />

      <!-- Model / Agent -->
      <div>
        <h3 class="text-sm font-medium mb-4">
          {{ selectedAcpProfile ? $t('bots.steps.agent') : $t('bots.steps.model') }}
        </h3>
        <p class="text-xs text-muted-foreground mb-3">
          {{ selectedAcpProfile ? $t('bots.steps.agentDesc') : $t('bots.steps.modelDesc') }}
        </p>
        <!-- Agent-kind chooser (ACP 提权): hides itself when the server
             publishes no hosted agents — the form then matches the pre-chooser
             behavior exactly. -->
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
          <Label class="mb-2">{{ $t('bots.settings.chatModel') }}</Label>
          <ModelSelect
            v-model="form.chat_model_id"
            v-model:reasoning-effort="form.reasoning_effort"
            :models="models"
            :providers="providers"
            model-type="chat"
            :placeholder="$t('common.none')"
            show-reasoning
          />
        </template>
        <AcpSetupPanel
          v-else
          ref="acpSetupPanelRef"
          v-model:error-message="acpError"
          :profile="selectedAcpProfile"
          :oauth-hint="$t('bots.agentCreate.oauthSettingsHint')"
        />
      </div>

      <Separator class="my-6" />

      <!-- Memory -->
      <div>
        <h3 class="text-sm font-medium mb-4">
          {{ $t('bots.steps.memory') }}
        </h3>
        <p class="text-xs text-muted-foreground mb-3">
          {{ $t('bots.steps.memoryDesc') }}
        </p>
        <Label class="mb-2">{{ $t('bots.settings.memoryProvider') }}</Label>
        <MemoryProviderSelect
          v-model="form.memory_provider_id"
          :providers="memoryProviders"
          :placeholder="$t('common.none')"
        />
      </div>

      <Separator class="my-6" />

      <!-- Settings -->
      <div>
        <h3 class="text-sm font-medium mb-4">
          {{ $t('bots.steps.settings') }}
        </h3>
        <Label class="mb-2">
          {{ $t('bots.timezone') }}
          <span class="text-muted-foreground text-xs ml-1">({{ $t('common.optional') }})</span>
        </Label>
        <TimezoneSelect
          v-model="form.timezone"
          :placeholder="$t('bots.timezonePlaceholder')"
          allow-empty
          :empty-label="$t('bots.timezoneInherited')"
        />
      </div>

      <!-- Hint -->
      <div class="rounded-md border bg-muted-soft px-3 py-2 text-xs text-muted-foreground mt-6">
        {{ $t('bots.createBotWaitHint') }}
      </div>

      <!-- Actions -->
      <div class="flex justify-end gap-3 mt-6 pb-4">
        <Button
          type="button"
          variant="outline"
          :disabled="isCreateFlowBlocked"
          @click="router.back()"
        >
          {{ $t('common.cancel') }}
        </Button>
        <Button
          type="submit"
          :disabled="!canSubmit"
          :loading="isCreateFlowBlocked"
        >
          {{ isCreateFlowBlocked ? $t('bots.createBotSettingUp') : $t('bots.createBot') }}
        </Button>
      </div>
    </form>

    <AvatarEditDialog
      v-model:open="avatarDialogOpen"
      v-model:avatar-url="form.avatar_url"
      :fallback-text="avatarFallback"
    />
  </section>
</template>

<script setup lang="ts">
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
  Button,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Separator,
  Tabs,
  TabsList,
  TabsTrigger,
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@felinic/ui'
import { SquarePen, CircleHelp, Check, X, LoaderCircle } from 'lucide-vue-next'
import { ref, reactive, computed, watch } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { getModels, getProviders, getMemoryProviders, getBotsNameAvailability, getAcpProfiles } from '@memohai/sdk'
import type { BotsCreateBotRequest, AcpprofilePublicProfile } from '@memohai/sdk'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { aclPresetOptions, defaultAclPreset } from '@/constants/acl-presets'
import { emptyTimezoneValue } from '@/utils/timezones'
import { acpAgentDisplayName, normalizeACPAgentID, withACPMetadata, type ACPForm } from '@/utils/acp'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, directBotAgentMetadata } from '@/utils/bot-agent'
import TimezoneSelect from '@/components/timezone-select/index.vue'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'
import ModelSelect from './components/model-select.vue'
import AgentTypePill from './components/agent-type-pill.vue'
import AcpSetupPanel from './components/acp-setup-panel.vue'
import { MEMOH_AGENT_VALUE } from './components/agent-type'
import MemoryProviderSelect from './components/memory-provider-select.vue'
import AvatarEditDialog from './components/avatar-edit-dialog.vue'
import BotImportPanel from './components/bot-import-panel.vue'

const router = useRouter()
const route = useRoute()
const { t } = useI18n()

const mode = ref<'create' | 'import'>(route.query.mode === 'import' ? 'import' : 'create')

const form = reactive({
  name: '',
  display_name: '',
  avatar_url: '',
  acl_preset: defaultAclPreset as string,
  chat_model_id: '',
  // Matches the bots.reasoning_effort column default, so creating a bot without
  // touching this control produces the same state as not sending it at all.
  reasoning_effort: 'medium',
  memory_provider_id: '',
  timezone: emptyTimezoneValue,
})

// Client-side slugify mirroring the backend rules: lowercase, dashes for
// non-alphanumerics, trimmed, clamped to 48 chars.
function slugifyName(value: string): string {
  const slug = value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
  return slug.replace(/^-+|-+$/g, '')
}

const nameTouched = ref(false)
type NameStatus = 'idle' | 'checking' | 'available' | 'taken' | 'invalid' | 'reserved'
const nameStatus = ref<NameStatus>('idle')

// Auto-derive name from display name until the user edits the name field.
watch(() => form.display_name, (displayName) => {
  if (nameTouched.value) return
  form.name = slugifyName(displayName ?? '')
})

const checkNameAvailability = useDebounceFn(async (candidate: string) => {
  const normalized = candidate.trim()
  if (!normalized) {
    nameStatus.value = 'idle'
    return
  }
  try {
    const { data } = await getBotsNameAvailability({
      query: { name: normalized },
      throwOnError: true,
    })
    if (data?.available) {
      nameStatus.value = 'available'
    } else {
      nameStatus.value = (data?.reason as NameStatus) || 'taken'
    }
  } catch {
    nameStatus.value = 'idle'
  }
}, 400)

watch(() => form.name, (candidate) => {
  const normalized = (candidate ?? '').trim()
  nameStatus.value = normalized ? 'checking' : 'idle'
  void checkNameAvailability(normalized)
})

function handleNameInput() {
  nameTouched.value = true
}

const nameStatusMessage = computed(() => {
  switch (nameStatus.value) {
    case 'checking':
      return t('bots.nameStatus.checking')
    case 'available':
      return t('bots.nameStatus.available')
    case 'taken':
      return t('bots.nameStatus.taken')
    case 'invalid':
      return t('bots.nameStatus.invalid')
    case 'reserved':
      return t('bots.nameStatus.reserved')
    default:
      return ''
  }
})

const avatarDialogOpen = ref(false)
const avatarFallback = useAvatarInitials(() => form.display_name || '')

// Data queries
const { data: modelData } = useQuery({
  key: ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const { data: providerData } = useQuery({
  key: ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

const { data: memoryProviderData } = useQuery({
  key: ['memory-providers'],
  query: async () => {
    const { data } = await getMemoryProviders({ throwOnError: true })
    return data
  },
})

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])
const memoryProviders = computed(() => memoryProviderData.value ?? [])

watch(memoryProviders, (list) => {
  if (form.memory_provider_id) return
  const builtin = list.find(p => p.provider === 'builtin')
  if (builtin?.id) {
    form.memory_provider_id = builtin.id
  }
}, { immediate: true })

const { data: acpProfileData } = useQuery({
  key: ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})

const acpProfiles = computed(() => acpProfileData.value?.items ?? [])

const agentType = ref(MEMOH_AGENT_VALUE)
const acpError = ref('')
const acpSetupPanelRef = ref<InstanceType<typeof AcpSetupPanel> | null>(null)

// codex / claude-code are direct runtimes with no ACP profile: they need no
// setup fields at creation (credentials and login are configured on the Bot's
// settings page afterwards).
const selectedDirectRuntime = computed(() => {
  const value = agentType.value
  return value === BOT_AGENT_RUNTIME_CODEX || value === BOT_AGENT_RUNTIME_CLAUDE_CODE ? value : ''
})

// Null for the built-in agent; the panel + metadata only exist when a hosted
// agent is picked.
const selectedAcpProfile = computed<AcpprofilePublicProfile | null>(() => {
  if (agentType.value === MEMOH_AGENT_VALUE || selectedDirectRuntime.value) return null
  return acpProfiles.value.find(profile => normalizeACPAgentID(profile.id) === agentType.value) ?? null
})

// ACL description
const aclDescription = computed(() => {
  const opt = aclPresetOptions.find(o => o.value === form.acl_preset)
  return opt ? t(opt.descriptionKey) : ''
})

// Validation
const canSubmit = computed(() => {
  if (!form.display_name.trim()) return false
  if (!form.name.trim() || nameStatus.value !== 'available') return false
  if (!form.acl_preset) return false
  return true
})

const store = useBotCreateProgressStore()
const submitLoading = ref(false)
const isCreateFlowBlocked = computed(() => submitLoading.value)

// Import from backup
function handleImported(botId: string) {
  if (botId) {
    router.push({ name: 'bot-detail', params: { botName: botId } })
  } else {
    router.push({ name: 'bots' })
  }
}

// A hosted agent travels as bot metadata (same shape the onboarding bot step
// builds); the built-in Memoh agent carries none.
function buildAcpMetadata(): Record<string, unknown> | undefined {
  if (selectedDirectRuntime.value) return undefined
  const panel = acpSetupPanelRef.value
  if (!selectedAcpProfile.value || !panel) return undefined
  const selection = panel.selection()
  const acpForm: ACPForm = {
    agents: {
      [selection.agentId]: {
        enabled: true,
        setup_mode: selection.setupMode,
        managed: selection.setupMode === 'api_key' ? selection.managed : {},
      },
    },
  }
  return withACPMetadata({}, acpForm, acpProfiles.value)
}

function buildCreatePayload(): BotsCreateBotRequest {
  const tz = form.timezone === emptyTimezoneValue ? undefined : form.timezone || undefined

  return {
    name: form.name.trim(),
    display_name: form.display_name.trim(),
    avatar_url: form.avatar_url.trim() || undefined,
    timezone: tz,
    is_active: true,
    acl_preset: form.acl_preset,
    metadata: buildAcpMetadata(),
    wait_for_ready: true,
  }
}

function createStartOptions() {
  return {
    display: {
      display_name: form.display_name.trim(),
      name: form.name.trim(),
      avatar_url: form.avatar_url.trim() || undefined,
    },
    settings: {
      chat_model_id: form.chat_model_id || undefined,
      memory_provider_id: form.memory_provider_id || undefined,
      // Only meaningful alongside a model; without one there are no tiers to pick
      // from and the stored value would be a guess.
      reasoning_effort: form.chat_model_id ? form.reasoning_effort || undefined : undefined,
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
  }
}

async function handleSubmit() {
  if (!canSubmit.value || isCreateFlowBlocked.value) return

  // Submit-time validation, not on-blur: the first empty required field is
  // named inline inside the panel and nothing nags while typing.
  const missing = selectedAcpProfile.value ? acpSetupPanelRef.value?.missingRequiredField() : null
  if (missing) {
    acpError.value = t('bots.agentCreate.requiredError', { field: missing.label || missing.id || '' })
    return
  }

  submitLoading.value = true

  const payload = buildCreatePayload()
  const options = createStartOptions()

  // Hand the live workspace stream off to the dedicated progress route.
  void store.start(payload, options)
  try {
    await router.push({ name: 'bot-create-progress' })
  } finally {
    submitLoading.value = false
  }
}
</script>
