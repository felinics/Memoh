<script setup lang="ts">
import { ref, computed, watch, onDeactivated, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { InlineLoadingRow, SectionGroup, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import {
  Button, Badge, Dialog, DialogBody, DialogHeader, DialogPanel, DialogTitle,
  Empty, EmptyDescription, EmptyHeader, EmptyTitle, Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
  Pagination, PaginationContent, PaginationEllipsis,
  PaginationFirst, PaginationItem, PaginationLast,
  PaginationNext, PaginationPrevious,
} from '@felinic/ui'
import { getBotsByBotIdCompactionLogs } from '@memohai/sdk'
import type { CompactionLog } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { formatDateTime } from '@/utils/date-time'

const logsOpen = ref(false)
onDeactivated(() => { logsOpen.value = false })

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()

// ---- Logs ----
const isLoading = ref(false)
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
watch(logsOpen, (open) => { if (open) void fetchLogs() })

async function fetchLogs(silent = false) {
  if (!logsOpen.value) return
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
// asking the user to hit a button: poll quietly, pause when the tab is hidden,
// and catch up the moment it's visible again.
const POLL_INTERVAL = 15_000
let pollTimer: ReturnType<typeof setInterval> | undefined

function tickPoll() {
  if (document.hidden) return
  void fetchLogs(true)
}

function onVisibilityChange() {
  if (logsOpen.value && !document.hidden) void fetchLogs(true)
}

onMounted(() => {
  pollTimer = setInterval(tickPoll, POLL_INTERVAL)
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<template>
  <SectionGroup
    :title="$t('bots.tabs.compaction')"
  >
    <div class="space-y-8">
      <SettingsSection>
        <SettingsRow :label="$t('bots.compaction.title')">
          <Button
            variant="outline"
            size="sm"
            @click="logsOpen = true"
          >
            {{ $t('common.open') }}
          </Button>
        </SettingsRow>
      </SettingsSection>
      <Dialog v-model:open="logsOpen">
        <DialogPanel width="3xl">
          <DialogHeader>
            <DialogTitle>{{ $t('bots.compaction.title') }}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <SettingsSection>
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
          </DialogBody>
        </DialogPanel>
      </Dialog>
    </div>
  </SectionGroup>
</template>
