<template>
  <PageShell
    variant="tab"
    :title="$t('bots.memory.title')"
  >
    <template #actions>
      <!-- Compact: low-frequency maintenance action, kept as a quiet outline
           button so it doesn't compete with the primary CTA. -->
      <Popover
        v-if="canSemanticCompact && memories.length > 0"
        v-model:open="compactPopoverOpen"
      >
        <PopoverTrigger as-child>
          <Button
            variant="outline"
            type="button"
            :disabled="compactLoading"
          >
            <Brain class="size-4" />
            {{ $t('bots.memory.compact') }}
          </Button>
        </PopoverTrigger>

        <PopoverContent
          side="bottom"
          align="end"
          class="w-72 p-3"
          :side-offset="4"
        >
          <FormStack>
            <FieldStack :label="$t('bots.memory.compactRatio')">
              <Select v-model="compactRatio">
                <SelectTrigger
                  size="sm"
                  class="w-full"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="0.8">
                    {{ $t('bots.memory.compactRatioLight') }}
                  </SelectItem>
                  <SelectItem value="0.5">
                    {{ $t('bots.memory.compactRatioMedium') }}
                  </SelectItem>
                  <SelectItem value="0.3">
                    {{ $t('bots.memory.compactRatioAggressive') }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldStack>

            <FieldStack>
              <template #label>
                <Label>
                  {{ $t('bots.memory.compactDecayDate') }}
                  <span class="text-muted-foreground">({{ $t('common.optional') }})</span>
                </Label>
              </template>
              <Popover>
                <PopoverTrigger
                  type="button"
                  data-slot="select-trigger"
                  data-size="sm"
                  :data-placeholder="compactDecayRange ? undefined : ''"
                  :class="[selectTriggerClass, 'w-full']"
                >
                  <span class="flex min-w-0 items-center gap-2">
                    <CalendarDays class="size-4" />
                    <span class="truncate">{{ compactDecayLabel }}</span>
                  </span>
                </PopoverTrigger>
                <PopoverContent
                  class="w-auto p-0"
                  align="start"
                >
                  <div class="p-3">
                    <RangeCalendar v-model="compactDecayRange" />
                  </div>
                </PopoverContent>
              </Popover>
            </FieldStack>
          </FormStack>

          <div class="mt-3 flex items-center justify-end gap-2 border-t pt-3">
            <Button
              variant="ghost"
              @click="compactPopoverOpen = false"
            >
              {{ $t('common.cancel') }}
            </Button>
            <Button
              :loading="compactLoading"
              @click="handleCompact"
            >
              {{ $t('common.confirm') }}
            </Button>
          </div>
        </PopoverContent>
      </Popover>

      <Button @click="openNewMemoryDialog">
        <Plus class="size-4" />
        {{ $t('bots.memory.newMemory') }}
      </Button>
    </template>

    <!-- Stat tiles: two framed metric readouts, same row. The caller owns the
         grid; each tile carries its own border so there's no divider-line
         visual-weight bias. `—` while a cold load has no data yet. -->
    <section class="mb-6 grid grid-cols-2 gap-3">
      <MetricReadout
        :label="$t('bots.memory.totalCount')"
        :value="String(loading && memories.length === 0 ? '—' : stats.totalCount)"
      />
      <MetricReadout :label="$t('bots.memory.lastUpdated')">
        <template #value>
          <template v-if="stats.lastUpdatedAt">
            {{ formatRelativeTime(stats.lastUpdatedAt, { locale }) }}
          </template>
          <template v-else>
            —
          </template>
        </template>
      </MetricReadout>
    </section>

    <!-- Index health banner: the semantic seed index is behind the wiki store
         (failed upserts queued for retry) or pgvector is down. Graph recall
         still works, but surface the state so the user understands why semantic
         recall may be weaker. -->
    <CalloutBanner
      v-if="showDegradedBanner"
      tone="warning"
      class="mb-6"
      :title="$t('bots.memory.degradedTitle')"
      :description="$t('bots.memory.degradedDesc')"
    >
      <Button
        variant="outline"
        size="sm"
        :loading="ingestLoading"
        @click="handleIngest"
      >
        <RefreshCw class="size-3.5" />
        {{ $t('bots.memory.ingestAction') }}
      </Button>
    </CalloutBanner>

    <!-- Memory graph: the shape of the wiki — hubs, clusters, orphans. -->
    <section class="mb-6">
      <MemoryGraph
        ref="graphRef"
        :bot-id="props.botId"
      />
    </section>

    <!-- Advanced: manual sync + path/index metrics (moved from General settings). -->
    <MemoryAdvancedActions
      :bot-id="props.botId"
      :memory-status="memoryStatus"
      :status-loading="memoryStatusLoading"
      @synced="handleAdvancedSynced"
    />

    <!-- Server-side recall over the wiki store; results render in place of the
         dated stream. -->
    <section class="mb-4">
      <InputGroup>
        <InputGroupAddon align="inline-start">
          <Search class="size-4 text-muted-foreground" />
        </InputGroupAddon>
        <InputGroupInput
          v-model="searchInput"
          :placeholder="$t('bots.memory.searchPlaceholder')"
          @input="onSearchInput"
          @keydown.enter.prevent="runSearch"
        />
        <InputGroupAddon
          v-if="searchInput"
          align="inline-end"
        >
          <InputGroupButton
            :aria-label="$t('common.clear')"
            @click="clearSearch"
          >
            <X class="size-3.5" />
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
    </section>

    <!-- Layer filter chips: local filter over the loaded list. Hidden while a
         server search is active (search results are already query-scoped).
         Stays hand-written: a wrapping row of count-bearing filter chips is a
         different relationship from the single-track SegmentedControl. The
         active state is a blue soft-fill (the one-blue selection rule). -->
    <section
      v-if="!searchActive && memories.length > 0"
      class="mb-6 flex flex-wrap items-center gap-1.5"
    >
      <button
        v-for="layer in MEMORY_LAYERS"
        :key="layer"
        type="button"
        :class="[
          'rounded-full px-2.5 py-0.5 text-caption font-medium transition-colors',
          activeLayer === layer
            ? 'bg-[var(--accent-blue-soft-active)] text-[var(--accent-blue-deep)]'
            : 'bg-[var(--accent-gray-soft-hover)] text-muted-foreground',
        ]"
        @click="activeLayer = layer"
      >
        {{ $t(`bots.memory.layer.${layer}`) }}
        <span class="ml-1 opacity-60">{{ layerCounts[layer] }}</span>
      </button>
    </section>

    <!-- Loading skeleton: one block mirroring the section-card frame so the
         swap does not jump. -->
    <Skeleton
      v-if="loading && memories.length === 0"
      class="h-44 w-full rounded-menu-shell"
    />

    <!-- Load failure: an honest error state. Without it a failed fetch renders
         the "no memories yet" empty state, which lies about the bot's data. -->
    <SettingsSection v-else-if="loadError">
      <Empty class="py-12">
        <EmptyHeader>
          <EmptyTitle>{{ $t('common.loadFailed') }}</EmptyTitle>
          <EmptyDescription>{{ loadError }}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    </SettingsSection>

    <!-- Empty: the section card is the frame, so the Empty is borderless,
         no icon-tile (skill: in-card Empty rule). -->
    <SettingsSection v-else-if="memories.length === 0">
      <Empty class="py-12">
        <EmptyHeader>
          <EmptyTitle>{{ $t('bots.memory.emptyTitle') }}</EmptyTitle>
          <EmptyDescription>{{ $t('bots.memory.empty') }}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            @click="openNewMemoryDialog"
          >
            {{ $t('bots.memory.newMemory') }}
          </Button>
        </EmptyContent>
      </Empty>
    </SettingsSection>

    <!-- Search results: matched memories with a match-score badge (mono, per
         the tool-call-detail convention). -->
    <SettingsSection
      v-else-if="searchActive"
      :title="$t('bots.memory.searchResults')"
    >
      <Empty v-if="searchResults.length === 0">
        <EmptyHeader>
          <EmptyDescription>{{ $t('bots.memory.noResults') }}</EmptyDescription>
        </EmptyHeader>
      </Empty>
      <MemoryCard
        v-for="item in searchResults"
        :key="item.id ?? item.memory"
        :item="item"
        :locale="locale"
        :show-score="typeof item.score === 'number'"
        @edit="openEditDialog(item)"
      />
    </SettingsSection>

    <!-- Memory stream: dated groups, newest first; rows are hairline-separated
         inside each section card. -->
    <div
      v-else
      class="space-y-6"
    >
      <SettingsSection
        v-for="group in visibleGroups"
        :key="group.date"
        :title="group.label"
      >
        <MemoryCard
          v-for="item in group.items"
          :key="item.id"
          :item="item"
          :locale="locale"
          :show-timestamp="false"
          @edit="openEditDialog(item)"
        />
      </SettingsSection>
      <div
        v-if="hasMoreVisibleMemories"
        ref="memoryLoadMoreSentinel"
        aria-hidden="true"
        class="h-px"
      />
    </div>

    <!-- Unified create / edit dialog. House form standard: DialogTitle, single
         Textarea, default-height footer buttons. Edit mode adds a destructive
         delete (ConfirmPopover) on the far left of the footer. -->
    <Dialog v-model:open="memoryDialogOpen">
      <DialogScrollContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {{ editingId ? $t('bots.memory.editMemory') : $t('bots.memory.newMemory') }}
          </DialogTitle>
        </DialogHeader>

        <form
          class="space-y-4"
          @submit.prevent="handleSave"
        >
          <FieldStack
            :label="$t('bots.memory.contentLabel')"
            for="memory-content"
          >
            <Textarea
              id="memory-content"
              v-model="dialogContent"
              :placeholder="$t('bots.memory.contentPlaceholder')"
              rows="3"
              class="min-h-[4.5rem] resize-y"
            />
          </FieldStack>

          <DialogFooter class="gap-2 sm:justify-between">
            <ConfirmPopover
              v-if="editingId"
              :message="$t('bots.memory.deleteConfirm')"
              variant="destructive"
              :cancel-text="$t('common.cancel')"
              :confirm-text="$t('common.delete')"
              @confirm="handleDelete"
            >
              <template #trigger>
                <Button
                  variant="ghost"
                  size="icon"
                  type="button"
                  class="text-destructive hover:bg-destructive-soft hover:text-destructive"
                  :aria-label="$t('common.delete')"
                >
                  <Trash2 class="size-4" />
                </Button>
              </template>
            </ConfirmPopover>
            <div
              v-else
              class="flex-1"
            />

            <div class="flex gap-2">
              <DialogClose as-child>
                <Button
                  type="button"
                  variant="outline"
                >
                  {{ $t('common.cancel') }}
                </Button>
              </DialogClose>
              <Button
                type="submit"
                :disabled="!dialogContent.trim()"
                :loading="actionLoading"
              >
                {{ editingId ? $t('common.save') : $t('common.confirm') }}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogScrollContent>
    </Dialog>
  </PageShell>
