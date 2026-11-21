<template>
  <!-- Overview is the bot's "lobby", modeled on a real product dashboard:
       where it's reachable (platforms), live runtime telemetry, token usage,
       then memory stats. No filler rows that mirror the left nav — model and
       provider setup live on their own tabs. Health checks stay demoted to an
       issue banner + dialog so a healthy bot reads calm. -->
  <PageShell
    variant="tab"
    :title="$t('bots.tabs.overview')"
  >
    <div class="space-y-8">
      <!-- Issue banner: only when the bot needs attention; opens diagnostics. -->
      <CalloutBanner
        v-if="hasIssue"
        tone="destructive"
        clickable
        :title="issueTitle"
        :description="$t('bots.overview.issueHint')"
        @click="checksOpen = true"
      />

      <!-- Reminders: setup steps the user still needs to do, that no other
           surface already nags about (Platforms owns "connect", the banner owns
           diagnostics). One list, grows as features land; the whole block is
           absent once there's nothing left to do. This is the early-life
           "what's next" that keeps a fresh bot's Overview from feeling empty.
           Row mirrors the Platforms "Connect" layout exactly — a real outline
           Button on the right, not a text affordance — so the two setup nudges
           read as the same kind of action. -->
      <SettingsSection
        v-if="reminders.length > 0"
        :title="$t('bots.overview.remindersTitle')"
      >
        <SettingsRow
          v-for="r in reminders"
          :key="r.key"
          :label="r.title"
          :description="r.hint"
        >
          <Button
            variant="outline"
            size="sm"
            class="shrink-0"
            @click="go(r.tab, r.section)"
          >
            {{ r.action }}
          </Button>
        </SettingsRow>
      </SettingsSection>

      <!-- Platforms is deliberately low-weight: a healthy, connected bot does
           NOT need to be told "you connected Telegram" — the user did that.
           So the block only earns its place when it's actionable: nothing
           connected yet (show the Connect nudge) OR the bot has a check issue
           (surface it so a broken connection is visible). When connected and
           healthy, it's hidden entirely. `check_state` is the aggregate signal
           (channel/model/mcp/container combined), so an issue elsewhere also
           reveals platforms — harmless, since it points at the same diagnostics
           as the banner. Every state holds the same min-height so a cold load
           doesn't make the block jump. -->
      <SettingsSection
        v-if="showPlatforms"
        :title="$t('bots.overview.platformsTitle')"
      >
        <SettingsRow
          v-if="channelsLoading && configuredChannels.length === 0"
        >
          <template #leading>
            <Skeleton class="size-7 rounded-md" />
          </template>
          <template #content>
            <div class="space-y-1.5">
              <Skeleton class="h-3.5 w-40" />
              <Skeleton class="h-3 w-56" />
            </div>
          </template>
        </SettingsRow>

        <SettingsRow
          v-else-if="channelsError && configuredChannels.length === 0"
          :label="$t('common.loadFailed')"
          :description="channelsErrorDetail || undefined"
        >
          <Button
            variant="outline"
            size="sm"
            class="shrink-0"
            @click="refetchChannels()"
          >
            {{ $t('common.retry') }}
          </Button>
        </SettingsRow>

        <SettingsRow
          v-else-if="configuredChannels.length === 0"
          :label="$t('bots.overview.platformsEmpty')"
          :description="$t('bots.overview.platformsEmptyHint')"
        >
          <Button
            variant="outline"
            size="sm"
            class="shrink-0"
            @click="go('channels')"
          >
            {{ $t('bots.overview.connectAction') }}
          </Button>
        </SettingsRow>

        <template v-else>
          <SettingsRow
            v-for="item in configuredChannels"
            :key="item.meta.type"
          >
            <template #leading>
              <span class="flex size-7 items-center justify-center">
                <ChannelIcon
                  :channel="item.meta.type as string"
                  size="1.25em"
                />
              </span>
            </template>
            <template #content>
              <span class="truncate text-sm font-medium text-foreground">
                {{ channelTitle(item.meta) }}
              </span>
            </template>
            <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span
                class="size-1.5 rounded-full"
                :class="item.config?.disabled ? 'bg-muted-foreground/40' : 'bg-success'"
              />
              {{ item.config?.disabled ? $t('bots.channels.configured') : $t('bots.channels.statusActive') }}
            </span>
          </SettingsRow>
        </template>
      </SettingsSection>

      <!-- Runtime: the live operational state of the bot's container — the one
           thing this page can tell the user that they can't already see. It is
           rendered only for container-backed bots; remote runtimes are managed
           outside this container view. Metrics auto-refresh while the container
           is running (see the poll in script).

           NO outer card: wrapping three metric tiles in a SettingsSection frame
           was card-in-card — a big bordered box moated around a single row of
           small boxes, which read as mostly-empty. The tiles ARE the content,
           so they sit directly under the title row. This also reads as "live
           telemetry" rather than a settings group, which is what it is. -->
      <section
        v-if="isContainerBot"
        class="space-y-2.5"
      >
        <!-- Title row: section label + status badge (Running or unavailable note). -->
        <div class="flex items-center gap-2 px-2">
          <h2 class="text-[13px] font-medium text-muted-foreground">
            {{ $t('bots.overview.runtimeTitle') }}
          </h2>
          <Badge
            variant="secondary"
            size="sm"
          >
            {{ runtimeStatusLabel }}
          </Badge>
          <span
            v-if="runtimeSampledAt"
            class="ml-auto text-[11px] tabular-nums text-muted-foreground"
          >
            {{ $t('bots.overview.runtimeSampledAt', { time: runtimeSampledAt }) }}
          </span>
        </div>

        <!-- Metric tiles: always render the three-slot grid so the block keeps
             the same shape whether or not the backend has sampled yet. Missing
             values read as '—'. -->
        <div class="grid grid-cols-3 gap-3">
          <MetricReadout
            v-for="m in runtimeMetricCards"
            :key="m.key"
            :label="m.label"
            :value="m.value"
          />
        </div>

        <p
          v-if="runtimeMetricsNote"
          class="px-2 text-xs text-muted-foreground"
        >
          {{ runtimeMetricsNote }}
        </p>
      </section>

      <!-- Usage: token stat row + daily bar chart — the dashboard's "numbers",
           sitting above memory telemetry so it reads earlier on the page. -->
      <SettingsSection :title="$t('bots.overview.usageTitle')">
        <div class="space-y-4 py-4">
          <div class="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-4">
            <div
              v-for="stat in usageStats"
              :key="stat.key"
            >
              <p class="text-xs text-muted-foreground">
                {{ stat.label }}
              </p>
              <p class="mt-0.5 text-xl font-semibold tabular-nums text-foreground">
                {{ usageLoading ? '—' : stat.value }}
              </p>
            </div>
          </div>

          <div
            v-if="usageLoading"
            class="h-[200px]"
          >
            <Skeleton class="size-full rounded-md" />
          </div>
          <DataChart
            v-else-if="hasUsage"
            type="bar"
            stacked
            :categories="usageDays"
            :series="dailyUsageSeries"
            :height="200"
            :x-axis-label-formatter="formatShortDay"
            :value-formatter="formatNumber"
          />
          <div
            v-else
            class="flex h-[200px] items-center justify-center text-sm text-muted-foreground"
          >
            {{ $t('bots.overview.usageNone') }}
          </div>
        </div>
      </SettingsSection>

      <!-- Memory: lightweight telemetry below usage. Manual sync and path
           metrics live on the Memory tab (Advanced). -->
      <section
        v-if="showMemorySection"
        class="space-y-2.5"
      >
        <div class="flex items-center gap-2 px-2">
          <h2 class="text-[13px] font-medium text-muted-foreground">
            {{ $t('bots.overview.memoryTitle') }}
          </h2>
        </div>

        <div
          v-if="memoryLoading"
          class="grid grid-cols-3 gap-3"
        >
          <Skeleton
            v-for="i in 3"
            :key="i"
            class="h-[4.5rem] rounded-[var(--radius-menu-shell)]"
          />
        </div>

        <template v-else>
          <div class="grid grid-cols-3 gap-3">
            <MetricReadout
              v-for="m in memoryMetricCards"
              :key="m.key"
              :label="m.label"
              :value="m.value"
            />
          </div>

          <p
            v-if="memoryStatsNote"
            class="px-2 text-xs text-muted-foreground"
          >
            {{ memoryStatsNote }}
          </p>
        </template>
      </section>

      <BotChecksPanel
        v-model:open="checksOpen"
        :bot-id="botId"
      />
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { Badge, Button, CalloutBanner, MetricReadout, PageShell, SettingsRow, SettingsSection, Skeleton } from '@felinic/ui'
import {
  getBotsByBotIdSettings,
  getBotsByBotIdMemoryStatus,
  getBotsByBotIdTokenUsage,
  getBotsByBotIdContainer,
  getBotsByBotIdContainerMetrics,
  type HandlersChannelMeta,
  type HandlersDailyTokenUsage,
} from '@memohai/sdk'
import { useBotQuery } from '@/composables/api/useBot'
import { useBotChannels } from '@/composables/api/useBotChannels'
import { useVisiblePoll } from '@/composables/useVisiblePoll'
import BotChecksPanel from './bot-checks-panel.vue'
import ChannelIcon from '@/components/channel-icon/index.vue'
import { DataChart } from '@/components/chart'
import { channelTypeDisplayName } from '@/utils/channel-type-label'
import { useBotStatusMeta } from '@/composables/useBotStatusMeta'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { canManageBotPermissions } from '@/utils/bot-permissions'
import { resolveBotWorkspaceBackend, resolveRuntimeStatusKey, runtimeStatusLabelKey } from '@/utils/bot-workspace'
import { buildRuntimeMetricCards } from '@/utils/format-bytes'
import { formatDateTime } from '@/utils/date-time'

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()

