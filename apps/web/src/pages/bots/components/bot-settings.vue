<template>
  <PageShell
    variant="tab"
    :title="$t('bots.tabs.settings')"
  >
    <template #actions>
      <Transition name="fade">
        <div
          v-if="hasChanges"
          class="flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-muted-soft border border-border/50"
        >
          <div class="size-1 rounded-full bg-muted-foreground/40" />
          <span class="text-[10px] text-muted-foreground font-medium whitespace-nowrap">{{ $t('common.unsaved') }}</span>
        </div>
      </Transition>
      <Button
        :disabled="!hasChanges || !nameValid"
        :loading="saveLoading"
        @click="handleSave"
      >
        {{ $t('bots.settings.save') }}
      </Button>
    </template>

    <div class="space-y-8">
      <!-- URL Name -->
      <SettingsSection>
        <SettingsRow
          :label="$t('bots.name')"
          :description="$t('bots.nameHint')"
        >
          <div class="w-52 space-y-1">
            <div class="relative">
              <Input
                v-model="nameInput"
                type="text"
                autocapitalize="off"
                autocomplete="off"
                spellcheck="false"
                class="h-9 pr-9"
                :placeholder="$t('bots.namePlaceholder')"
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
              v-if="nameStatusMessage"
              class="text-xs"
              :class="nameStatus === 'available' ? 'text-success-foreground' : 'text-destructive'"
            >
              {{ nameStatusMessage }}
            </p>
          </div>
        </SettingsRow>
      </SettingsSection>

      <SettingsGlobalCard :form="form" />

      <SettingsInteractionCard
        :form="form"
        :models="models"
        :providers="providers"
        :acp-profiles="acpProfiles"
        :bot-metadata="botMetadata"
      />

      <SettingsContextCard
        :form="form"
        :search-providers="searchProviders"
        :fetch-providers="fetchProviders"
        :memory-providers="memoryProviders"
      />

      <SettingsMultimediaCard
        :form="form"
        :tts-models="ttsModels"
        :tts-providers="ttsProviders"
        :transcription-models="transcriptionModels"
        :transcription-providers="transcriptionProviders"
        :image-capable-models="imageCapableModels"
        :providers="providers"
        :video-models="videoModels"
        :video-providers="videoProviders"
      />

      <!-- Backup -->
      <SettingsSection>
        <SettingsRow
          :label="$t('bots.backup.cardTitle')"
          :description="$t('bots.backup.cardSubtitle')"
        >
          <BotBackupActions
            :bot-id="botId"
            :bot-name="bot?.display_name"
            @imported="handleBackupImported"
          />
        </SettingsRow>
      </SettingsSection>

      <SettingsDangerZone
        :delete-loading="deleteLoading"
        @delete="handleDeleteBot"
      />
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import {
  Button,
  Input,
} from '@felinic/ui'
import { Check, X, LoaderCircle } from 'lucide-vue-next'
import { reactive, ref, computed, watch, onMounted, onActivated, nextTick } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { useRouter, useRoute } from 'vue-router'
import { PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import SettingsGlobalCard from './settings-global-card.vue'
import SettingsInteractionCard from './settings-interaction-card.vue'
import SettingsContextCard from './settings-context-card.vue'
import SettingsMultimediaCard from './settings-multimedia-card.vue'
import SettingsDangerZone from './settings-danger-zone.vue'
import BotBackupActions from './bot-backup-actions.vue'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import { getAcpProfiles, getBotsById, putBotsById, getBotsByBotIdSettings, putBotsByBotIdSettings, deleteBotsById, getModels, getProviders, getSearchProviders, getFetchProviders, getMemoryProviders, getSpeechProviders, getSpeechModels, getTranscriptionProviders, getTranscriptionModels, getVideoProviders, getVideoModels, getBotsNameAvailability } from '@memohai/sdk'
import type { AcpprofilePublicProfile, SettingsSettings } from '@memohai/sdk'
import type { Ref } from 'vue'
import { apiErrorStatus, parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import { useChatStore } from '@/store/chat-list'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const chatStore = useChatStore()

// Deep-link scroll: another surface (e.g. the Overview "choose a model"
// reminder) can navigate here with ?section=<anchor-id> to land on a specific
// block. We scroll after paint, then strip the param so a later manual visit or
// a back/forward doesn't yank the user back down. KeepAlive caches this tab, so
// onMounted covers the first open and onActivated covers cached re-entry.
function scrollToSectionParam() {
  const raw = route.query.section
  const section = Array.isArray(raw) ? raw[0] : raw
  if (!section) return
  void nextTick(() => {
    requestAnimationFrame(() => {
      document.getElementById(`settings-section-${section}`)?.scrollIntoView({
        behavior: 'smooth',
        block: 'start',
      })
      // Clear only the section param, preserving tab and everything else.
      const { section: _drop, ...rest } = route.query
      void router.replace({ query: rest })
    })
  })
}

onMounted(scrollToSectionParam)
onActivated(scrollToSectionParam)

const botIdRef = computed(() => props.botId) as Ref<string>

// ---- Data ----
const queryCache = useQueryCache()

const { data: settings } = useQuery({
  key: () => ['bot-settings', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
})

const { data: bot } = useQuery({
  key: () => ['bot', botIdRef.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
})

const { data: modelData } = useQuery({
  key: ['all-models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const { data: providerData } = useQuery({
  key: ['all-providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

const { data: acpProfileData } = useQuery({
  key: ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})

const { data: searchProviderData } = useQuery({
  key: ['all-search-providers'],
  query: async () => {
    const { data } = await getSearchProviders({ throwOnError: true })
    return data
  },
})

const { data: fetchProviderData } = useQuery({
  key: ['all-fetch-providers'],
  query: async () => {
    const { data } = await getFetchProviders({ throwOnError: true })
    return data
  },
})

const { data: memoryProviderData } = useQuery({
  key: ['all-memory-providers'],
  query: async () => {
    const { data } = await getMemoryProviders({ throwOnError: true })
    return data
  },
})

const { data: ttsProviderData } = useQuery({
  key: ['speech-providers'],
  query: async () => {
    const { data } = await getSpeechProviders({ throwOnError: true })
    return data
  },
})

const { data: ttsModelData } = useQuery({
  key: ['speech-models'],
  query: async () => {
    const { data } = await getSpeechModels({ throwOnError: true })
    return data
  },
})

const { data: transcriptionModelData } = useQuery({
  key: ['transcription-models'],
  query: async () => {
    const { data } = await getTranscriptionModels({ throwOnError: true })
    return data
  },
})

const { data: transcriptionProviderData } = useQuery({
  key: ['transcription-providers'],
  query: async () => {
    const { data } = await getTranscriptionProviders({ throwOnError: true })
    return data
  },
})

const { data: videoProviderData } = useQuery({
  key: ['video-providers'],
  query: async () => {
    const { data } = await getVideoProviders({ throwOnError: true })
    return data
  },
})

const { data: videoModelData } = useQuery({
  key: ['video-models'],
  query: async () => {
    const { data } = await getVideoModels({ throwOnError: true })
    return data
  },
})

const { mutateAsync: updateSettings, isLoading } = useMutation({
  mutation: async (body: Partial<SettingsSettings>) => {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: botIdRef.value },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] })
    void chatStore.refreshBots().catch(() => {})
  },
})

const { mutateAsync: updateBot, isLoading: isUpdatingBot } = useMutation({
  mutation: async (body: { timezone?: string, name?: string }) => {
    const { data } = await putBotsById({
      path: { id: botIdRef.value },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot'] })
    queryCache.invalidateQueries({ key: ['bot', botIdRef.value] })
    queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] })
    queryCache.invalidateQueries({ key: ['bots'] })
    void chatStore.refreshBots().catch(() => {})
  },
})

// ---- URL name (slug) editing ----
type NameStatus = 'idle' | 'checking' | 'available' | 'taken' | 'invalid' | 'reserved'
const nameInput = ref('')
const nameStatus = ref<NameStatus>('idle')

watch(bot, (val) => {
  nameInput.value = val?.name ?? ''
  nameStatus.value = 'idle'
}, { immediate: true })

const hasNameChange = computed(() => nameInput.value.trim() !== (bot.value?.name ?? ''))

const checkNameAvailability = useDebounceFn(async (candidate: string) => {
  const normalized = candidate.trim()
  if (!normalized || !hasNameChange.value) {
    nameStatus.value = 'idle'
    return
  }
  try {
    const { data } = await getBotsNameAvailability({
      query: { name: normalized, exclude_bot_id: botIdRef.value },
      throwOnError: true,
    })
    nameStatus.value = data?.available ? 'available' : ((data?.reason as NameStatus) || 'taken')
  } catch {
    nameStatus.value = 'idle'
  }
}, 400)

watch(nameInput, (candidate) => {
  if (!hasNameChange.value) {
    nameStatus.value = 'idle'
    return
  }
  nameStatus.value = 'checking'
  void checkNameAvailability(candidate)
})

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

const nameValid = computed(() => !hasNameChange.value || nameStatus.value === 'available')

const { mutateAsync: deleteBot, isLoading: deleteLoading } = useMutation({
  mutation: async () => {
    await deleteBotsById({ path: { id: botIdRef.value }, throwOnError: true })
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bots'] })
    queryCache.invalidateQueries({ key: ['bot'] })
  },
})

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])
const acpProfiles = computed<AcpprofilePublicProfile[]>(() => acpProfileData.value?.items ?? [])
const botMetadata = computed(() => bot.value?.metadata as Record<string, unknown> | undefined)
const imageCapableModels = computed(() =>
  models.value.filter((m) => m.config?.compatibilities?.includes('image-output')),
)
const searchProviders = computed(() => (searchProviderData.value ?? []).filter((p) => p.enable !== false))
const fetchProviders = computed(() => (fetchProviderData.value ?? []).filter((p) => p.enable !== false || p.provider === 'native' || p.id === form.fetch_provider_id))
const memoryProviders = computed(() => memoryProviderData.value ?? [])
const ttsProviders = computed(() => (ttsProviderData.value ?? []).filter((p) => p.enable !== false))
const enabledTtsProviderIds = computed(() => new Set(ttsProviders.value.map((p) => p.id)))
const transcriptionProviders = computed(() => (transcriptionProviderData.value ?? []).filter((p: Record<string, unknown>) => p.enable !== false))
const enabledTranscriptionProviderIds = computed(() => new Set(transcriptionProviders.value.map((p: Record<string, unknown>) => p.id as string)))
const ttsModels = computed(() => (ttsModelData.value ?? []).filter((m: Record<string, unknown>) => enabledTtsProviderIds.value.has(m.provider_id as string)))
const transcriptionModels = computed(() => (transcriptionModelData.value ?? []).filter((m: Record<string, unknown>) => enabledTranscriptionProviderIds.value.has(m.provider_id as string)))
const videoProviders = computed(() => (videoProviderData.value ?? []).filter((p: Record<string, unknown>) => p.enable !== false))
const enabledVideoProviderIds = computed(() => new Set(videoProviders.value.map((p: Record<string, unknown>) => p.id as string)))
const videoModels = computed(() =>
  (videoModelData.value ?? [])
    .filter((m: Record<string, unknown>) => enabledVideoProviderIds.value.has(m.provider_id as string))
    .map((model) => ({ ...model, type: 'video' as const })),
)

// ---- Form ----
type SettingsForm = SettingsSettings & {
  chat_runtime: string
  chat_acp_agent_id: string
  chat_acp_project_path: string
  chat_acp_project_mode: string
  timezone: string
}

const form = reactive<SettingsForm>({
  chat_model_id: '',
  chat_runtime: 'model',
  chat_acp_agent_id: '',
  chat_acp_project_path: '/data',
  chat_acp_project_mode: 'project',
  image_model_id: '',
  search_provider_id: '',
  fetch_provider_id: '',
  memory_provider_id: '',
  tts_model_id: '',
  transcription_model_id: '',
  video_model_id: '',
  timezone: '',
  language: '',
  reasoning_enabled: false,
  reasoning_effort: 'medium',
  show_tool_calls_in_im: false,
})

watch(settings, (val) => {
  if (val) {
    form.chat_model_id = val.chat_model_id ?? ''
    const next = val as SettingsForm
    form.chat_runtime = next.chat_runtime === 'acp_agent' ? 'acp_agent' : 'model'
    form.chat_acp_agent_id = next.chat_acp_agent_id ?? ''
    form.chat_acp_project_path = next.chat_acp_project_path || '/data'
    form.chat_acp_project_mode = next.chat_acp_project_mode || 'project'
    form.image_model_id = val.image_model_id ?? ''
    form.search_provider_id = val.search_provider_id ?? ''
    form.fetch_provider_id = val.fetch_provider_id ?? ''
    form.memory_provider_id = val.memory_provider_id ?? ''
    form.tts_model_id = val.tts_model_id ?? ''
    form.transcription_model_id = val.transcription_model_id ?? ''
    form.video_model_id = val.video_model_id ?? ''
    form.language = val.language ?? ''
    form.reasoning_enabled = val.reasoning_enabled ?? false
    form.reasoning_effort = val.reasoning_effort || 'medium'
    form.show_tool_calls_in_im = val.show_tool_calls_in_im ?? false
  }
}, { immediate: true })

watch(bot, (val) => {
  form.timezone = val?.timezone ?? ''
}, { immediate: true })

const hasSettingsChanges = computed(() => {
  if (!settings.value) return true
  const s = settings.value
  return (
    form.chat_model_id !== (s.chat_model_id ?? '')
    || form.chat_runtime !== (((s as SettingsForm).chat_runtime === 'acp_agent') ? 'acp_agent' : 'model')
    || form.chat_acp_agent_id !== ((s as SettingsForm).chat_acp_agent_id ?? '')
    || form.chat_acp_project_path !== (((s as SettingsForm).chat_acp_project_path || '/data'))
    || form.chat_acp_project_mode !== (((s as SettingsForm).chat_acp_project_mode || 'project'))
    || form.image_model_id !== (s.image_model_id ?? '')
    || form.search_provider_id !== (s.search_provider_id ?? '')
    || form.fetch_provider_id !== (s.fetch_provider_id ?? '')
    || form.memory_provider_id !== (s.memory_provider_id ?? '')
    || form.tts_model_id !== (s.tts_model_id ?? '')
    || form.transcription_model_id !== (s.transcription_model_id ?? '')
    || form.video_model_id !== (s.video_model_id ?? '')
    || form.language !== (s.language ?? '')
    || form.reasoning_enabled !== (s.reasoning_enabled ?? false)
    || form.reasoning_effort !== (s.reasoning_effort || 'medium')
    || form.show_tool_calls_in_im !== (s.show_tool_calls_in_im ?? false)
  )
})

const hasTimezoneChanges = computed(() => form.timezone !== (bot.value?.timezone ?? ''))
const hasChanges = computed(() => hasSettingsChanges.value || hasTimezoneChanges.value || hasNameChange.value)
const saveLoading = computed(() => isLoading.value || isUpdatingBot.value)

function isNameConflict(error: unknown): boolean {
  const code = parseMemohError(error)?.code
  if (code) return code === 'bot.name_taken'
  if (apiErrorStatus(error) === 409) return true
  // Desktop can connect to older hosted servers whose conflict response
  // carries neither a code nor a status field — only the English message.
  return resolveApiErrorMessage(error, '').toLowerCase().includes('already taken')
}

async function handleSave() {
  if (!nameValid.value) {
    toast.error(nameStatusMessage.value || t('bots.nameStatus.invalid'))
    return
  }
  try {
    if (hasSettingsChanges.value) {
      const { timezone: _timezone, ...settingsPayload } = form
      await updateSettings(settingsPayload)
    }
    if (hasTimezoneChanges.value || hasNameChange.value) {
      await updateBot({
        ...(hasTimezoneChanges.value ? { timezone: form.timezone } : {}),
        ...(hasNameChange.value ? { name: nameInput.value.trim() } : {}),
      })
    }
    toast.success(t('bots.settings.saveSuccess'))
  } catch (error) {
    if (isNameConflict(error)) {
      nameStatus.value = 'taken'
      toast.error(t('bots.nameStatus.taken'))
      return
    }
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}

function handleBackupImported(botId: string) {
  queryCache.invalidateQueries({ key: ['bots'] })
  if (botId && botId !== props.botId) {
    router.push({ name: 'bot-detail', params: { botId } })
    return
  }
  queryCache.invalidateQueries({ key: ['bot', botIdRef.value] })
  queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] })
}

async function handleDeleteBot() {
  try {
    await deleteBot()
    await router.push({ name: 'bots' })
    toast.success(t('bots.deleteSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.lifecycle.deleteFailed')))
  }
}
</script>
