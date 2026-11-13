<template>
  <PageShell
    variant="tab"
    :title="$t('bots.tabs.general')"
  >
    <div class="space-y-8">
      <!-- Bot identity and timezone -->
      <SettingsSection>
        <SettingsRow
          :label="$t('bots.name')"
          stack="sm"
        >
          <div class="w-full sm:w-52 space-y-1">
            <div class="relative">
              <Input
                v-model="form.name"
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
        <SettingsRow
          :label="$t('bots.timezone')"
          stack="sm"
        >
          <div class="w-full sm:w-52">
            <TimezoneSelect
              :model-value="form.timezone || emptyTimezoneValue"
              :placeholder="$t('bots.timezonePlaceholder')"
              allow-empty
              :empty-label="inheritedTimezoneLabel"
              popover-class="min-w-[var(--reka-popover-trigger-width)] w-72"
              popover-align="end"
              @update:model-value="(val: string) => form.timezone = val === emptyTimezoneValue ? '' : val"
            />
          </div>
        </SettingsRow>
      </SettingsSection>

      <SettingsInteractionCard
        :form="form"
        :models="models"
        :providers="providers"
        :bot-agents="botAgents"
        :acp-profiles="acpProfiles"
      />

      <SettingsContextCard
        :form="form"
        :search-providers="searchProviders"
        :fetch-providers="fetchProviders"
      />

      <SettingsMultimediaCard
        id="settings-section-multimedia"
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
        :bot-name="bot?.name ?? ''"
        @delete="handleDeleteBot"
      />
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import {
  Input,
} from '@felinic/ui'
import { Check, X, LoaderCircle } from 'lucide-vue-next'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { reactive, ref, computed, watch, onMounted, onActivated, nextTick } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { useRouter, useRoute } from 'vue-router'
import { PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import TimezoneSelect from '@/components/timezone-select/index.vue'
import { emptyTimezoneValue } from '@/utils/timezones'
import SettingsInteractionCard from './settings-interaction-card.vue'
import SettingsContextCard from './settings-context-card.vue'
import SettingsMultimediaCard from './settings-multimedia-card.vue'
import SettingsDangerZone from './settings-danger-zone.vue'
import BotBackupActions from './bot-backup-actions.vue'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import { getBots, putBotsById, getBotsByBotIdAgents, getBotsByBotIdSettings, putBotsByBotIdSettings, deleteBotsById, getModels, getProviders, getSearchProviders, getFetchProviders, getSpeechProviders, getSpeechModels, getTranscriptionProviders, getTranscriptionModels, getVideoProviders, getVideoModels, getBotsNameAvailability, getAcpProfiles } from '@memohai/sdk'
import { useBotQuery } from '@/composables/api/useBot'
import type { AcpprofilePublicProfile, BotagentsBotAgent, SettingsSettings, SettingsUpsertRequest } from '@memohai/sdk'
import type { Ref } from 'vue'
import { apiErrorStatus, parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import { useChatStore } from '@/store/chat-list'
import { useAutosaveQueue, type AutosaveJob } from '@/composables/use-autosave-queue'
import { saveQueryCacheToDiskNow } from '@/lib/query-cache-persistence'
import { modelProduces } from '@/utils/model-capabilities'

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

const { data: bot } = useBotQuery(botIdRef)

// Names the timezone the bot follows when its own is cleared, as the server
// resolves it, instead of a generic "inherited" placeholder.
const inheritedTimezoneLabel = computed(() => {
  const timezone = settings.value?.inherited_timezone?.trim()
  if (!timezone) return t('bots.timezoneInherited')
  return settings.value?.inherited_timezone_source === 'owner'
    ? t('bots.timezoneFollowOwner', { timezone })
    : t('bots.timezoneFollowSystem', { timezone })
})

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

const { data: botAgentData } = useQuery({
  key: () => ['bot-agents', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdAgents({
      path: { bot_id: botIdRef.value },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!botIdRef.value,
})

const { data: acpProfileData } = useQuery({
  key: ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})

const { data: searchProviderData } = useQuery({
  key: ['search-providers'],
  query: async () => {
    const { data } = await getSearchProviders({ throwOnError: true })
    return data
  },
})

const { data: fetchProviderData } = useQuery({
  key: ['fetch-providers'],
  query: async () => {
    const { data } = await getFetchProviders({ throwOnError: true })
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

const { mutateAsync: deleteBot, isLoading: deleteLoading } = useMutation({
  mutation: async () => {
    await deleteBotsById({ path: { id: botIdRef.value }, throwOnError: true })
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: getBotsQueryKey() })
    queryCache.invalidateQueries({ key: ['bot'] })
  },
})

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])
const botAgents = computed<BotagentsBotAgent[]>(() => botAgentData.value?.items ?? [])
const acpProfiles = computed<AcpprofilePublicProfile[]>(() => acpProfileData.value?.items ?? [])
const imageCapableModels = computed(() =>
  models.value.filter((m) => modelProduces(m.config, 'image')),
)
const searchProviders = computed(() => (searchProviderData.value ?? []).filter((p) => p.enable !== false))
const fetchProviders = computed(() => (fetchProviderData.value ?? []).filter((p) => p.enable !== false || p.provider === 'native' || p.id === form.fetch_provider_id))
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
// `name` and `timezone` are not settings-endpoint fields — they live on the
// bot row and persist through PUT /bots/{id}. They ride the same form anyway
// so the autosave queue gets one uniform diff/rollback model; buildJobs routes
// by field, not by card (card boundaries ≠ API boundaries).
type SettingsForm = SettingsSettings & {
  chat_runtime: string
  chat_acp_agent_id: string
  chat_acp_project_path: string
  chat_acp_project_mode: string
  default_bot_agent_id: string
  timezone: string
  name: string
}

const DEFAULT_SETTINGS_FORM: SettingsForm = {
  chat_model_id: '',
  chat_runtime: 'model',
  chat_acp_agent_id: '',
  chat_acp_project_path: '/data',
  chat_acp_project_mode: 'project',
  default_bot_agent_id: '',
  image_model_id: '',
  search_provider_id: '',
  fetch_provider_id: '',
  memory_enabled: false,
  tts_model_id: '',
  transcription_model_id: '',
  video_model_id: '',
  timezone: '',
  reasoning_effort: 'medium',
  show_tool_calls_in_im: false,
  name: '',
}

const form = reactive<SettingsForm>({ ...DEFAULT_SETTINGS_FORM })

// Fields that persist through PUT /bots/{bot_id}/settings.
const SETTINGS_FIELD_KEYS = [
  'chat_model_id',
  'chat_runtime',
  'chat_acp_agent_id',
  'chat_acp_project_path',
  'chat_acp_project_mode',
  'default_bot_agent_id',
  'image_model_id',
  'search_provider_id',
  'fetch_provider_id',
  'memory_enabled',
  'tts_model_id',
  'transcription_model_id',
  'video_model_id',
  'reasoning_effort',
  'show_tool_calls_in_im',
] as const satisfies readonly (keyof SettingsForm)[]

// Last-known-server snapshot. The autosave engine diffs `form` against this;
// it must advance in the SAME synchronous block as any form write that is not
// a user edit (hydration, rollback), or the diff would misread those writes
// as edits and re-save them.
const synced = reactive<SettingsForm>({ ...form })

watch(settings, (val) => {
  if (!val) return
  const next = {
    chat_model_id: val.chat_model_id ?? '',
    chat_runtime: (val as SettingsForm).chat_runtime || 'model',
    chat_acp_agent_id: (val as SettingsForm).chat_acp_agent_id ?? '',
    chat_acp_project_path: (val as SettingsForm).chat_acp_project_path || '/data',
    chat_acp_project_mode: (val as SettingsForm).chat_acp_project_mode || 'project',
    default_bot_agent_id: (val as SettingsForm).default_bot_agent_id ?? '',
    image_model_id: val.image_model_id ?? '',
    search_provider_id: val.search_provider_id ?? '',
    fetch_provider_id: val.fetch_provider_id ?? '',
    memory_enabled: val.memory_enabled ?? false,
    tts_model_id: val.tts_model_id ?? '',
    transcription_model_id: val.transcription_model_id ?? '',
    video_model_id: val.video_model_id ?? '',
    reasoning_effort: val.reasoning_effort || 'medium',
    show_tool_calls_in_im: val.show_tool_calls_in_im ?? false,
  }
  // Per-field guard: a refetch landing while the user has an unsaved edit
  // (form diverged from synced, save still in flight) must not clobber the
  // edit. Advancing `synced` regardless keeps the diff alive so the autosave
  // queue re-pushes the user's value.
  for (const key of Object.keys(next) as (keyof typeof next)[]) {
    if (form[key] === synced[key]) form[key] = next[key] as never
    synced[key] = next[key] as never
  }
}, { immediate: true })

// ---- URL name (slug) editing ----
// The name autosaves only after the debounced availability check passes; the
// inline status IS the save state (no separate indicator).
type NameStatus = 'idle' | 'checking' | 'available' | 'taken' | 'invalid' | 'reserved'
const nameStatus = ref<NameStatus>('idle')
let currentBotId = props.botId

watch(bot, (val) => {
  // Same per-field guard as the settings hydration above: a refetch landing
  // while the user has an unsaved edit must not clobber it — for the name
  // that means in-progress typing, for the timezone an in-flight save.
  const serverName = val?.name ?? ''
  if (form.name.trim() === synced.name) {
    form.name = serverName
    nameStatus.value = 'idle'
  }
  synced.name = serverName
  const serverTimezone = val?.timezone ?? ''
  if (form.timezone === synced.timezone) {
    form.timezone = serverTimezone
  }
  synced.timezone = serverTimezone
}, { immediate: true })

const hasNameChange = computed(() => form.name.trim() !== synced.name)

const checkNameAvailability = useDebounceFn(async (candidate: string) => {
  const normalized = candidate.trim()
  const requestBotId = currentBotId
  if (!requestBotId || !normalized || !hasNameChange.value) {
    nameStatus.value = 'idle'
    return
  }
  try {
    const { data } = await getBotsNameAvailability({
      query: { name: normalized, exclude_bot_id: requestBotId },
      throwOnError: true,
    })
    if (currentBotId !== requestBotId || form.name.trim() !== normalized) return
    nameStatus.value = data?.available ? 'available' : ((data?.reason as NameStatus) || 'taken')
    if (nameStatus.value === 'available') scheduleSync()
  } catch {
    if (currentBotId === requestBotId) nameStatus.value = 'idle'
  }
}, 400)

watch(() => form.name, (candidate) => {
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

// ---- Autosave engine ----
// This page has no Save button by design (web skill §8: a page of toggles and
// selects auto-saves; success stays silent, only errors surface). The queue
// lives in use-autosave-queue.ts (serialized snapshot-diff engine, unit-tested
// there); this file owns the page-specific partition of changed fields into
// per-endpoint jobs and the tiered invalidation on drain.
const drainedFieldsByBot = new Map<string, Set<keyof SettingsForm>>()

function recordSavedKeys(botId: string, keys: Iterable<keyof SettingsForm>) {
  let set = drainedFieldsByBot.get(botId)
  if (!set) {
    set = new Set()
    drainedFieldsByBot.set(botId, set)
  }
  for (const key of keys) set.add(key)
}

function buildJobs(changed: (keyof SettingsForm)[]): AutosaveJob<SettingsForm>[] {
  const targetBotId = currentBotId
  if (!targetBotId) return []
  const isCurrent = () => currentBotId === targetBotId

  const keys = new Set(changed)
  const jobs: AutosaveJob<SettingsForm>[] = []

  const settingsPayload: SettingsUpsertRequest = {}
  const sentSettings: Partial<SettingsForm> = {}
  for (const key of SETTINGS_FIELD_KEYS) {
    if (keys.has(key)) {
      sentSettings[key] = form[key] as never
      ;(settingsPayload as Record<string, unknown>)[key] = form[key]
    }
  }
  if (Object.keys(settingsPayload).length > 0) {
    const savedSettingKeys = Object.keys(sentSettings) as (keyof SettingsForm)[]
    jobs.push({
      payload: sentSettings,
      isCurrent,
      save: async () => {
        await putBotsByBotIdSettings({
          path: { bot_id: targetBotId },
          body: settingsPayload,
          throwOnError: true,
        })
      },
      onSaved: () => recordSavedKeys(targetBotId, savedSettingKeys),
      onError: (error) => toast.error(resolveApiErrorMessage(error, t('common.saveFailed'))),
    })
  }

  if (keys.has('timezone')) {
    const targetTimezone = form.timezone
    jobs.push({
      payload: { timezone: targetTimezone },
      isCurrent,
      save: async () => {
        await putBotsById({
          path: { id: targetBotId },
          body: { timezone: targetTimezone },
          throwOnError: true,
        })
      },
      onSaved: () => recordSavedKeys(targetBotId, ['timezone']),
      onError: (error) => toast.error(resolveApiErrorMessage(error, t('common.saveFailed'))),
    })
  }

  // The name job is gated on the availability check and never rolls back: the
  // draft must survive a failure so the user can fix it — the inline status
  // carries the error instead.
  if (keys.has('name') && hasNameChange.value && nameStatus.value === 'available') {
    const targetName = form.name.trim()
    jobs.push({
      payload: { name: targetName },
      rollback: false,
      isCurrent,
      save: async () => {
        await putBotsById({
          path: { id: targetBotId },
          body: { name: targetName },
          throwOnError: true,
        })
      },
      onSaved: () => {
        recordSavedKeys(targetBotId, ['name'])
        if (isCurrent() && form.name.trim() === synced.name) {
          nameStatus.value = 'idle'
        }
      },
      onError: (error) => {
        if (!isCurrent()) {
          toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
          return
        }
        if (isNameConflict(error)) {
          nameStatus.value = 'taken'
          toast.error(t('bots.nameStatus.taken'))
        } else {
          // Non-conflict failure: an unsaved name must not keep a fake ✓.
          nameStatus.value = 'idle'
          toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
        }
      },
    })
  }

  return jobs
}

function patchFromAutosavedFields(savedKeys: Set<keyof SettingsForm>): SettingsSettings {
  const patch: SettingsSettings = {}
  for (const key of SETTINGS_FIELD_KEYS) {
    if (savedKeys.has(key)) {
      ;(patch as Record<string, unknown>)[key] = synced[key]
    }
  }
  return patch
}

function onDrained() {
  const batches = [...drainedFieldsByBot.entries()]
  drainedFieldsByBot.clear()
  for (const [targetBotId, savedKeys] of batches) {
    const saved = [...savedKeys]
    if (saved.some((key) => key !== 'name' && key !== 'timezone')) {
      if (currentBotId === targetBotId) {
        const patch = patchFromAutosavedFields(savedKeys)
        if (Object.keys(patch).length > 0) {
          queryCache.setQueryData(['bot-settings', targetBotId], (current) => ({
            ...(current ?? {}),
            ...patch,
          }))
          saveQueryCacheToDiskNow(queryCache)
        }
      }
      void queryCache.invalidateQueries({ key: ['bot-settings', targetBotId] })
    }
    if (savedKeys.has('name') || savedKeys.has('timezone')) {
      queryCache.invalidateQueries({ key: ['bot', targetBotId] })
    }
    // Schedule views read the bot's effective timezone from its settings.
    if (savedKeys.has('timezone')) {
      void queryCache.invalidateQueries({ key: ['bot-settings', targetBotId] })
    }
    // The sidebar lists bots by name; nothing else on this page is visible
    // there, so the expensive full-list refetch only fires for renames.
    if (savedKeys.has('name')) {
      queryCache.invalidateQueries({ key: getBotsQueryKey() })
      void chatStore.refreshBots().catch(() => {})
    }
  }
}

const { scheduleSync } = useAutosaveQueue<SettingsForm>({
  form,
  synced,
  buildJobs,
  onDrained,
})

watch(botIdRef, (nextBotId) => {
  // Flush any pending diff against the previous bot before rebinding `form` and
  // `synced` to clean defaults for `nextBotId`.
  scheduleSync()
  currentBotId = nextBotId
  Object.assign(form, DEFAULT_SETTINGS_FORM)
  Object.assign(synced, DEFAULT_SETTINGS_FORM)
  nameStatus.value = 'idle'
}, { flush: 'sync' })

function isNameConflict(error: unknown): boolean {
  const code = parseMemohError(error)?.code
  if (code) return code === 'bot.name_taken'
  if (apiErrorStatus(error) === 409) return true
  // Desktop can connect to older hosted servers whose conflict response
  // carries neither a code nor a status field — only the English message.
  return resolveApiErrorMessage(error, '').toLowerCase().includes('already taken')
}

async function handleBackupImported(botId: string) {
  queryCache.invalidateQueries({ key: getBotsQueryKey() })
  if (!botId) {
    await router.push({ name: 'bots' })
    return
  }
  if (botId !== props.botId) {
    let resolvedBotName = ''
    try {
      const { data } = await getBots({ throwOnError: true })
      const imported = data?.items?.find(item => item.id === botId)
      resolvedBotName = imported?.name?.trim() || ''
    } catch {
      resolvedBotName = ''
    }
    if (resolvedBotName) {
      await router.push({ name: 'bot-detail', params: { botName: resolvedBotName } })
    } else {
      await router.push({ name: 'bots' })
    }
    return
  }
  queryCache.invalidateQueries({ key: ['bot', botIdRef.value] })
  queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] })
}

async function handleDeleteBot() {
  try {
    await deleteBot()
    // Move the chat selection off the deleted bot before leaving; otherwise
    // the persistent chat pane keeps talking to it and "back to chat" lands on it.
    void chatStore.refreshBots().catch(() => {})
    await router.push({ name: 'bots' })
    toast.success(t('bots.deleteSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.lifecycle.deleteFailed')))
  }
}
</script>
