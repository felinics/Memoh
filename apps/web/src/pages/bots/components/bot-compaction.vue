<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { Box } from 'lucide-vue-next'
import {
  ActionCard, Button, Badge, Dialog, DialogBody, DialogDescription, DialogHeader, DialogPanel, DialogTitle,
  Empty, EmptyDescription, EmptyHeader, EmptyTitle, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, Switch, Input, Label,
  Pagination, PaginationContent, PaginationEllipsis,
  PaginationFirst, PaginationItem, PaginationLast,
  PaginationNext, PaginationPrevious,
} from '@felinic/ui'
import ModelSelect from './model-select.vue'
import { filterCompactionModels } from './compaction-models'
import {
  compactionTargetPercentAfterToggle,
  isCompactionTargetPercentInvalid,
} from './compaction-target'
import {
  getBotsByBotIdSettings, putBotsByBotIdSettings,
  getBotsByBotIdCompactionLogs, deleteBotsByBotIdCompactionLogs,
  getModels, getProviders,
} from '@memohai/sdk'
import type { SettingsSettings, SettingsUpsertRequest, CompactionLog } from '@memohai/sdk'
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
const compactionModels = computed(() => filterCompactionModels(models.value, providers.value))

// ---- Settings (autosaved) ----
// No Save button by design (web skill §8); same autosave contract as
// bot-settings.vue. Number inputs are free-typing drafts committed on
// blur/Enter so autosave fires once per edit, never per keystroke; an invalid
// draft never enters `form` (it reverts on commit), which replaces the old
// "Save button stays disabled while invalid" gate.
// A type alias (not interface) so the record satisfies the queue's
// Record<string, unknown> constraint.
type CompactionForm = {
  compaction_enabled: boolean
  compaction_threshold: number
  compaction_target_percent: number | null
  compaction_model_id: string
}

const form = reactive<CompactionForm>({
  compaction_enabled: false,
  compaction_threshold: 0,
  compaction_target_percent: null,
  compaction_model_id: '',
})

// Last-known-server snapshot; see bot-settings.vue for the full contract. Any
// non-user write to `form` (hydration, rollback) must advance `synced` in the
// same block or the diff misreads it as an edit.
const synced = reactive<CompactionForm>({ ...form })

watch(settings, (val: SettingsSettings | undefined) => {
  if (!val) return
  const next: CompactionForm = {
    compaction_enabled: val.compaction_enabled ?? false,
    compaction_threshold: val.compaction_threshold ?? 0,
    compaction_target_percent: val.compaction_target_percent ?? null,
    compaction_model_id: val.compaction_model_id ?? '',
  }
  // Per-field guard: a refetch landing mid-edit must not clobber it.
  for (const key of Object.keys(next) as (keyof CompactionForm)[]) {
    if (form[key] === synced[key]) form[key] = next[key] as never
    synced[key] = next[key] as never
  }
}, { immediate: true })

const advancedOpen = ref(false)

// Threshold draft: commits on blur/Enter; non-integer or negative reverts.
const thresholdDraft = ref(String(form.compaction_threshold))
const thresholdFocused = ref(false)

watch(() => form.compaction_threshold, (value) => {
  if (!thresholdFocused.value) thresholdDraft.value = String(value)
})

function commitThresholdDraft() {
  const parsed = Number(thresholdDraft.value.trim())
  if (!Number.isInteger(parsed) || parsed < 0) {
    thresholdDraft.value = String(form.compaction_threshold)
    return
  }
  form.compaction_threshold = parsed
  thresholdDraft.value = String(form.compaction_threshold)
}

// Target-percent draft: '' means "use the default" (null → backend clears the
// override). The inline error is live typing feedback; the value only reaches
// `form` via commit, so an invalid draft can never be saved.
const percentDraft = ref(form.compaction_target_percent === null ? '' : String(form.compaction_target_percent))
const percentFocused = ref(false)

watch(() => form.compaction_target_percent, (value) => {
  if (!percentFocused.value) percentDraft.value = value === null ? '' : String(value)
})