</template>

<script setup lang="ts">
import { Brain, CalendarDays, Plus, RefreshCw, Search, Trash2, X } from 'lucide-vue-next'
import type { DateRange } from 'reka-ui'
import { DateFormatter, getLocalTimeZone, today } from '@internationalized/date'
import { useIntersectionObserver } from '@vueuse/core'
import { computed, ref, watch } from 'vue'
import {
  Button,
  Textarea,
  CalloutBanner,
  Dialog,
  DialogScrollContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogClose,
  FieldStack,
  FormStack,
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
  Popover,
  PopoverTrigger,
  PopoverContent,
  Label,
  RangeCalendar,
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
  Empty,
  EmptyTitle,
  EmptyDescription,
  EmptyHeader,
  EmptyContent,
  Skeleton,
  selectTriggerClass,
  toast,
} from '@felinic/ui'
import {
  getBotsByBotIdMemory,
  getBotsByBotIdMemoryStatus,
  postBotsByBotIdMemory,
  postBotsByBotIdMemorySearch,
  postBotsByBotIdMemoryIngest,
  putBotsByBotIdMemoryByMemoryId,
  deleteBotsByBotIdMemoryById,
  postBotsByBotIdMemoryCompact,
} from '@memohai/sdk'
import type {
  AdaptersMemoryItem,
  AdaptersMemoryStatusResponse,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, MetricReadout, PageShell, SettingsSection } from '@felinic/ui'
