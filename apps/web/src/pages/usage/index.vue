<template>
  <PageShell :title="$t('usage.title')">
    <div class="space-y-8">
      <SettingsSection :title="$t('usage.filters')">
        <div class="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <p class="text-xs text-muted-foreground">
              {{ $t('usage.selectBot') }}
            </p>
            <BotSelect
              v-model="selectedBotId"
              trigger-class="w-full"
              :placeholder="$t('usage.selectBotPlaceholder')"
            />
          </div>

          <div class="space-y-1.5">
            <p class="text-xs text-muted-foreground">
              {{ $t('usage.timeRange') }}
            </p>
            <DateRangePicker
              v-model="customDateRange"
              :presets="datePresets"
              :locale="locale"
              :placeholder="$t('usage.customRangePlaceholder')"
            />
          </div>

          <div class="space-y-1.5">
            <p class="text-xs text-muted-foreground">
              {{ $t('usage.sessionType') }}
            </p>
            <Select v-model="selectedSessionType">
              <SelectTrigger class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">
                  {{ $t('usage.allTypes') }}
                </SelectItem>
                <SelectItem value="chat">
                  {{ $t('usage.chat') }}
                </SelectItem>
                <SelectItem value="discuss">
                  {{ $t('usage.discuss') }}
                </SelectItem>
                <SelectItem value="acp_agent">
                  {{ $t('usage.acpAgent') }}
                </SelectItem>
                <SelectItem value="heartbeat">
                  {{ $t('usage.heartbeat') }}
                </SelectItem>
                <SelectItem value="schedule">
                  {{ $t('usage.schedule') }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div class="space-y-1.5">
            <p class="text-xs text-muted-foreground">
              {{ $t('usage.filterByModel') }}
            </p>
            <Select v-model="selectedModelId">
              <SelectTrigger class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">
                  {{ $t('usage.allModels') }}
                </SelectItem>
                <SelectItem
                  v-for="m in modelOptions"
                  :key="m.model_id"
                  :value="m.model_id!"
                >
                  {{ m.model_name || m.model_slug }} ({{ m.provider_name }})
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </SettingsSection>

      <template v-if="!selectedBotId">
        <Empty class="py-12">
          <EmptyHeader>
            <EmptyTitle>{{ $t('usage.selectBotPlaceholder') }}</EmptyTitle>
          </EmptyHeader>
        </Empty>
      </template>

      <template v-else-if="isLoading">
        <div class="flex items-center justify-center py-16">
          <Spinner class="size-6" />
        </div>
      </template>

      <template v-else>
        <section class="space-y-2.5">
          <h2 class="px-2 text-[13px] font-medium text-muted-foreground">
            {{ $t('usage.overview') }}
          </h2>
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <MetricReadout
              :label="$t('usage.totalInputTokens')"
              :value="formatNumber(summary.totalInputTokens)"
            />
            <MetricReadout
              :label="$t('usage.totalOutputTokens')"
              :value="formatNumber(summary.totalOutputTokens)"
            />
            <MetricReadout
              :label="$t('usage.avgCacheHitRate')"
              :value="summary.avgCacheHitRate"
            />
            <MetricReadout
              :label="$t('usage.totalReasoningTokens')"
              :value="formatNumber(summary.totalReasoningTokens)"
            />
          </div>
        </section>

        <template v-if="hasData">
          <SettingsSection
            v-if="byModelData.length > 0"
            :title="$t('usage.modelDistribution')"
          >
            <template #actions>
              <Select v-model="modelChartType">
                <SelectTrigger
                  size="sm"
                  class="h-7 w-24 text-xs"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent align="end">
                  <SelectItem value="pie">
                    {{ $t('usage.chartPie') }}
                  </SelectItem>
                  <SelectItem value="bar">
                    {{ $t('usage.chartBar') }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </template>
            <VChart
              :key="modelChartType"
              class="p-4"
              style="height: 300px; width: 100%"
              :option="modelChartOption"
              autoresize
            />
          </SettingsSection>

          <SettingsSection :title="$t('usage.dailyTokens')">
            <VChart
              class="p-4"
              style="height: 300px; width: 100%"
              :option="dailyTokensOption"
              autoresize
            />
          </SettingsSection>

          <SettingsSection :title="$t('usage.cacheBreakdown')">
            <VChart
              class="p-4"
              style="height: 300px; width: 100%"
              :option="cacheBreakdownOption"
              autoresize
            />
          </SettingsSection>

          <SettingsSection :title="$t('usage.cacheHitRate')">
            <VChart
              class="p-4"
              style="height: 300px; width: 100%"
              :option="cacheHitRateOption"
              autoresize
            />
          </SettingsSection>
        </template>

        <Empty
          v-else
          class="py-12"
        >
          <EmptyHeader>
            <EmptyTitle>{{ $t('usage.noData') }}</EmptyTitle>
          </EmptyHeader>
        </Empty>

        <SettingsSection :title="$t('usage.records')">
          <template
            v-if="recordsPaginationSummary"
            #actions
          >
            <span class="text-xs text-muted-foreground tabular-nums">
              {{ recordsPaginationSummary }}
            </span>
          </template>

          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{{ $t('usage.colTime') }}</TableHead>
                <TableHead>{{ $t('usage.colSessionType') }}</TableHead>
                <TableHead>{{ $t('usage.colModel') }}</TableHead>
                <TableHead>{{ $t('usage.colProvider') }}</TableHead>
                <TableHead class="text-right">
                  {{ $t('usage.colInputTokens') }}
                </TableHead>
                <TableHead class="text-right">
                  {{ $t('usage.colOutputTokens') }}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-if="isRecordsInitialLoading">
                <TableCell
                  :colspan="6"
                  class="p-0"
                >
                  <div class="flex items-center justify-center h-[480px]">
                    <Spinner class="size-6" />
                  </div>
                </TableCell>
              </TableRow>
              <TableRow v-else-if="recordsList.length === 0">
                <TableCell
                  :colspan="6"
                  class="p-0"
                >
                  <div class="flex items-center justify-center h-[480px] text-muted-foreground">
                    {{ $t('usage.noRecords') }}
                  </div>
                </TableCell>
              </TableRow>
              <template v-else>
                <TableRow
                  v-for="r in recordsList"
                  :key="r.id"
                  :class="isRecordsFetching ? 'opacity-60 transition-opacity' : 'transition-opacity'"
                >
                  <TableCell class="whitespace-nowrap text-muted-foreground">
                    {{ formatDateTimeShort(r.created_at, { locale }) }}
                  </TableCell>
                  <TableCell>{{ sessionTypeLabel(r.session_type) }}</TableCell>
                  <TableCell>{{ recordModelLabel(r) }}</TableCell>
                  <TableCell class="text-muted-foreground">
                    {{ r.provider_name || '-' }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ formatNumber(r.input_tokens ?? 0) }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ formatNumber(r.output_tokens ?? 0) }}
                  </TableCell>
                </TableRow>
              </template>
            </TableBody>
          </Table>
        </SettingsSection>

        <!-- Pagination lives OUTSIDE the section card: it's the table's pager, not
             a row of the card. Kept justify-end below the card so it doesn't fight
             the card's edge; the record-count summary moved up into the section
             #actions header. -->
        <div
          v-if="recordsTotalPages > 1"
          class="flex justify-end"
        >
          <Pagination
            :total="recordsTotal"
            :items-per-page="RECORDS_PAGE_SIZE"
            :sibling-count="1"
            :page="recordsPageNumber"
            show-edges
            @update:page="setRecordsPage"
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
                  :is-active="item.value === recordsPageNumber"
                />
              </template>
              <PaginationNext />
              <PaginationLast />
            </PaginationContent>
          </Pagination>
        </div>
      </template>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart, BarChart, PieChart } from 'echarts/charts'
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
} from 'echarts/components'
import VChart from 'vue-echarts'
import { useDark } from '@vueuse/core'
import { getLocalTimeZone, parseDate, today, type DateValue } from '@internationalized/date'
import {
  type DateRange,
  DateRangePicker,
  type DateRangePreset,
  Empty,
  EmptyHeader,
  EmptyTitle,
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationFirst,
  PaginationItem,
  PaginationLast,
  PaginationNext,
  PaginationPrevious,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@felinic/ui'
import { getBotsQuery } from '@memohai/sdk/colada'
import { getBotsByBotIdTokenUsage, getBotsByBotIdTokenUsageRecords } from '@memohai/sdk'
import { MetricReadout, PageShell, SettingsSection } from '@felinic/ui'
import BotSelect from '@/components/bot-select/index.vue'
import { useChatSelectionStore } from '@/store/chat-selection'
import type { HandlersDailyTokenUsage, HandlersModelTokenUsage, HandlersTokenUsageRecord } from '@memohai/sdk'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { formatDateTimeShort } from '@/utils/date-time'

use([CanvasRenderer, LineChart, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent])

const { t, locale } = useI18n()

const selectedBotId = useSyncedQueryParam('bot', '')
const selectedModelId = useSyncedQueryParam('model', 'all')
const selectedSessionType = useSyncedQueryParam('type', 'all')
const recordsPage = useSyncedQueryParam('rpage', '1')
const modelChartType = ref('pie')

const RECORDS_PAGE_SIZE = 20

function daysAgo(days: number): string {
  const d = new Date()
  d.setDate(d.getDate() - days + 1)
  return formatDate(d)
}

function tomorrow(): string {
  const d = new Date()
  d.setDate(d.getDate() + 1)
  return formatDate(d)
}

const dateFrom = useSyncedQueryParam('from', daysAgo(7))
const dateTo = useSyncedQueryParam('to', tomorrow())

function parseDateValue(value: string): DateValue | undefined {
  if (!value) return undefined
  try {
    return parseDate(value)
  }
  catch {
    return undefined
  }
}

// The picker speaks an inclusive DateRange (@internationalized/date); the query
// keeps plain YYYY-MM-DD strings where `to` is EXCLUSIVE (the day after the last
// counted day, as `allDays` consumes it). Map between the two here so a preset
// and a hand-picked range agree: read end = to - 1 day, write to = end + 1 day.
// Only write back once BOTH ends are chosen, so an in-progress pick never fires
// a half-open query.
const customDateRange = ref<DateRange>()

watch([dateFrom, dateTo], ([from, to]) => {
  const start = parseDateValue(from)
  const endExclusive = parseDateValue(to)
  const end = endExclusive ? endExclusive.subtract({ days: 1 }) : undefined
  customDateRange.value = start || end ? { start, end } : undefined
}, { immediate: true })

watch(customDateRange, (value) => {
  if (value?.start && value?.end) {
    const nextFrom = value.start.toString()
    const nextTo = value.end.add({ days: 1 }).toString()
    if (nextFrom !== dateFrom.value) dateFrom.value = nextFrom
    if (nextTo !== dateTo.value) dateTo.value = nextTo
  }
})

// Quick presets for the range picker: "Last N days" is the inclusive range
// [today-(N-1), today]; matching one of these makes the trigger read its name.
function presetRange(days: number): DateRange {
  const end = today(getLocalTimeZone())
  return { start: end.subtract({ days: days - 1 }), end }
}

const datePresets = computed<DateRangePreset[]>(() => [
  { label: t('usage.last7Days'), range: presetRange(7) },
  { label: t('usage.last30Days'), range: presetRange(30) },
  { label: t('usage.last90Days'), range: presetRange(90) },
])

const { data: botData } = useQuery(getBotsQuery())
const botList = computed(() => botData.value?.items ?? [])

// Default to the bot already active on the chat page (persisted in chat-selection),
// so opening Usage continues looking at whatever the user was just chatting with.
// Fall back to the first bot only when there's no active bot or it's gone. A
// ?bot= param or a manual pick always wins, since both flow through selectedBotId.
const chatSelection = useChatSelectionStore()

watch(botList, (list) => {
  if (selectedBotId.value || list.length === 0) return
  const inherited = chatSelection.currentBotId
  const next = inherited && list.some(bot => bot.id === inherited)
    ? inherited
    : (list[0]?.id ?? '')
  if (next) selectedBotId.value = next
}, { immediate: true })

const modelIdFilter = computed(() =>
  selectedModelId.value === 'all' ? undefined : selectedModelId.value,
)

type UsageBucketType = 'chat' | 'discuss' | 'acp_agent' | 'heartbeat' | 'schedule'
type SessionTypeFilter = UsageBucketType

// Declared before the first useQuery below: Pinia Colada evaluates `key`
// synchronously during setup, so referencing a later `const` would hit the
// temporal dead zone and crash the whole route component.
const sessionTypeFilter = computed(() =>
  selectedSessionType.value === 'all' ? null : selectedSessionType.value as SessionTypeFilter,
)

const { data: usageData, asyncStatus, refetch } = useQuery({
  key: () => ['token-usage', selectedBotId.value, dateFrom.value, dateTo.value, modelIdFilter.value ?? '', sessionTypeFilter.value ?? ''],
  query: async () => {
    const { data } = await getBotsByBotIdTokenUsage({
      path: { bot_id: selectedBotId.value },
      query: {
        from: dateFrom.value,
        to: dateTo.value,
        model_id: modelIdFilter.value,
        session_type: sessionTypeFilter.value ?? undefined,
      },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!selectedBotId.value,
})

const isFetching = computed(() => asyncStatus.value === 'loading')
const isLoading = computed(() => isFetching.value && !usageData.value)

onMounted(() => {
  if (selectedBotId.value) {
    refetch()
  }
})

const byModelData = computed<HandlersModelTokenUsage[]>(() => usageData.value?.by_model ?? [])

const modelOptions = computed(() =>
  byModelData.value.filter(m => m.model_id),
)

const recordsPageNumber = computed(() => {
  const parsed = parseInt(recordsPage.value, 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 1
})

const { data: recordsData, asyncStatus: recordsAsyncStatus, refetch: refetchRecords } = useQuery({
  key: () => [
    'token-usage-records',
    selectedBotId.value,
    dateFrom.value,
    dateTo.value,
    modelIdFilter.value ?? '',
    sessionTypeFilter.value ?? '',
    recordsPageNumber.value,
  ],
  query: async () => {
    const { data } = await getBotsByBotIdTokenUsageRecords({
      path: { bot_id: selectedBotId.value },
      query: {
        from: dateFrom.value,
        to: dateTo.value,
        model_id: modelIdFilter.value,
        session_type: sessionTypeFilter.value ?? undefined,
        limit: RECORDS_PAGE_SIZE,
        offset: (recordsPageNumber.value - 1) * RECORDS_PAGE_SIZE,
      },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!selectedBotId.value,
})

const recordsList = computed<HandlersTokenUsageRecord[]>(() => recordsData.value?.items ?? [])
const isRecordsFetching = computed(() => recordsAsyncStatus.value === 'loading')
const isRecordsInitialLoading = computed(() => isRecordsFetching.value && !recordsData.value)
const recordsTotal = computed(() => recordsData.value?.total ?? 0)
const recordsTotalPages = computed(() =>
  Math.max(1, Math.ceil(recordsTotal.value / RECORDS_PAGE_SIZE)),
)

const recordsPaginationSummary = computed(() => {
  const total = recordsTotal.value
  if (total === 0) return ''
  const start = (recordsPageNumber.value - 1) * RECORDS_PAGE_SIZE + 1
  const end = Math.min(recordsPageNumber.value * RECORDS_PAGE_SIZE, total)
  return `${start}-${end} / ${total}`
})

function resetRecordsPage() {
  if (recordsPage.value !== '1') {
    recordsPage.value = '1'
  }
}

watch(
  () => [
    selectedBotId.value,
    dateFrom.value,
    dateTo.value,
    modelIdFilter.value,
    sessionTypeFilter.value,
  ],
  resetRecordsPage,
)

function setRecordsPage(page: number) {
  const clamped = Math.max(1, Math.min(page, recordsTotalPages.value))
  recordsPage.value = String(clamped)
}

function sessionTypeLabel(type: string | undefined): string {
  switch (type) {
    case 'chat': return t('usage.chat')
    case 'discuss': return t('usage.discuss')
    case 'acp_agent': return t('usage.acpAgent')
    case 'heartbeat': return t('usage.heartbeat')
    case 'schedule': return t('usage.schedule')
    default: return type || '-'
  }
}

function recordModelLabel(r: HandlersTokenUsageRecord): string {
  return r.model_name || r.model_slug || '-'
}

onMounted(() => {
  if (selectedBotId.value) {
    refetchRecords()
  }
})

interface TypedDayMaps {
  chat: Map<string, HandlersDailyTokenUsage>
  discuss: Map<string, HandlersDailyTokenUsage>
  acp_agent: Map<string, HandlersDailyTokenUsage>
  heartbeat: Map<string, HandlersDailyTokenUsage>
  schedule: Map<string, HandlersDailyTokenUsage>
}

const usageBucketTypes: UsageBucketType[] = ['chat', 'discuss', 'acp_agent', 'heartbeat', 'schedule']

function buildDayMap(rows: HandlersDailyTokenUsage[] | undefined) {
  const map = new Map<string, HandlersDailyTokenUsage>()
  for (const r of rows ?? []) {
    if (r.day) map.set(r.day, r)
  }
  return map
}

const dayMaps = computed<TypedDayMaps>(() => ({
  chat: buildDayMap(usageData.value?.chat),
  discuss: buildDayMap(usageData.value?.discuss),
  acp_agent: buildDayMap(usageData.value?.acp_agent),
  heartbeat: buildDayMap(usageData.value?.heartbeat),
  schedule: buildDayMap(usageData.value?.schedule),
}))

const activeTypes = computed<UsageBucketType[]>(() => {
  const filter = sessionTypeFilter.value
  if (filter) return [filter]
  return usageBucketTypes
})

const allDays = computed(() => {
  const from = new Date(dateFrom.value + 'T00:00:00')
  const toExclusive = new Date(dateTo.value + 'T00:00:00')
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const end = new Date(Math.min(toExclusive.getTime(), today.getTime() + 86400000))
  const days: string[] = []
  const cursor = new Date(from)
  while (cursor < end) {
    const y = cursor.getFullYear()
    const m = String(cursor.getMonth() + 1).padStart(2, '0')
    const d = String(cursor.getDate()).padStart(2, '0')
    days.push(`${y}-${m}-${d}`)
    cursor.setDate(cursor.getDate() + 1)
  }
  return days
})

const hasData = computed(() => {
  const chat = usageData.value?.chat ?? []
  const discuss = usageData.value?.discuss ?? []
  const acpAgent = usageData.value?.acp_agent ?? []
  const heartbeat = usageData.value?.heartbeat ?? []
  const schedule = usageData.value?.schedule ?? []
  return chat.length > 0 || discuss.length > 0 || acpAgent.length > 0 || heartbeat.length > 0 || schedule.length > 0 || byModelData.value.length > 0
})

const summary = computed(() => {
  const days = allDays.value
  const types = activeTypes.value
  const maps = dayMaps.value
  let totalInput = 0
  let totalOutput = 0
  let totalCacheRead = 0
  let totalReasoning = 0
  for (const day of days) {
    for (const tp of types) {
      const r = maps[tp].get(day)
      if (!r) continue
      totalInput += r.input_tokens ?? 0
      totalOutput += r.output_tokens ?? 0
      totalCacheRead += r.cache_read_tokens ?? 0
      totalReasoning += r.reasoning_tokens ?? 0
    }
  }
  const rate = totalInput > 0 ? ((totalCacheRead / totalInput) * 100).toFixed(1) + '%' : '-'
  return {
    totalInputTokens: totalInput,
    totalOutputTokens: totalOutput,
    avgCacheHitRate: rate,
    totalReasoningTokens: totalReasoning,
  }
})

function modelLabel(m: HandlersModelTokenUsage) {
  return `${m.model_name || m.model_slug} (${m.provider_name})`
}

// echarts paints on a <canvas> and can't read our CSS custom properties (the
// tokens are oklch + nested vars), so resolve each design token to a concrete
// color through a probe element, then rasterize it to a single pixel and read
// the bytes back as rgb/rgba. The pixel round-trip matters: echarts' emphasis
// (hover) state runs the color through a lift that only parses #hex/rgb/rgba/
// hsl — never oklch/color() — so without it a hovered bar/slice paints
// transparent. `void isDark.value` re-runs this when the theme flips.
const isDark = useDark()

const colorCanvas = typeof document !== 'undefined'
  ? document.createElement('canvas').getContext('2d', { willReadFrequently: true })
  : null

function readColor(token: string, fallback: string): string {
  if (typeof document === 'undefined') return fallback
  const probe = document.createElement('span')
  probe.style.color = `var(${token})`
  probe.style.display = 'none'
  document.body.appendChild(probe)
  const resolved = getComputedStyle(probe).color
  probe.remove()
  if (!resolved) return fallback
  if (!colorCanvas) return resolved
  try {
    colorCanvas.clearRect(0, 0, 1, 1)
    colorCanvas.fillStyle = '#000'
    colorCanvas.fillStyle = resolved
    colorCanvas.fillRect(0, 0, 1, 1)
    const [r = 0, g = 0, b = 0, a = 255] = colorCanvas.getImageData(0, 0, 1, 1).data
    return a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`
  }
  catch {
    return fallback
  }
}

// Black / white / grey only. `--primary` is a violet here, so the charts use the
// neutral foreground (primary series) + muted-foreground (secondary), with the
// card color separating pie slices. No brand or accent color anywhere.
const chartTheme = computed(() => {
  void isDark.value
  const fontFamily = typeof document !== 'undefined'
    ? getComputedStyle(document.body).fontFamily
    : 'inherit'
  return {
    bar: readColor('--foreground', '#18181b'),
    barMuted: readColor('--muted-foreground', '#a1a1aa'),
    text: readColor('--muted-foreground', '#a1a1aa'),
    line: readColor('--border', '#e4e4e7'),
    surface: readColor('--card', '#ffffff'),
    pie: [
      readColor('--foreground', '#18181b'),
      readColor('--muted-foreground', '#a1a1aa'),
      readColor('--border', '#e4e4e7'),
    ],
    fontFamily,
  }
})

// Tooltip surface mirrors Popover/HoverCard exactly: same shell radius, hairline
// and dropdown shadow, with echarts' own background/border/padding zeroed so
// they don't fight the token CSS.
function tooltipSurface(fontFamily: string) {
  return {
    backgroundColor: 'transparent',
    borderWidth: 0,
    padding: 0,
    extraCssText: [
      'background: var(--popover)',
      'color: var(--popover-foreground)',
      'border: 1px solid var(--border-menu)',
      'border-radius: var(--radius-menu-shell)',
      'box-shadow: var(--shadow-dropdown)',
      'padding: 10px 12px',
      `font-family: ${fontFamily}`,
    ].join('; '),
  }
}

interface AxisTooltipParam {
  seriesName?: string
  value?: number
  color?: string
  axisValueLabel?: string
}

// Shared axis-tooltip body: a header row + one "dot · series · value" row per
// series, colored with popover tokens so it matches the dropdown surface.
function axisTooltipFormatter(format: (v: number) => string) {
  return (params: AxisTooltipParam[] | AxisTooltipParam) => {
    const list = Array.isArray(params) ? params : [params]
    const head = list[0]?.axisValueLabel ?? ''
    const rows = list.map((p) => {
      const val = format(typeof p.value === 'number' ? p.value : 0)
      const dot = `<span style="display:inline-block;width:6px;height:6px;border-radius:9999px;margin-right:7px;background:${p.color ?? 'var(--muted-foreground)'};"></span>`
      return '<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;line-height:1.7;">'
        + `<span style="color:var(--muted-foreground);">${dot}${p.seriesName ?? ''}</span>`
        + `<span style="color:var(--popover-foreground);font-weight:500;font-variant-numeric:tabular-nums;">${val}</span></div>`
    }).join('')
    return '<div style="font-size:var(--text-body);min-width:140px;">'
      + `<div style="color:var(--muted-foreground);margin-bottom:3px;">${head}</div>${rows}</div>`
  }
}

const modelPieOption = computed(() => {
  const c = chartTheme.value
  const data = byModelData.value.map(m => ({
    name: modelLabel(m),
    value: (m.input_tokens ?? 0) + (m.output_tokens ?? 0),
  }))
  return {
    color: c.pie,
    textStyle: { fontFamily: c.fontFamily },
    tooltip: {
      trigger: 'item' as const,
      ...tooltipSurface(c.fontFamily),
      formatter: (params: { name: string, value: number, percent: number }) =>
        '<div style="font-size:var(--text-body);min-width:140px;">'
        + `<div style="color:var(--popover-foreground);font-weight:500;margin-bottom:3px;">${params.name}</div>`
        + '<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;line-height:1.7;">'
        + `<span style="color:var(--muted-foreground);">${t('usage.tokens')}</span>`
        + `<span style="color:var(--popover-foreground);font-weight:500;font-variant-numeric:tabular-nums;">${formatNumber(params.value)} (${params.percent}%)</span></div></div>`,
    },
    legend: {
      orient: 'vertical' as const,
      right: 8,
      top: 'middle' as const,
      icon: 'roundRect' as const,
      itemWidth: 8,
      itemHeight: 8,
      textStyle: { color: c.text, fontFamily: c.fontFamily, fontSize: 11, overflow: 'truncate' as const, width: 130 },
    },
    series: [
      {
        type: 'pie' as const,
        radius: ['52%', '72%'],
        center: ['36%', '50%'],
        avoidLabelOverlap: true,
        itemStyle: {
          borderRadius: 4,
          borderColor: c.surface,
          borderWidth: 2,
        },
        label: { show: false },
        emphasis: { label: { show: false } },
        data,
      },
    ],
  }
})

const modelBarOption = computed(() => {
  const c = chartTheme.value
  const models = byModelData.value
  const names = models.map(m => modelLabel(m))
  const inputLabel = t('usage.inputTokens')
  const outputLabel = t('usage.outputTokens')
  return {
    textStyle: { fontFamily: c.fontFamily },
    tooltip: { trigger: 'axis' as const, ...tooltipSurface(c.fontFamily), formatter: axisTooltipFormatter(formatNumber) },
    legend: {
      data: [inputLabel, outputLabel],
      top: 0,
      icon: 'roundRect' as const,
      itemWidth: 8,
      itemHeight: 8,
      textStyle: { color: c.text, fontFamily: c.fontFamily, fontSize: 11 },
    },
    grid: { left: 8, right: 8, top: 36, bottom: 64, containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: names,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: c.line } },
      axisLabel: { rotate: 30, fontSize: 10, color: c.text, fontFamily: c.fontFamily },
    },
    yAxis: {
      type: 'value' as const,
      axisLine: { show: false },
      splitLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: (v: number) => formatNumber(v) },
    },
    series: [
      { name: inputLabel, type: 'bar' as const, stack: 'tokens', itemStyle: { color: c.bar }, data: models.map(m => m.input_tokens ?? 0) },
      { name: outputLabel, type: 'bar' as const, stack: 'tokens', itemStyle: { color: c.barMuted, borderRadius: [3, 3, 0, 0] as [number, number, number, number] }, data: models.map(m => m.output_tokens ?? 0) },
    ],
  }
})

const modelChartOption = computed(() =>
  modelChartType.value === 'bar' ? modelBarOption.value : modelPieOption.value,
)

const dailyTokensOption = computed(() => {
  const c = chartTheme.value
  const days = allDays.value
  const types = activeTypes.value
  const maps = dayMaps.value
  const totalInputLabel = t('usage.totalInput')
  const totalOutputLabel = t('usage.totalOutput')
  const sumDay = (day: string, field: 'input_tokens' | 'output_tokens') => {
    let sum = 0
    for (const tp of types) sum += maps[tp].get(day)?.[field] ?? 0
    return sum
  }
  return {
    textStyle: { fontFamily: c.fontFamily },
    tooltip: { trigger: 'axis' as const, ...tooltipSurface(c.fontFamily), formatter: axisTooltipFormatter(formatNumber) },
    legend: {
      data: [totalInputLabel, totalOutputLabel],
      bottom: 0,
      itemGap: 16,
      icon: 'roundRect' as const,
      itemWidth: 8,
      itemHeight: 8,
      textStyle: { color: c.text, fontFamily: c.fontFamily, fontSize: 11 },
    },
    grid: { left: 8, right: 8, top: 14, bottom: 40, containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: days,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: (v: string) => v.slice(5) },
    },
    yAxis: {
      type: 'value' as const,
      axisLine: { show: false },
      splitLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: (v: number) => formatNumber(v) },
    },
    series: [
      { name: totalInputLabel, type: 'bar' as const, stack: 'tokens', itemStyle: { color: c.bar }, data: days.map(d => sumDay(d, 'input_tokens')) },
      { name: totalOutputLabel, type: 'bar' as const, stack: 'tokens', itemStyle: { color: c.barMuted, borderRadius: [3, 3, 0, 0] as [number, number, number, number] }, data: days.map(d => sumDay(d, 'output_tokens')) },
    ],
  }
})

const cacheBreakdownOption = computed(() => {
  const c = chartTheme.value
  const days = allDays.value
  const types = activeTypes.value
  const maps = dayMaps.value
  function sumField(day: string, field: 'cache_read_tokens' | 'input_tokens') {
    let total = 0
    for (const tp of types) total += (maps[tp].get(day)?.[field] ?? 0) as number
    return total
  }
  return {
    textStyle: { fontFamily: c.fontFamily },
    tooltip: { trigger: 'axis' as const, ...tooltipSurface(c.fontFamily), formatter: axisTooltipFormatter(formatNumber) },
    legend: {
      data: [t('usage.cacheRead'), t('usage.noCache')],
      bottom: 0,
      itemGap: 16,
      icon: 'roundRect' as const,
      itemWidth: 8,
      itemHeight: 8,
      textStyle: { color: c.text, fontFamily: c.fontFamily, fontSize: 11 },
    },
    grid: { left: 8, right: 8, top: 14, bottom: 40, containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: days,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: (v: string) => v.slice(5) },
    },
    yAxis: {
      type: 'value' as const,
      axisLine: { show: false },
      splitLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: (v: number) => formatNumber(v) },
    },
    series: [
      { name: t('usage.cacheRead'), type: 'bar' as const, stack: 'cache', itemStyle: { color: c.bar }, data: days.map(d => sumField(d, 'cache_read_tokens')) },
      {
        name: t('usage.noCache'),
        type: 'bar' as const,
        stack: 'cache',
        itemStyle: { color: c.barMuted, borderRadius: [3, 3, 0, 0] as [number, number, number, number] },
        data: days.map(d => {
          const totalInput = sumField(d, 'input_tokens')
          const cacheRead = sumField(d, 'cache_read_tokens')
          return Math.max(0, totalInput - cacheRead)
        }),
      },
    ],
  }
})

const cacheHitRateOption = computed(() => {
  const c = chartTheme.value
  const days = allDays.value
  const types = activeTypes.value
  const maps = dayMaps.value
  function sumField(day: string, field: 'cache_read_tokens' | 'input_tokens') {
    let total = 0
    for (const tp of types) total += (maps[tp].get(day)?.[field] ?? 0) as number
    return total
  }
  return {
    textStyle: { fontFamily: c.fontFamily },
    tooltip: { trigger: 'axis' as const, ...tooltipSurface(c.fontFamily), formatter: axisTooltipFormatter((v: number) => `${v.toFixed(1)}%`) },
    grid: { left: 8, right: 8, top: 14, bottom: 24, containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: days,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: (v: string) => v.slice(5) },
    },
    yAxis: {
      type: 'value' as const,
      max: 100,
      axisLine: { show: false },
      splitLine: { lineStyle: { color: c.line } },
      axisLabel: { color: c.text, fontFamily: c.fontFamily, fontSize: 10, formatter: '{value}%' },
    },
    series: [
      {
        name: t('usage.cacheHitRate'),
        type: 'line' as const,
        smooth: true,
        symbol: 'none' as const,
        lineStyle: { color: c.bar, width: 2 },
        itemStyle: { color: c.bar },
        data: days.map(d => {
          const totalInput = sumField(d, 'input_tokens')
          const cacheRead = sumField(d, 'cache_read_tokens')
          return totalInput > 0 ? parseFloat(((cacheRead / totalInput) * 100).toFixed(1)) : 0
        }),
      },
    ],
  }
})

function formatDate(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

function formatNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}
</script>