const routeIdentifier = computed(() => route.params.botName as string)
const checksOpen = ref(false)

const { data: bot } = useBotQuery(routeIdentifier)
const botId = computed(() => bot.value?.id ?? '')

const { hasIssue, issueTitle } = useBotStatusMeta(bot, t)

const { data: settings } = useQuery({
  key: () => ['bot-settings', botId.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: botId.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botId.value,
})

const { data: memoryStatus, isLoading: memoryLoading } = useQuery({
  key: () => ['bot-memory-status', botId.value],
  query: async () => {
    const { data } = await getBotsByBotIdMemoryStatus({ path: { bot_id: botId.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botId.value,
})

const canManageBot = computed(() =>
  canManageBotPermissions(bot.value?.current_user_permissions),
)

// Shares one query with bot-channels.vue, so visiting Platforms after
// Overview (or vice versa) reuses the cached probe instead of refetching.
const { data: channels, error: channelsError, isLoading: channelsLoading, refetch: refetchChannels } = useBotChannels(
  botId,
  { enabled: () => canManageBot.value },
)

const channelsErrorDetail = computed(() => (channelsError.value ? resolveApiErrorMessage(channelsError.value, '') : ''))

const configuredChannels = computed(() =>
  (channels.value ?? [])
    .filter((c) => c.configured)
    .sort((a, b) => Number(Boolean(a.config?.disabled)) - Number(Boolean(b.config?.disabled))),
)

// Platforms is low-weight: only shown when nothing is connected yet (Connect
// nudge) or the bot has a check issue (so a broken connection stays visible).
// Connected + healthy hides it — the user knows what they connected.
const showPlatforms = computed(() => canManageBot.value && (configuredChannels.value.length === 0 || hasIssue.value))

function channelTitle(meta: HandlersChannelMeta) {
  return channelTypeDisplayName(t, meta.type, meta.display_name)
}

// Reminders: a single, extensible "do this next" list for setup steps that the
// dedicated surfaces don't already nag about. Platforms (connect) and the issue
// banner (diagnostics) own their own signals, so reminders deliberately covers
// only what's left — today that's "no model". Push a new entry here as features
// land (desktop setup, etc.); each is hidden once its condition clears, and the
// whole block disappears when there's nothing to do. `settings` is undefined
// until loaded, so we only nag once we actually know the model is unset.
interface BotReminder {
  key: string
  title: string
  hint: string
  action: string
  tab: string
  // Optional anchor id within the target tab to scroll to (see go()).
  section?: string
}

const reminders = computed<BotReminder[]>(() => {
  const list: BotReminder[] = []
  if (settings.value && (settings.value.chat_runtime ?? 'model') === 'model' && !settings.value.chat_model_id) {
    list.push({
      key: 'model',
      title: t('bots.overview.reminderModelTitle'),
      hint: t('bots.overview.reminderModelHint'),
      action: t('bots.overview.reminderAction'),
      tab: 'general',
      section: 'interaction',
    })
  }
  return list
})

const showMemorySection = computed(() => !!settings.value?.memory_enabled)

const memoryMetricCards = computed(() => {
  const status = memoryStatus.value
  const formatCount = (n: number | undefined) => (n == null ? '—' : formatNumber(n))
  return [
    {
      key: 'indexed',
      label: t('bots.settings.memoryIndexedEntries'),
      value: formatCount(status?.indexed_count),
    },
    {
      key: 'edges',
      label: t('bots.settings.memoryGraphEdges'),
      value: formatCount(status?.edge_count),
    },
    {
      key: 'sources',
      label: t('bots.settings.memoryMarkdownFiles'),
      value: formatCount(status?.markdown_file_count),
    },
  ]
})

const memoryStatsNote = computed(() => {
  if (memoryLoading.value) return ''
  if (!settings.value?.memory_enabled) return ''
  if (memoryStatus.value) return ''
  return t('bots.overview.memoryNoStats')
})

// --- Runtime: live container state + resource metrics. This is only meaningful
// for container-backed bots. We mirror the detail page's resolution: fetch the
// container record and resolve the backend from its workspace_backend value,
// while legacy records without a value continue to default to container. The
// metrics query (and the whole Runtime block) then gate on that backend. ---

const { data: container, refetch: refetchContainer } = useQuery({
  key: () => ['bot-container-overview', botId.value],
  query: async () => {
    // No throwOnError: a missing or externally managed container record is a
    // normal "no container" signal, not an error to surface.
    const result = await getBotsByBotIdContainer({ path: { bot_id: botId.value } })
    if (result.error !== undefined) return null
    return result.data ?? null
  },
  enabled: () => !!botId.value,
})

const isContainerBot = computed(
  () => resolveBotWorkspaceBackend(container.value?.workspace_backend) === 'container',
)

const { data: containerMetrics, refetch: refetchMetrics } = useQuery({
  key: () => ['bot-container-metrics-overview', botId.value],
  query: async () => {
    const result = await getBotsByBotIdContainerMetrics({ path: { bot_id: botId.value } })
    if (result.error !== undefined) return null
    return result.data ?? null
  },
  enabled: () => !!botId.value && isContainerBot.value,
})

// Is the container's task actually running? Drives both the status dot and
// whether we keep polling — a stopped container produces no live metrics.
const containerRunning = computed(() => {
  if (containerMetrics.value?.status?.task_running != null) {
    return containerMetrics.value.status.task_running
  }
  if (container.value?.task_running != null) return container.value.task_running
  const status = (container.value?.status ?? '').trim().toLowerCase()
  return status === 'running' || status === 'created'
})

const containerPaused = computed(() => resolveRuntimeStatusKey(container.value?.status) === 'paused')

// Poll metrics (and container state) every 10s while visible and running/paused.
// Paused keeps polling so a runtime woken by a message updates without reload.
const POLL_MS = 10_000
useVisiblePoll(
  () => {
    void refetchMetrics()
    void refetchContainer()
  },
  POLL_MS,
  {
    enabled: () => isContainerBot.value && (containerRunning.value || containerPaused.value),
  },
)

// Status label reuses the Container tab's status vocabulary so the two pages
// never disagree on "is it running". Badge is always secondary (neutral gray) —
// green "Running" was too loud for a quiet telemetry row.
const runtimeStatusKey = computed(() => {
  const key = resolveRuntimeStatusKey(container.value?.status)
  if (key === 'exited') return 'stopped'
  if (key === 'unknown' && containerRunning.value) return 'running'
  return key
})

const runtimeStatusLabel = computed(() => t(runtimeStatusLabelKey(runtimeStatusKey.value)))

const runtimeSampledAt = computed(() => {
  const ts = containerMetrics.value?.sampled_at
  return ts ? formatDateTime(ts, { locale: locale.value }) : ''
})

const cpuMetrics = computed(() => containerMetrics.value?.metrics?.cpu)
const memoryMetrics = computed(() => containerMetrics.value?.metrics?.memory)
const storageMetrics = computed(() => containerMetrics.value?.metrics?.storage)

const runtimeHasMetrics = computed(
  () => !!cpuMetrics.value || !!memoryMetrics.value || !!storageMetrics.value,
)

const runtimeMetricCards = computed(() => buildRuntimeMetricCards(containerMetrics.value, t))

// Footnote only when the tile grid is present but nothing has been sampled yet.
const runtimeMetricsNote = computed(() => {
  if (runtimeHasMetrics.value) return ''
  if (containerMetrics.value?.supported === false) {
    return t('bots.container.metricsUnsupported')
  }
  if (!containerRunning.value) return t('bots.overview.runtimeStopped')
  return t('bots.overview.runtimeNoMetrics')
})

// --- Usage: last 30 days of token usage, drawn as a stat row + a daily bar
// chart (same data shape + echarts recipe as the dedicated Usage page). ---

function ymd(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

// The 30 calendar days ending today; doubles as the chart x-axis so empty days
// still render a gap instead of collapsing the timeline.
const usageDays = computed(() => {
  const days: string[] = []
  const cursor = new Date()
  cursor.setHours(0, 0, 0, 0)
  cursor.setDate(cursor.getDate() - 29)
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  while (cursor <= today) {
    days.push(ymd(cursor))
    cursor.setDate(cursor.getDate() + 1)
  }
  return days
})

const { data: tokenUsage, isLoading: usageLoading } = useQuery({
  key: () => ['bot-token-usage-overview', botId.value],
  query: async () => {
    const from = usageDays.value[0] ?? ymd(new Date())
    const end = new Date()
    end.setDate(end.getDate() + 1) // `to` is exclusive
    const { data } = await getBotsByBotIdTokenUsage({
      path: { bot_id: botId.value },
      query: { from, to: ymd(end) },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!botId.value,
})

function buildDayMap(rows: HandlersDailyTokenUsage[] | undefined) {
  const map = new Map<string, HandlersDailyTokenUsage>()
  for (const r of rows ?? []) {
    if (r.day) map.set(r.day, r)
  }
  return map
}

const dayMaps = computed(() => ({
  chat: buildDayMap(tokenUsage.value?.chat),
  schedule: buildDayMap(tokenUsage.value?.schedule),
}))

const usageTotals = computed(() => {
  const maps = dayMaps.value
  let input = 0
  let output = 0
  let cacheRead = 0
  for (const day of usageDays.value) {
    for (const tp of ['chat', 'schedule'] as const) {
      const r = maps[tp].get(day)
      if (!r) continue
      input += r.input_tokens ?? 0
      output += r.output_tokens ?? 0
      cacheRead += r.cache_read_tokens ?? 0
    }
  }
  return { input, output, total: input + output, cacheRead }
})

const hasUsage = computed(() => usageTotals.value.total > 0)

function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

const usageStats = computed(() => {
  const u = usageTotals.value
  const rate = u.input > 0 ? `${Math.round((u.cacheRead / u.input) * 100)}%` : '—'
  return [
    { key: 'total', label: t('bots.overview.usageTotal'), value: formatNumber(u.total) },
    { key: 'input', label: t('bots.overview.usageInput'), value: formatNumber(u.input) },
    { key: 'output', label: t('bots.overview.usageOutput'), value: formatNumber(u.output) },
    { key: 'cache', label: t('bots.overview.usageCacheHit'), value: rate },
  ]
})

const dailyUsageSeries = computed(() => {
  const days = usageDays.value
  const maps = dayMaps.value
  const sumDay = (day: string, field: 'input_tokens' | 'output_tokens') => {
    let sum = 0
    for (const tp of ['chat', 'schedule'] as const) {
      sum += maps[tp].get(day)?.[field] ?? 0
    }
    return sum
  }
  return [
    { name: t('bots.overview.usageInput'), data: days.map(d => sumDay(d, 'input_tokens')) },
    { name: t('bots.overview.usageOutput'), data: days.map(d => sumDay(d, 'output_tokens')) },
  ]
})

function formatShortDay(day: string): string {
  return day.slice(5)
}

function go(tab: string, section?: string) {
  // One atomic navigation writing both tab and (optional) section: doing two
  // separate query writes races, because each spreads a possibly-stale
  // route.query and the second can clobber the first. The target tab reads
  // `section` on mount and scrolls to it. activeTab's own param watcher syncs
  // this back into its model, so the tab still switches.
  const query: LocationQueryRaw = { ...route.query, tab }
  if (section) query.section = section
  else delete query.section
  void router.replace({ query })
}
</script>