import { formatRelativeTime } from '@/utils/date-time'
import { resolveApiErrorMessage } from '@/utils/api-error'
import MemoryGraph from './memory-graph.vue'
import MemoryCard from './memory-card.vue'
import MemoryAdvancedActions from './memory-advanced-actions.vue'
import { useMemoryGroups } from './use-memory-groups'
import { MEMORY_LAYERS, useMemoryFilter } from './use-memory-filter'

interface MemoryItem {
  id: string
  memory: string
  created_at?: string
  updated_at?: string
  hash?: string
  score?: number
  metadata?: Record<string, unknown>
}

const props = defineProps<{
  botId: string
}>()

const { t, locale } = useI18n()
const loading = ref(false)
const actionLoading = ref(false)
const compactLoading = ref(false)
const ingestLoading = ref(false)
const memories = ref<MemoryItem[]>([])
const memoryStatus = ref<AdaptersMemoryStatusResponse | null>(null)
const memoryStatusLoading = ref(false)
const loadError = ref('')

const graphRef = ref<InstanceType<typeof MemoryGraph> | null>(null)

const compactPopoverOpen = ref(false)
const compactRatio = ref('0.5')
const compactDecayRange = ref<DateRange | undefined>({ start: today(getLocalTimeZone()), end: today(getLocalTimeZone()) })
const compactDecayFormatter = new DateFormatter(locale.value, { dateStyle: 'medium' })
const compactDecayLabel = computed(() => {
  if (!compactDecayRange.value?.start) return ''
  return compactDecayFormatter.format(compactDecayRange.value.start.toDate(getLocalTimeZone()))
})

