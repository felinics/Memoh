<template>
  <ActionCard
    :title="$t('bots.channels.telegramStickersTitle')"
    @click="open = true"
  >
    <template #icon>
      <Sticker />
    </template>
  </ActionCard>

  <Dialog v-model:open="open">
    <DialogPanel width="lg">
      <DialogHeader>
        <DialogTitle>{{ $t('bots.channels.telegramStickersTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('bots.channels.telegramStickersDescription') }}</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <div class="mb-4 space-y-3 rounded-lg border border-border bg-card p-4">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div class="min-w-0 flex-1 space-y-1.5">
              <label class="text-sm font-medium text-foreground">
                {{ $t('bots.channels.telegramStickerSets') }}
              </label>
              <p class="text-xs text-muted-foreground">
                {{ $t('bots.channels.telegramStickerSetsDescription') }}
              </p>
              <TagsInput
                :model-value="configuredSetNames"
                :add-on-blur="true"
                :disabled="savingSets"
                @update:model-value="(values) => configuredSetNames = normalizeSetNames(values.map(String))"
              >
                <TagsInputItem
                  v-for="setName in configuredSetNames"
                  :key="setName.toLocaleLowerCase()"
                  :value="setName"
                >
                  <TagsInputItemText />
                  <TagsInputItemDelete />
                </TagsInputItem>
                <TagsInputInput :placeholder="$t('bots.channels.telegramStickerSetsPlaceholder')" />
              </TagsInput>
            </div>
            <Button
              variant="outline"
              :loading="savingSets"
              :disabled="savingSets || !stickerSetsChanged || configuredSetNames.length === 0"
              @click="saveStickerSets"
            >
              {{ $t('bots.channels.telegramStickerSetsSave') }}
            </Button>
          </div>
        </div>

        <div class="mb-4 space-y-3 rounded-lg border border-border bg-card p-4">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div class="min-w-0 flex-1 space-y-1.5">
              <label class="text-sm font-medium text-foreground">
                {{ $t('bots.channels.telegramStickersVisionModel') }}
              </label>
              <p class="text-xs text-muted-foreground">
                {{ $t('bots.channels.telegramStickersVisionModelDescription') }}
              </p>
              <ModelSelect
                v-model="selectedVisionModelId"
                :models="visionModels"
                :providers="enabledProviders"
                model-type="chat"
                :placeholder="$t('bots.channels.telegramStickersVisionModelInherit')"
                :none-label="$t('bots.channels.telegramStickersVisionModelInherit')"
              />
            </div>
            <Button
              variant="outline"
              :loading="savingModel"
              :disabled="savingModel || !visionModelChanged"
              @click="saveVisionModel"
            >
              {{ $t('bots.channels.telegramStickersSaveVisionModel') }}
            </Button>
          </div>
          <p class="text-xs text-muted-foreground">
            {{ $t('bots.channels.telegramStickersActiveVisionModel', {
              model: activeVisionModelLabel,
              prompt: catalog?.prompt_version || '—',
            }) }}
          </p>
        </div>

        <div
          v-if="catalog"
          class="mb-4 flex flex-wrap items-center gap-2"
        >
          <Badge variant="outline">
            {{ $t('bots.channels.telegramStickerSetsCount', { count: stickerSets.length }) }}
          </Badge>
          <Badge variant="success">
            {{ $t('bots.channels.telegramStickersReady', { count: catalog.ready_count ?? 0 }) }}
          </Badge>
          <Badge
            v-if="catalog.failed_count"
            variant="destructive"
          >
            {{ $t('bots.channels.telegramStickersFailed', { count: catalog.failed_count }) }}
          </Badge>
          <Badge
            v-if="catalog.pending_count"
            variant="secondary"
          >
            {{ $t('bots.channels.telegramStickersPending', { count: catalog.pending_count }) }}
          </Badge>
          <Button
            class="ml-auto"
            variant="outline"
            size="sm"
            :loading="refreshing"
            :disabled="refreshing"
            @click="refreshStickerSet"
          >
            <RefreshCw class="size-4" />
            {{ $t('bots.channels.telegramStickersRefreshSet') }}
          </Button>
        </div>

        <div
          v-if="loading"
          class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4"
        >
          <Skeleton
            v-for="index in 8"
            :key="index"
            class="aspect-square rounded-lg"
          />
        </div>

        <div
          v-else-if="error"
          class="flex flex-col items-center gap-3 py-10 text-center"
        >
          <p class="text-sm text-muted-foreground">
            {{ resolveApiErrorMessage(error, $t('bots.channels.telegramStickersLoadFailed')) }}
          </p>
          <Button
            variant="outline"
            size="sm"
            @click="refetch()"
          >
            {{ $t('common.retry') }}
          </Button>
        </div>

        <div
          v-else-if="stickerSets.every(set => set.stickers.length === 0)"
          class="py-10 text-center text-sm text-muted-foreground"
        >
          {{ $t('bots.channels.telegramStickersEmpty') }}
        </div>

        <div
          v-else
          class="space-y-4"
        >
          <details
            v-for="(set, setIndex) in stickerSets"
            :key="set.name"
            :open="setIndex === 0"
            class="rounded-lg border border-border bg-card"
          >
            <summary class="flex cursor-pointer list-none flex-wrap items-center gap-2 px-4 py-3 text-sm font-medium text-foreground">
              <span class="mr-auto">{{ set.name || $t('bots.channels.telegramStickersUnknownSet') }}</span>
              <Badge variant="success">
                {{ $t('bots.channels.telegramStickersReady', { count: set.ready_count ?? 0 }) }}
              </Badge>
              <Badge
                v-if="set.failed_count"
                variant="destructive"
              >
                {{ $t('bots.channels.telegramStickersFailed', { count: set.failed_count }) }}
              </Badge>
              <Badge
                v-if="set.pending_count"
                variant="secondary"
              >
                {{ $t('bots.channels.telegramStickersPending', { count: set.pending_count }) }}
              </Badge>
            </summary>
            <div class="grid grid-cols-2 gap-3 border-t border-border p-3 sm:grid-cols-3 lg:grid-cols-4">
              <article
                v-for="item in set.stickers"
                :key="item.id"
                class="flex min-w-0 flex-col gap-2 rounded-lg border border-border bg-background p-3"
              >
                <TelegramStickerPreview
                  :bot-id="botId"
                  :sticker-id="item.id"
                  :alt="item.description || item.id"
                />
                <div class="flex min-w-0 items-center justify-between gap-2">
                  <span class="truncate font-mono text-xs text-muted-foreground">
                    {{ item.id }} {{ item.emoji || '' }}
                  </span>
                  <Badge :variant="statusVariant(item.status)">
                    {{ statusLabel(item.status) }}
                  </Badge>
                </div>
                <p class="min-h-10 text-xs leading-5 text-foreground">
                  {{ item.description || $t('bots.channels.telegramStickersNoDescription') }}
                </p>
                <Button
                  v-if="item.status === 'failed'"
                  variant="outline"
                  size="sm"
                  :loading="retryingIds.has(item.id)"
                  :disabled="retryingIds.has(item.id)"
                  @click="retryRecognition(item.id)"
                >
                  {{ $t('bots.channels.telegramStickersRetryRecognition') }}
                </Button>
              </article>
            </div>
          </details>
        </div>
      </DialogBody>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  ActionCard, Badge, Button, Dialog, DialogBody, DialogDescription, DialogHeader,
  DialogPanel, DialogTitle, Skeleton, TagsInput, TagsInputInput, TagsInputItem,
  TagsInputItemDelete, TagsInputItemText, toast,
} from '@felinic/ui'
import { RefreshCw, Sticker } from 'lucide-vue-next'
import {
  getBotsByBotIdTelegramStickers,
  getBotsByBotIdSettings,
  getModels,
  getProviders,
  postBotsByBotIdTelegramStickersRefresh,
  postBotsByBotIdTelegramStickersByStickerIdRetry,
  putBotsByBotIdTelegramStickersSets,
  putBotsByBotIdSettings,
} from '@memohai/sdk'
import type { HandlersTelegramStickerCatalogEntry, HandlersTelegramStickerSetCatalog } from '@memohai/sdk'
import TelegramStickerPreview from './telegram-sticker-preview.vue'
import ModelSelect from './model-select.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ botId: string }>()
const { t } = useI18n()
const open = ref(false)
const refreshing = ref(false)
const savingModel = ref(false)
const savingSets = ref(false)
const selectedVisionModelId = ref('')
const configuredSetNames = ref<string[]>([])
const savedSetNames = ref<string[]>([])
const retryingIds = reactive(new Set<string>())
const queryCache = useQueryCache()
const STICKER_STATUS_POLL_MS = 3000
let recognitionPollTimer: ReturnType<typeof setInterval> | null = null
let recognitionPollInFlight = false

const { data: settings } = useQuery({
  key: () => ['bot-settings', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => open.value && !!props.botId,
})

const { data: modelData } = useQuery({
  key: ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
  enabled: () => open.value,
})

const { data: providerData } = useQuery({
  key: ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
  enabled: () => open.value,
})

const { data: catalog, error, isLoading: loading, refetch } = useQuery({
  key: () => ['telegram-stickers', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdTelegramStickers({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => open.value && !!props.botId,
})

type StickerItem = HandlersTelegramStickerCatalogEntry & { id: string }
type StickerSetView = HandlersTelegramStickerSetCatalog & { name: string; stickers: StickerItem[] }

const stickerSets = computed<StickerSetView[]>(() => {
  const sets = catalog.value?.sets ?? []
  if (sets.length > 0) {
    return sets.map((set, index) => ({
      ...set,
      name: set.name || `${t('bots.channels.telegramStickersUnknownSet')} ${index + 1}`,
      stickers: (set.stickers ?? []).filter((item): item is StickerItem => !!item.id),
    }))
  }
  const stickers = (catalog.value?.stickers ?? []).filter((item): item is StickerItem => !!item.id)
  if (!catalog.value?.name && stickers.length === 0) return []
  return [{
    name: catalog.value?.name || t('bots.channels.telegramStickersUnknownSet'),
    total_count: catalog.value?.total_count,
    ready_count: catalog.value?.ready_count,
    failed_count: catalog.value?.failed_count,
    pending_count: catalog.value?.pending_count,
    stickers,
  }]
})
const stickerSetsChanged = computed(() => JSON.stringify(configuredSetNames.value) !== JSON.stringify(savedSetNames.value))
const enabledProviders = computed(() => (providerData.value ?? []).filter(provider => provider.enable !== false))
const enabledProviderIds = computed(() => new Set(enabledProviders.value.map(provider => provider.id)))
const visionModels = computed(() => (modelData.value ?? []).filter(model =>
  model.enable !== false
  && enabledProviderIds.value.has(model.provider_id)
  && model.config?.compatibilities?.includes('vision'),
))
const savedVisionModelId = computed(() => settings.value?.telegram_sticker_vision_model_id ?? '')
const visionModelChanged = computed(() => selectedVisionModelId.value !== savedVisionModelId.value)
const activeVisionModelLabel = computed(() => {
  const activeId = catalog.value?.recognition_model_id ?? ''
  const model = visionModels.value.find(item => (item.id || item.model_id) === activeId)
  if (model) return model.name || model.model_id || activeId
  if (activeId) return activeId
  return t('bots.channels.telegramStickersVisionModelUnavailable')
})

watch([open, settings], ([isOpen, currentSettings]) => {
  if (!isOpen) return
  selectedVisionModelId.value = currentSettings?.telegram_sticker_vision_model_id ?? ''
}, { immediate: true })

watch(catalog, (value) => {
  const names = normalizeSetNames((value?.sets ?? []).map(set => set.name || '').filter(Boolean))
  const wasDirty = stickerSetsChanged.value
  savedSetNames.value = names
  if (!wasDirty) configuredSetNames.value = [...names]
}, { immediate: true })

watch(
  [open, () => catalog.value?.pending_count ?? 0],
  ([isOpen, pendingCount]) => {
    stopRecognitionPolling()
    if (!isOpen || pendingCount <= 0) return
    recognitionPollTimer = setInterval(() => void pollRecognitionStatus(), STICKER_STATUS_POLL_MS)
  },
  { immediate: true },
)

onBeforeUnmount(stopRecognitionPolling)

function normalizeSetNames(values: string[]): string[] {
  const unique = new Map<string, string>()
  for (const value of values) {
    const name = value.trim()
    if (name && !unique.has(name.toLocaleLowerCase())) unique.set(name.toLocaleLowerCase(), name)
  }
  return [...unique.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }))
}

function statusVariant(status?: string): 'success' | 'destructive' | 'secondary' {
  if (status === 'ready') return 'success'
  if (status === 'failed') return 'destructive'
  return 'secondary'
}

function statusLabel(status?: string): string {
  if (status === 'ready') return t('bots.channels.telegramStickersStatusReady')
  if (status === 'failed') return t('bots.channels.telegramStickersStatusFailed')
  return t('bots.channels.telegramStickersStatusPending')
}

function stopRecognitionPolling() {
  if (recognitionPollTimer === null) return
  clearInterval(recognitionPollTimer)
  recognitionPollTimer = null
}

async function pollRecognitionStatus() {
  if (recognitionPollInFlight || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
  recognitionPollInFlight = true
  try {
    await refetch()
  } catch {
    // The query owns its error state; background polling stays silent.
  } finally {
    recognitionPollInFlight = false
  }
}

async function retryRecognition(stickerId: string) {
  retryingIds.add(stickerId)
  try {
    await postBotsByBotIdTelegramStickersByStickerIdRetry({
      path: { bot_id: props.botId, sticker_id: stickerId },
      throwOnError: true,
    })
    await refetch()
    toast.success(t('bots.channels.telegramStickersRetrySuccess'))
  } catch (retryError) {
    toast.error(resolveApiErrorMessage(retryError, t('bots.channels.telegramStickersRetryFailed')))
  } finally {
    retryingIds.delete(stickerId)
  }
}

async function saveVisionModel() {
  savingModel.value = true
  try {
    await putBotsByBotIdSettings({
      path: { bot_id: props.botId },
      body: { telegram_sticker_vision_model_id: selectedVisionModelId.value },
      throwOnError: true,
    })
    await queryCache.invalidateQueries({ key: ['bot-settings', props.botId] })
    await refetch()
    toast.success(t('bots.channels.telegramStickersVisionModelSaved'))
  } catch (saveError) {
    toast.error(resolveApiErrorMessage(saveError, t('bots.channels.telegramStickersVisionModelSaveFailed')))
  } finally {
    savingModel.value = false
  }
}

async function saveStickerSets() {
  savingSets.value = true
  try {
    const names = normalizeSetNames(configuredSetNames.value)
    await putBotsByBotIdTelegramStickersSets({
      path: { bot_id: props.botId },
      body: { names },
      throwOnError: true,
    })
    configuredSetNames.value = names
    savedSetNames.value = [...names]
    await refetch()
    toast.success(t('bots.channels.telegramStickerSetsSaved'))
  } catch (saveError) {
    toast.error(resolveApiErrorMessage(saveError, t('bots.channels.telegramStickerSetsSaveFailed')))
  } finally {
    savingSets.value = false
  }
}

async function refreshStickerSet() {
  refreshing.value = true
  try {
    await postBotsByBotIdTelegramStickersRefresh({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    await refetch()
    toast.success(t('bots.channels.telegramStickersRefreshSuccess'))
  } catch (refreshError) {
    toast.error(resolveApiErrorMessage(refreshError, t('bots.channels.telegramStickersRefreshFailed')))
  } finally {
    refreshing.value = false
  }
}
</script>