const parsedPercentDraft = computed(() => {
  const raw = percentDraft.value.trim()
  if (raw === '') return null
  const parsed = Number(raw)
  return Number.isNaN(parsed) ? null : parsed
})

const compactionTargetPercentInvalid = computed(() => {
  if (percentDraft.value.trim() === '') return false
  const parsed = Number(percentDraft.value.trim())
  return Number.isNaN(parsed) || isCompactionTargetPercentInvalid(parsed)
})

function commitPercentDraft() {
  if (compactionTargetPercentInvalid.value) {
    percentDraft.value = form.compaction_target_percent === null ? '' : String(form.compaction_target_percent)
    return
  }
  form.compaction_target_percent = parsedPercentDraft.value
  percentDraft.value = form.compaction_target_percent === null ? '' : String(form.compaction_target_percent)
}

// Logs context follows the SAVED state, not an in-flight edit: the panel must
// describe what is actually running.
const savedEnabled = computed(() => synced.compaction_enabled)

function updateCompactionEnabled(value: boolean) {
  // Toggling off drops an invalid in-progress draft (helper returns the saved
  // value); a valid draft is kept so the toggle doesn't eat typing.
  const kept = compactionTargetPercentAfterToggle(value, parsedPercentDraft.value, form.compaction_target_percent)
  if (kept !== parsedPercentDraft.value) {
    percentDraft.value = kept === null ? '' : String(kept)
  }
  form.compaction_enabled = value
}

function buildJobs(changed: (keyof CompactionForm)[]): AutosaveJob<CompactionForm>[] {
  const payload: SettingsUpsertRequest = {}
  const sent: Partial<CompactionForm> = {}
  for (const key of changed) {
    sent[key] = form[key] as never
    // Backend clears the override on any out-of-range value when the field is
    // explicitly sent (1-99 normalizes, everything else → NULL), so the null
    // "use default" state travels as 0; omitting the key would mean "keep".
    ;(payload as Record<string, unknown>)[key] = key === 'compaction_target_percent'
      ? (form.compaction_target_percent ?? 0)
      : form[key]
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

useAutosaveQueue<CompactionForm>({
  form,
  synced,
  buildJobs,
  onDrained: () => queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] }),
})

// ---- Logs ----
const isLoading = ref(false)
const isClearing = ref(false)
const logs = ref<CompactionLog[]>([])
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
  if (status === 'pending') return 'default' as const
  return 'destructive' as const
}

function statusLabel(status: string | undefined) {
  if (status === 'ok') return t('bots.compaction.statusOk')
  if (status === 'pending') return t('bots.compaction.statusPending')
  return t('bots.compaction.statusError')
}