// Dialog state: shared between create and edit modes.
const memoryDialogOpen = ref(false)
const editingId = ref<string | null>(null)
const dialogContent = ref('')

// Local layer filter + server-side search state.
const memoriesComputed = computed(() => memories.value as unknown as AdaptersMemoryItem[])
const {
  activeLayer,
  searchQuery,
  isSearching,
  filtered,
  layerCounts,
  searchActive,
} = useMemoryFilter(memoriesComputed)

const searchInput = ref('')
const searchResults = ref<MemoryItem[]>([])
let searchDebounce: ReturnType<typeof setTimeout> | null = null
const MEMORY_RENDER_BATCH_SIZE = 30
const visibleMemoryCount = ref(MEMORY_RENDER_BATCH_SIZE)
const memoryLoadMoreSentinel = ref<HTMLElement | null>(null)

const memoriesForGroups = computed(() => filtered.value as unknown as MemoryItem[])
const { groups: filteredGroups, stats } = useMemoryGroups(memoriesForGroups, computed(() => locale.value))
const filteredMemoryCount = computed(() => filteredGroups.value.reduce((count, group) => count + group.items.length, 0))
const visibleGroups = computed(() => {
  let remaining = visibleMemoryCount.value
  const groups: Array<(typeof filteredGroups.value)[number]> = []

  for (const group of filteredGroups.value) {
    if (remaining <= 0) break
    const items = group.items.slice(0, remaining)
    if (items.length > 0) {
      groups.push({ ...group, items })
      remaining -= items.length
    }
  }

  return groups
})
const hasMoreVisibleMemories = computed(() => visibleMemoryCount.value < filteredMemoryCount.value)

const compactCapability = computed(() => memoryStatus.value?.compact)
const canSemanticCompact = computed(() => compactCapability.value?.semantic === true)

