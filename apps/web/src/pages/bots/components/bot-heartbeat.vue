<template>
  <PageShell
    variant="tab"
    :title="$t('bots.tabs.heartbeat')"
  >
    <div class="space-y-8">
      <SettingsSection :title="$t('bots.heartbeat.settingsTitle')">
        <SettingsRow
          :label="$t('bots.settings.heartbeatEnabled')"
          :description="$t('bots.settings.heartbeatDescription')"
        >
          <Switch
            :model-value="form.heartbeat_enabled"
            @update:model-value="(val) => form.heartbeat_enabled = !!val"
          />
        </SettingsRow>

        <template v-if="form.heartbeat_enabled">
          <SettingsRow :label="$t('bots.heartbeat.checkEvery')">
            <div class="flex items-center gap-2">
              <!-- Free-typing draft, committed on blur/Enter (appearance-page
                   idiom): a per-keystroke v-model would autosave every digit. -->
              <Input
                :model-value="intervalDraft"
                type="number"
                :min="1"
                placeholder="1440"
                class="h-8 w-20 tabular-nums"
                @update:model-value="(value) => intervalDraft = String(value ?? '')"
                @focus="intervalFocused = true"
                @change="commitIntervalDraft"
                @blur="intervalFocused = false; commitIntervalDraft()"
                @keydown.enter="commitIntervalDraft"
              />
              <span class="text-sm text-muted-foreground">{{ $t('bots.heartbeat.intervalUnit') }}</span>
            </div>
          </SettingsRow>
        </template>
      </SettingsSection>

      <!-- Model override is a power-user facet (defaults to the bot's chat
           model), so it lives behind a named ActionCard entry opening a
           focused dialog — the house replacement for the old in-card
           "Advanced" expand row. The dialog edits the same autosaved form, so
           a selection persists the moment it is made. -->
      <section
        v-if="form.heartbeat_enabled"
        class="space-y-2.5"
      >
        <h2 class="px-2 text-label font-medium text-muted-foreground">
          {{ $t('bots.heartbeat.advanced') }}
        </h2>
        <!-- Slim single-line entry, per the ActionCard contract: NO description
             (it would grow the row past the 48px rung) — the dialog's own
             DialogDescription carries the explanation. Box = the house icon
             for "model" (providers page: Boxes count / Box empty state). -->
        <ActionCard
          :title="$t('bots.settings.heartbeatModel')"
          @click="advancedOpen = true"
        >
          <template #icon>
            <Box />
          </template>
        </ActionCard>
      </section>

      <SettingsSection :title="$t('bots.heartbeat.title')">
        <SettingsRow
          v-if="totalCount > 0"
          :label="$t('common.status')"
        >
          <Select v-model="statusFilter">
            <SelectTrigger class="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                {{ $t('bots.heartbeat.filterAll') }}
              </SelectItem>
              <SelectItem value="ok">
                {{ $t('bots.heartbeat.statusOk') }}
              </SelectItem>
              <SelectItem value="alert">
                {{ $t('bots.heartbeat.statusAlert') }}
              </SelectItem>
              <SelectItem value="error">
                {{ $t('bots.heartbeat.statusError') }}
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>

        <InlineLoadingRow
          v-if="isLoading && logs.length === 0"
          size="md"
          surface="card-row"
        >
          {{ $t('common.loading') }}
        </InlineLoadingRow>

        <Empty
          v-else-if="!isLoading && totalCount === 0 && !savedEnabled"
          class="py-12"
        >
          <EmptyHeader>
            <EmptyTitle>{{ $t('bots.heartbeat.logsDisabledTitle') }}</EmptyTitle>
            <EmptyDescription>{{ $t('bots.heartbeat.logsDisabledHint') }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <Empty
          v-else-if="!isLoading && totalCount === 0"
          class="py-12"
        >
          <EmptyHeader>
            <EmptyTitle>{{ $t('bots.heartbeat.empty') }}</EmptyTitle>
            <EmptyDescription>{{ $t('bots.heartbeat.firstCheckHint', { minutes: savedInterval }) }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <Empty
          v-else-if="filteredLogs.length === 0"
          class="py-12"
        >
          <EmptyHeader>
            <EmptyTitle>{{ $t('bots.heartbeat.empty') }}</EmptyTitle>
            <EmptyDescription>{{ $t('bots.heartbeat.filterEmpty') }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <div v-else>
          <ExpandableSettingsRow
            v-for="log in filteredLogs"
            :key="log.id"
            :open="!!log.id && expandedIds.has(log.id)"
            @update:open="toggleExpand(log.id)"
          >
            <template #content>
              <div class="flex items-center gap-2">
                <Badge
                  :variant="statusVariant(log.status)"
                  size="sm"
                >
                  {{ statusLabel(log.status) }}
                </Badge>
                <span class="text-xs tabular-nums text-muted-foreground">
                  {{ formatDateTime(log.started_at) }}
                </span>
              </div>
              <!-- Preview line only while collapsed; the expanded panel shows the
                   full result, so the truncated echo would be redundant. -->
              <p
                v-if="!expandedIds.has(log.id!)"
                class="mt-1 truncate text-xs"
                :class="log.status === 'error' ? 'text-destructive' : 'text-muted-foreground'"
              >
                {{ log.status === 'error' ? (log.error_message || $t('bots.heartbeat.noResult')) : (truncateText(log.result_text) || $t('bots.heartbeat.noResult')) }}
              </p>
            </template>

            <template #trailing>
              <span class="text-xs tabular-nums text-muted-foreground">
                {{ formatDuration(log.started_at, log.completed_at) }}
              </span>
            </template>

            <template #expanded>
              <div class="space-y-3">
                <div class="overflow-hidden rounded-md border border-border bg-card p-3">
                  <pre class="whitespace-pre-wrap break-all font-mono text-xs leading-relaxed text-foreground">{{ log.result_text || $t('bots.heartbeat.noResult') }}</pre>
                </div>

                <div
                  v-if="log.error_message"
                  class="rounded-md border border-border bg-card p-3"
                >
                  <p class="font-mono text-xs leading-normal text-destructive">
                    {{ log.error_message }}
                  </p>
                </div>

                <div
                  v-if="log.usage"
                  class="flex flex-wrap gap-2"
                >
                  <span
                    v-for="(val, key) in (log.usage as any)"
                    :key="key"
                    class="rounded-sm border border-border px-1.5 py-0.5 text-xs tabular-nums text-muted-foreground"
                  >
                    {{ key }}: {{ val }}
                  </span>
                </div>
              </div>
            </template>
          </ExpandableSettingsRow>
        </div>

        <div
          v-if="totalPages > 1"
          class="flex items-center justify-between border-t border-border p-4"
        >
          <span class="whitespace-nowrap text-xs tabular-nums text-muted-foreground">
            {{ paginationSummary }}
          </span>
          <Pagination
            :total="totalCount"
            :items-per-page="PAGE_SIZE"
            :sibling-count="1"
            :page="currentPage"
            show-edges
            @update:page="currentPage = $event"
          >
            <PaginationContent v-slot="{ items }">
              <PaginationFirst />
              <PaginationPrevious />
              <template
                v-for="(item, index) in items"
                :key="index"
              >
                <PaginationEllipsis
                  v-if="item.type === 'ellipsis'"
                  :index="index"
                />
                <PaginationItem
                  v-else
                  :value="item.value"
                  :is-active="item.value === currentPage"
                />
              </template>
              <PaginationNext />
              <PaginationLast />
            </PaginationContent>
          </Pagination>
        </div>
      </SettingsSection>

      <SettingsSection
        v-if="logs.length > 0"
        :title="$t('common.dangerZone')"
      >
        <SettingsRow
          :label="$t('bots.heartbeat.clearLogs')"
          :description="$t('bots.heartbeat.clearConfirm')"
        >
          <ConfirmPopover
            :message="$t('bots.heartbeat.clearConfirm')"
            :loading="isClearing"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('bots.heartbeat.clearLogs')"
            @confirm="handleClear"
          >
            <template #trigger>
              <Button
                variant="destructive"
                size="sm"
                :disabled="isClearing"
              >
                <Trash2 class="size-4" />
                {{ $t('bots.heartbeat.clearLogs') }}
              </Button>
            </template>
          </ConfirmPopover>
        </SettingsRow>
      </SettingsSection>
    </div>
  </PageShell>

  <!-- Advanced model override dialog (workbench form). Edits the autosaved
       form directly: picking a model (or None, which clears the override)
       saves immediately, so closing the dialog never loses or applies
       anything by itself. -->
  <Dialog v-model:open="advancedOpen">
    <DialogPanel width="lg">
      <DialogHeader>
        <DialogTitle>{{ $t('bots.settings.heartbeatModel') }}</DialogTitle>
        <DialogDescription>{{ $t('bots.settings.heartbeatModelDescription') }}</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <ModelSelect
          v-model="form.heartbeat_model_id"
          :models="models"
          :providers="providers"
          model-type="chat"
          :placeholder="$t('bots.settings.heartbeatModelPlaceholder')"
          :none-label="$t('bots.settings.heartbeatModelPlaceholder')"
          class="w-full"
        />
      </DialogBody>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { Trash2, Box } from 'lucide-vue-next'
import { ref, reactive, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, ExpandableSettingsRow, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import {
  ActionCard, Badge, Button, Dialog, DialogBody, DialogDescription, DialogHeader, DialogPanel, DialogTitle,
  Empty, EmptyDescription, EmptyHeader, EmptyTitle, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, Switch, Input,
  Pagination, PaginationContent, PaginationEllipsis,
  PaginationFirst, PaginationItem, PaginationLast,
  PaginationNext, PaginationPrevious,
} from '@felinic/ui'
import ModelSelect from './model-select.vue'
import {
  getBotsByBotIdSettings, putBotsByBotIdSettings,
  getBotsByBotIdHeartbeatLogs, deleteBotsByBotIdHeartbeatLogs,
  getModels, getProviders,
} from '@memohai/sdk'
import type { SettingsSettings, SettingsUpsertRequest, HeartbeatLog } from '@memohai/sdk'
import { useQuery, useQueryCache } from '@pinia/colada'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { formatDateTime } from '@/utils/date-time'
import { useAutosaveQueue, type AutosaveJob } from '@/composables/use-autosave-queue'
import type { Ref } from 'vue'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const botIdRef = computed(() => props.botId) as Ref<string>

// ---- Settings ----
const queryCache = useQueryCache()

const { data: settings } = useQuery({
  key: () => ['bot-settings', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
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

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])

// ---- Settings (autosaved) ----
// This tab has no Save button by design (web skill §8: a page of toggles and
// selects auto-saves; success stays silent, only errors surface). The queue
// lives in use-autosave-queue.ts; this file owns the heartbeat slice of the
// settings endpoint plus the interval draft-commit idiom.
// A type alias (not interface) so the record satisfies the queue's
// Record<string, unknown> constraint.
type HeartbeatForm = {
  heartbeat_enabled: boolean
  heartbeat_interval: number
  heartbeat_model_id: string
}

const form = reactive<HeartbeatForm>({
  heartbeat_enabled: false,
  heartbeat_interval: 1440,
  heartbeat_model_id: '',
})

// Last-known-server snapshot; see bot-settings.vue for the full contract. Any
// non-user write to `form` (hydration, rollback) must advance `synced` in the
// same block or the diff misreads it as an edit.
const synced = reactive<HeartbeatForm>({ ...form })

watch(settings, (val: SettingsSettings | undefined) => {
  if (!val) return
  const next: HeartbeatForm = {
    heartbeat_enabled: val.heartbeat_enabled ?? false,
    heartbeat_interval: val.heartbeat_interval ?? 1440,
    heartbeat_model_id: val.heartbeat_model_id ?? '',
  }
  // Per-field guard: a refetch landing mid-edit must not clobber it.
  for (const key of Object.keys(next) as (keyof HeartbeatForm)[]) {
    if (form[key] === synced[key]) form[key] = next[key] as never
    synced[key] = next[key] as never
  }
}, { immediate: true })

const advancedOpen = ref(false)

// The interval input is a free-typing draft so autosave fires once per edit,
// not per keystroke. Commit on blur/Enter; an invalid value reverts to the
// last committed one (appearance-page idiom).
const intervalDraft = ref(String(form.heartbeat_interval))
const intervalFocused = ref(false)

watch(() => form.heartbeat_interval, (value) => {
  // Hydration/rollback rewrote the committed value; refresh the draft unless
  // the user is mid-typing (their newer edit owns the box).
  if (!intervalFocused.value) intervalDraft.value = String(value)
})

function commitIntervalDraft() {
  const parsed = Number(intervalDraft.value.trim())
  if (!Number.isInteger(parsed) || parsed < 1) {
    intervalDraft.value = String(form.heartbeat_interval)
    return
  }
  form.heartbeat_interval = parsed
  intervalDraft.value = String(form.heartbeat_interval)
}

// Logs context follows the SAVED state, not an in-flight edit: the panel must
// describe what is actually running.
const savedEnabled = computed(() => synced.heartbeat_enabled)
const savedInterval = computed(() => synced.heartbeat_interval)

function buildJobs(changed: (keyof HeartbeatForm)[]): AutosaveJob<HeartbeatForm>[] {
  const payload: SettingsUpsertRequest = {}
  const sent: Partial<HeartbeatForm> = {}
  for (const key of changed) {
    sent[key] = form[key] as never
    ;(payload as Record<string, unknown>)[key] = form[key]
  }
  return [{
    payload: sent,
    save: async () => {
      await putBotsByBotIdSettings({
        path: { bot_id: botIdRef.value },
        body: payload,
        throwOnError: true,
      })
    },
    onError: (error) => toast.error(resolveApiErrorMessage(error, t('common.saveFailed'))),
  }]
}

useAutosaveQueue<HeartbeatForm>({
  form,
  synced,
  buildJobs,
  onDrained: () => queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] }),
})

const isLoading = ref(false)
const isClearing = ref(false)
const logs = ref<HeartbeatLog[]>([])
const totalCount = ref(0)
const statusFilter = ref('all')
const expandedIds = ref(new Set<string>())
const currentPage = ref(1)

const PAGE_SIZE = 20

const filteredLogs = computed(() => {
  if (statusFilter.value === 'all') return logs.value
  return logs.value.filter(l => l.status === statusFilter.value)
})

const totalPages = computed(() => Math.ceil(totalCount.value / PAGE_SIZE))

const paginationSummary = computed(() => {
  const total = totalCount.value
  if (total === 0) return ''
  const start = (currentPage.value - 1) * PAGE_SIZE + 1
  const end = Math.min(currentPage.value * PAGE_SIZE, total)
  return `${start}-${end} / ${total}`
})

watch(currentPage, () => {
  fetchLogs()
})

function statusVariant(status: string | undefined) {
  if (status === 'ok') return 'secondary' as const
  if (status === 'alert') return 'warning' as const
  return 'destructive' as const
}

function statusLabel(status: string | undefined) {
  if (status === 'ok') return t('bots.heartbeat.statusOk')
  if (status === 'alert') return t('bots.heartbeat.statusAlert')
  return t('bots.heartbeat.statusError')
}

function formatDuration(startedAt: string | undefined, completedAt: string | null | undefined) {
  if (!startedAt || !completedAt) return '—'
  const ms = new Date(completedAt).getTime() - new Date(startedAt).getTime()
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function truncateText(text: string | undefined, maxLen = 120) {
  if (!text) return ''
  if (text === 'HEARTBEAT_OK') return 'HEARTBEAT_OK'
  return text.length > maxLen ? text.slice(0, maxLen) + '…' : text
}

function toggleExpand(id: string | undefined) {
  if (!id) return
  if (expandedIds.value.has(id)) {
    expandedIds.value.delete(id)
  } else {
    expandedIds.value.add(id)
  }
}

// silent = background poll: no loading flicker, no error toast, keep expanded rows.
async function fetchLogs(silent = false) {
  if (!props.botId) return
  if (!silent) isLoading.value = true
  try {
    const offset = (currentPage.value - 1) * PAGE_SIZE
    const { data } = await getBotsByBotIdHeartbeatLogs({
      path: { bot_id: props.botId },
      query: { limit: PAGE_SIZE, offset },
      throwOnError: true,
    })
    logs.value = data?.items ?? []
    totalCount.value = data?.total_count ?? 0
  } catch (error) {
    if (!silent) toast.error(resolveApiErrorMessage(error, t('bots.heartbeat.loadFailed')))
  } finally {
    if (!silent) isLoading.value = false
  }
}

// Logs stream in on their own cadence, so the panel refreshes itself instead of
// asking the user to hit a button: poll quietly while enabled, pause when the tab
// is hidden, and catch up the moment it's visible again.
const POLL_INTERVAL = 15_000
let pollTimer: ReturnType<typeof setInterval> | undefined

function tickPoll() {
  if (document.hidden || !savedEnabled.value) return
  void fetchLogs(true)
}

function onVisibilityChange() {
  if (!document.hidden && savedEnabled.value) void fetchLogs(true)
}

async function handleClear() {
  isClearing.value = true
  try {
    await deleteBotsByBotIdHeartbeatLogs({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    logs.value = []
    totalCount.value = 0
    expandedIds.value.clear()
    toast.success(t('bots.heartbeat.clearSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.heartbeat.clearFailed')))
  } finally {
    isClearing.value = false
  }
}

onMounted(() => {
  fetchLogs()
  pollTimer = setInterval(tickPoll, POLL_INTERVAL)
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>