function formatDuration(startedAt: string | undefined, completedAt: string | null | undefined) {
  if (!startedAt || !completedAt) return '—'
  const ms = new Date(completedAt).getTime() - new Date(startedAt).getTime()
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
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
    const { data } = await getBotsByBotIdCompactionLogs({
      path: { bot_id: props.botId },
      query: { limit: PAGE_SIZE, offset },
      throwOnError: true,
    })
    logs.value = data?.items ?? []
    totalCount.value = data?.total_count ?? 0
  } catch (error) {
    if (!silent) toast.error(resolveApiErrorMessage(error, t('bots.compaction.loadFailed')))
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
    await deleteBotsByBotIdCompactionLogs({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    logs.value = []
    totalCount.value = 0
    expandedIds.value.clear()
    toast.success(t('bots.compaction.clearSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.compaction.clearFailed')))
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

<template>
  <PageShell
    variant="tab"
    :title="$t('bots.tabs.compaction')"
  >
    <div class="space-y-8">
      <SettingsSection :title="$t('bots.compaction.settingsTitle')">
        <SettingsRow
          :label="$t('bots.settings.compactionEnabled')"
          :description="$t('bots.settings.compactionDescription')"
        >
          <Switch
            :model-value="form.compaction_enabled"
            @update:model-value="updateCompactionEnabled"
          />
        </SettingsRow>

        <template v-if="form.compaction_enabled">
          <SettingsRow stack="sm">
            <template #content>
              <Label for="compaction-threshold">
                {{ $t('bots.settings.compactionThreshold') }}
              </Label>
            </template>
            <Input
              id="compaction-threshold"
              :model-value="thresholdDraft"
              type="number"
              :min="0"
              :step="1"
              placeholder="0"
              size="sm"
              class="w-32 tabular-nums"
              @update:model-value="(value) => thresholdDraft = String(value ?? '')"
              @focus="thresholdFocused = true"
              @change="commitThresholdDraft"
              @blur="thresholdFocused = false; commitThresholdDraft()"
              @keydown.enter="commitThresholdDraft"
            />
          </SettingsRow>

          <SettingsRow
            stack="sm"
          >
            <template #content>
              <Label for="compaction-target-percent">
                {{ $t('bots.settings.compactionTargetPercent') }}
              </Label>
              <p
                id="compaction-target-percent-description"
                class="mt-0.5 text-body text-muted-foreground"
              >
                {{ $t('bots.settings.compactionTargetPercentDescription') }}
              </p>
              <p
                v-if="compactionTargetPercentInvalid"
                id="compaction-target-percent-error"
                class="mt-1 text-body text-destructive"
              >
                {{ $t('bots.settings.compactionTargetPercentInvalid') }}
              </p>
            </template>
            <Input
              id="compaction-target-percent"
              :model-value="percentDraft"
              type="number"
              :min="1"
              :max="99"
              :step="1"
              placeholder="40"
              size="sm"
              class="w-32 tabular-nums"
              :aria-describedby="compactionTargetPercentInvalid
                ? 'compaction-target-percent-description compaction-target-percent-error'
                : 'compaction-target-percent-description'"
              :aria-invalid="compactionTargetPercentInvalid"
              @update:model-value="(value) => percentDraft = String(value ?? '')"
              @focus="percentFocused = true"
              @change="commitPercentDraft"
              @blur="percentFocused = false; commitPercentDraft()"
              @keydown.enter="commitPercentDraft"
            />
          </SettingsRow>
        </template>
      </SettingsSection>

      <!-- Model override is a power-user facet (defaults to the bot's chat
           model), so it lives behind a named ActionCard entry opening a
           focused dialog — the house replacement for the old in-card
           "Advanced" expand row. The dialog edits the same autosaved form, so
           a selection persists the moment it is made. -->
      <section
        v-if="form.compaction_enabled"
        class="space-y-2.5"
      >
        <h2 class="px-2 text-label font-medium text-muted-foreground">
          {{ $t('bots.compaction.advanced') }}
        </h2>
        <!-- Slim single-line entry, per the ActionCard contract: NO description
             (it would grow the row past the 48px rung) — the dialog's own
             DialogDescription carries the explanation. Box = the house icon
             for "model" (providers page: Boxes count / Box empty state). -->
        <ActionCard
          :title="$t('bots.settings.compactionModel')"
          @click="advancedOpen = true"
        >
          <template #icon>
            <Box />
          </template>
        </ActionCard>
      </section>

      <SettingsSection :title="$t('bots.compaction.title')">
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
                {{ $t('bots.compaction.filterAll') }}
              </SelectItem>
              <SelectItem value="ok">
                {{ $t('bots.compaction.statusOk') }}
              </SelectItem>
              <SelectItem value="pending">
                {{ $t('bots.compaction.statusPending') }}
              </SelectItem>
              <SelectItem value="error">
                {{ $t('bots.compaction.statusError') }}
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
            <EmptyTitle>{{ $t('bots.compaction.logsDisabledTitle') }}</EmptyTitle>
            <EmptyDescription>{{ $t('bots.compaction.logsDisabledHint') }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <Empty
          v-else-if="!isLoading && totalCount === 0"
          class="py-12"
        >
          <EmptyHeader>
            <EmptyTitle>{{ $t('bots.compaction.empty') }}</EmptyTitle>
            <EmptyDescription>{{ $t('bots.compaction.emptyHint') }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <Empty
          v-else-if="filteredLogs.length === 0"
          class="py-12"
        >
          <EmptyHeader>
            <EmptyTitle>{{ $t('bots.compaction.empty') }}</EmptyTitle>
            <EmptyDescription>{{ $t('bots.compaction.filterEmpty') }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <template v-else>
          <div class="overflow-x-auto">
            <table class="w-full text-xs">
              <thead>
                <tr class="border-b border-border">
                  <th class="px-4 py-2.5 text-left font-medium text-muted-foreground">
                    {{ $t('bots.compaction.status') }}
                  </th>
                  <th class="px-4 py-2.5 text-left font-medium text-muted-foreground">
                    {{ $t('bots.compaction.time') }}
                  </th>
                  <th class="px-4 py-2.5 text-left font-medium text-muted-foreground">
                    {{ $t('bots.compaction.duration') }}
                  </th>
                  <th class="px-4 py-2.5 text-left font-medium text-muted-foreground">
                    {{ $t('bots.compaction.error') }}
                  </th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border">
                <template
                  v-for="log in filteredLogs"
                  :key="log.id"
                >
                  <tr
                    class="cursor-pointer transition-colors hover:bg-accent"
                    @click="toggleExpand(log.id)"
                  >
                    <td class="px-4 py-3">
                      <Badge
                        :variant="statusVariant(log.status)"
                        size="sm"
                      >
                        {{ statusLabel(log.status) }}
                      </Badge>
                    </td>
                    <td class="px-4 py-3 font-mono text-muted-foreground">
                      {{ formatDateTime(log.started_at) }}
                    </td>
                    <td class="px-4 py-3 font-mono text-muted-foreground">
                      {{ formatDuration(log.started_at, log.completed_at) }}
                    </td>
                    <td class="px-4 py-3">
                      <span
                        v-if="log.error_message"
                        class="block max-w-[200px] truncate text-destructive"
                      >{{ log.error_message }}</span>
                      <span
                        v-else
                        class="text-muted-foreground"
                      >—</span>
                    </td>
                  </tr>
                  <tr
                    v-if="log.id && expandedIds.has(log.id)"
                    class="border-t border-border"
                  >
                    <td
                      colspan="4"
                      class="px-4 py-4"
                    >
                      <div class="space-y-3">
                        <div
                          v-if="log.error_message"
                          class="rounded-md border border-border bg-card p-3"
                        >
                          <p class="whitespace-pre-wrap font-mono text-xs text-destructive">
                            {{ log.error_message }}
                          </p>
                        </div>
                        <div
                          v-if="log.usage"
                          class="space-y-1"
                        >
                          <span class="text-xs font-medium text-muted-foreground">{{ $t('common.usage') }}</span>
                          <div class="whitespace-pre-wrap rounded-md border border-border bg-card p-3 font-mono text-xs text-muted-foreground">
                            {{ JSON.stringify(log.usage, null, 2) }}
                          </div>
                        </div>
                      </div>
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>

          <div
            v-if="totalPages > 1"
            class="flex items-center justify-between border-t border-border p-4"
          >
            <span class="text-xs tabular-nums text-muted-foreground">
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
        </template>
      </SettingsSection>

      <SettingsSection
        v-if="logs.length > 0"
        :title="$t('common.dangerZone')"
      >
        <SettingsRow
          :label="$t('bots.compaction.clearLogs')"
          :description="$t('bots.compaction.clearConfirm')"
        >
          <ConfirmPopover
            :message="$t('bots.compaction.clearConfirm')"
            :loading="isClearing"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('bots.compaction.clearLogs')"
            @confirm="handleClear"
          >
            <template #trigger>
              <Button
                variant="destructive"
                size="sm"
                :loading="isClearing"
              >
                {{ $t('bots.compaction.clearLogs') }}
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
        <DialogTitle>{{ $t('bots.settings.compactionModel') }}</DialogTitle>
        <DialogDescription>{{ $t('bots.settings.compactionModelDescription') }}</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <ModelSelect
          v-model="form.compaction_model_id"
          :models="compactionModels"
          :providers="providers"
          model-type="chat"
          :placeholder="$t('bots.settings.compactionModelPlaceholder')"
          :none-label="$t('bots.settings.compactionModelPlaceholder')"
          class="w-full"
        />
      </DialogBody>
    </DialogPanel>
  </Dialog>
</template>