const showDegradedBanner = computed(() => {
  const status = memoryStatus.value
  if (!status) return false
  return status.degraded === true || (status.pgvector?.ok === false && (status.source_count ?? 0) > 0)
})

const compactDecayDays = computed(() => {
  if (!compactDecayRange.value?.start) return 0
  const selectedDate = compactDecayRange.value.start.toDate(getLocalTimeZone())
  const todayDate = today(getLocalTimeZone()).toDate(getLocalTimeZone())
  selectedDate.setHours(0, 0, 0, 0)
  todayDate.setHours(0, 0, 0, 0)
  const diffTime = todayDate.getTime() - selectedDate.getTime()
  const diffDays = Math.floor(diffTime / (1000 * 60 * 60 * 24))
  return diffDays > 0 ? diffDays : 0
})

async function loadMemories() {
  const botId = props.botId.trim()
  if (!botId) {
    memories.value = []
    loadError.value = ''
    return
  }

  loading.value = true
  loadError.value = ''
  try {
    const { data } = await getBotsByBotIdMemory({
      path: { bot_id: botId },
      throwOnError: true,
    })
    memories.value = (data.results ?? [])
      .filter((item): item is AdaptersMemoryItem & { id: string; memory: string } =>
        typeof item?.id === 'string' && item.id.length > 0 && typeof item.memory === 'string',
      )
      .map((item) => ({
        id: item.id,
        memory: item.memory,
        created_at: item.created_at,
        updated_at: item.updated_at,
        hash: item.hash,
        score: item.score,
        metadata: item.metadata,
      }))
  } catch (error) {
    console.error('Failed to load memories:', error)
    loadError.value = resolveApiErrorMessage(error, t('common.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function loadMemoryStatus() {
  const botId = props.botId.trim()
  if (!botId) {
    memoryStatus.value = null
    memoryStatusLoading.value = false
    return
  }

  memoryStatusLoading.value = true
  try {
    const { data } = await getBotsByBotIdMemoryStatus({
      path: { bot_id: botId },
      throwOnError: true,
    })
    memoryStatus.value = data ?? null
  } catch (error) {
    // Status failure only hides the compact action and the health banner; the
    // list itself reports its own loadError, so there is nothing to surface.
    console.error('Failed to load memory status:', error)
    memoryStatus.value = null
  } finally {
    memoryStatusLoading.value = false
  }
}

async function handleAdvancedSynced() {
  await Promise.all([loadMemories(), loadMemoryStatus()])
  graphRef.value?.refresh?.()
}

// Debounced server search: fire after the user pauses typing. The layer chips
// hide while a search is active (search results are already query-scoped).
function onSearchInput() {
  if (searchDebounce) clearTimeout(searchDebounce)
  const query = searchInput.value.trim()
  if (!query) {
    clearSearch()
    return
  }
  isSearching.value = true
  searchDebounce = setTimeout(() => {
    void runSearch()
  }, 300)
}

async function runSearch() {
  const query = searchInput.value.trim()
  searchQuery.value = query
  if (!query) {
    searchResults.value = []
    isSearching.value = false
    return
  }
  isSearching.value = true
  try {
    const { data } = await postBotsByBotIdMemorySearch({
      path: { bot_id: props.botId.trim() },
      body: { query, limit: 20 },
      throwOnError: true,
    })
    searchResults.value = (data.results ?? []).filter(
      (item): item is AdaptersMemoryItem & { id: string; memory: string } =>
        typeof item?.memory === 'string',
    ) as MemoryItem[]
  } catch (error) {
    console.error('Failed to search memories:', error)
    toast.error(resolveApiErrorMessage(error, t('bots.memory.searchFailed')))
    searchResults.value = []
  } finally {
    isSearching.value = false
  }
}

function clearSearch() {
  searchInput.value = ''
  searchQuery.value = ''
  searchResults.value = []
  isSearching.value = false
  if (searchDebounce) clearTimeout(searchDebounce)
}

function loadMoreVisibleMemories() {
  if (!hasMoreVisibleMemories.value) return
  visibleMemoryCount.value = Math.min(
    visibleMemoryCount.value + MEMORY_RENDER_BATCH_SIZE,
    filteredMemoryCount.value,
  )
}

async function handleIngest() {
  ingestLoading.value = true
  try {
    const { data } = await postBotsByBotIdMemoryIngest({
      path: { bot_id: props.botId.trim() },
      throwOnError: true,
    })
    toast.success(t('bots.memory.ingestSuccess', { n: data.ingested ?? 0 }))
    await Promise.all([loadMemories(), loadMemoryStatus()])
    graphRef.value?.refresh?.()
  } catch (error) {
    console.error('Failed to ingest memories:', error)
    toast.error(resolveApiErrorMessage(error, t('bots.memory.ingestFailed')))
  } finally {
    ingestLoading.value = false
  }
}

function openNewMemoryDialog() {
  editingId.value = null
  dialogContent.value = ''
  memoryDialogOpen.value = true
}

function openEditDialog(item: MemoryItem) {
  editingId.value = item.id
  dialogContent.value = item.memory
  memoryDialogOpen.value = true
}

async function handleSave() {
  const text = dialogContent.value.trim()
  if (!text) return

  actionLoading.value = true
  try {
    if (editingId.value) {
      // Edit = in-place update via PUT (preserves id, layer, metadata).
      await putBotsByBotIdMemoryByMemoryId({
        path: { bot_id: props.botId, memory_id: editingId.value },
        body: { memory: text },
        throwOnError: true,
      })
    } else {
      await postBotsByBotIdMemory({
        path: { bot_id: props.botId },
        body: { message: text },
        throwOnError: true,
      })
    }

    toast.success(t('bots.memory.saveSuccess'))
    memoryDialogOpen.value = false
    await loadMemories()
    graphRef.value?.refresh?.()
  } catch (error) {
    console.error('Failed to save memory:', error)
    toast.error(t('common.saveFailed'))
  } finally {
    actionLoading.value = false
  }
}

async function handleDelete() {
  if (!editingId.value) return

  actionLoading.value = true
  try {
    await deleteBotsByBotIdMemoryById({
      path: { bot_id: props.botId, id: editingId.value },
      throwOnError: true,
    })
    toast.success(t('bots.memory.deleteSuccess'))
    memoryDialogOpen.value = false
    await loadMemories()
    graphRef.value?.refresh?.()
  } catch (error) {
    console.error('Failed to delete memory:', error)
    toast.error(t('bots.memory.deleteFailed'))
  } finally {
    actionLoading.value = false
  }
}

async function handleCompact() {
  if (!canSemanticCompact.value) return
  compactLoading.value = true
  try {
    await postBotsByBotIdMemoryCompact({
      path: { bot_id: props.botId },
      body: {
        ratio: parseFloat(compactRatio.value),
        decay_days: compactDecayDays.value || undefined,
      },
      throwOnError: true,
    })
    toast.success(t('bots.memory.compactSuccess'))
    compactPopoverOpen.value = false
    await loadMemories()
  } catch (error) {
    console.error('Failed to compact memory:', error)
    toast.error(resolveApiErrorMessage(error, t('bots.memory.compactFailed')))
  } finally {
    compactLoading.value = false
  }
}

watch(() => props.botId, () => {
  memories.value = []
  loadError.value = ''
  clearSearch()
  activeLayer.value = 'all'
  void loadMemories()
  void loadMemoryStatus()
}, { immediate: true })

watch(filteredGroups, () => {
  visibleMemoryCount.value = MEMORY_RENDER_BATCH_SIZE
}, { flush: 'sync' })

useIntersectionObserver(
  memoryLoadMoreSentinel,
  ([entry]) => {
    if (!entry?.isIntersecting) return
    loadMoreVisibleMemories()
  },
  {
    rootMargin: '0px 0px 480px 0px',
    threshold: 0,
  },
)
</script>
