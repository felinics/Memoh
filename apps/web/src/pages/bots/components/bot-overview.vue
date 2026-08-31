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
            :sub="m.sub"
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
        <div class="space-y-4 p-4">
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
          <!-- Inline style, not a Tailwind height class: vue-echarts puts an
               inline height on its root, which beats a class and collapses the
               canvas to 0. Mirrors the Usage page's `style="height:..."`. -->
          <VChart
            v-else-if="hasUsage"
            :option="dailyOption"
            autoresize
            style="height: 200px; width: 100%"
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
import { computed, ref, onActivated, onDeactivated, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import VChart from 'vue-echarts'
import { useDark } from '@vueuse/core'
import { Badge, Button, CalloutBanner, MetricReadout, PageShell, SettingsRow, SettingsSection, Skeleton } from '@felinic/ui'
import {
  getBotsById,
  getBotsByBotIdSettings,
  getBotsByBotIdMemoryStatus,
  getBotsByBotIdTokenUsage,
  getBotsByBotIdContainer,
  getBotsByBotIdContainerMetrics,
  getChannels,
  getBotsByIdChannelByPlatform,
  type HandlersChannelMeta,
  type ChannelChannelConfig,
  type HandlersDailyTokenUsage,
} from '@memohai/sdk'
import BotChecksPanel from './bot-checks-panel.vue'
import ChannelIcon from '@/components/channel-icon/index.vue'
import { channelTypeDisplayName } from '@/utils/channel-type-label'
import { useBotStatusMeta } from '@/composables/useBotStatusMeta'
import { resolveBotWorkspaceBackend } from '@/utils/bot-workspace'
import { formatMetricBytes, formatMetricPercent } from '@/utils/format-bytes'
import { formatDateTime } from '@/utils/date-time'

use([CanvasRenderer, BarChart, GridComponent, TooltipComponent, LegendComponent])

interface BotChannelItem {
  meta: HandlersChannelMeta
  config: ChannelChannelConfig | null
  configured: boolean
}

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const routeIdentifier = computed(() => route.params.botName as string)
const checksOpen = ref(false)

const { data: bot } = useQuery({
  key: () => ['bot', routeIdentifier.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: routeIdentifier.value }, throwOnError: true })
    return data
  },
  enabled: () => !!routeIdentifier.value,
})
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

// Shares the colada key with bot-channels.vue, so visiting Platforms after
// Overview (or vice versa) reuses the cached probe instead of refetching.
const { data: channels, isLoading: channelsLoading } = useQuery({
  key: () => ['bot-channels', botId.value],
  query: async (): Promise<BotChannelItem[]> => {
    const { data: metas } = await getChannels({ throwOnError: true })
    if (!metas) return []
    const configurableTypes = metas.filter((m) => !m.configless)
    return Promise.all(
      configurableTypes.map(async (meta) => {
        try {
          const { data: config } = await getBotsByIdChannelByPlatform({ path: { id: botId.value, platform: meta.type ?? '' }, throwOnError: true })
          return { meta, config: config ?? null, configured: true }
        } catch {
          return { meta, config: null, configured: false }
        }
      }),
    )
  },
  enabled: () => !!botId.value,
})

const configuredChannels = computed(() => (channels.value ?? []).filter((c) => c.configured))

// Platforms is low-weight: only shown when nothing is connected yet (Connect
// nudge) or the bot has a check issue (so a broken connection stays visible).
// Connected + healthy hides it — the user knows what they connected.
const showPlatforms = computed(() => configuredChannels.value.length === 0 || hasIssue.value)

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

const showMemorySection = computed(() => !!settings.value?.memory_provider_id)

const memoryIsBuiltin = computed(() =>
  (memoryStatus.value?.provider_type ?? 'builtin') === 'builtin',
)

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
      label: memoryIsBuiltin.value
        ? t('bots.settings.memoryGraphEdges')
        : t('bots.settings.memorySourceEntries'),
      value: formatCount(memoryIsBuiltin.value ? status?.edge_count : status?.source_count),
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
  if (!settings.value?.memory_provider_id) return ''
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

// Poll metrics (and container state) every 10s while running, mirroring the
// detail-page pattern. KeepAlive wraps this tab, so onUnmounted never fires on
// tab switch — gate on onActivated/onDeactivated instead, and stop polling once
// the container isn't running so we don't hammer a backend with nothing to say.
const POLL_MS = 10_000
let pollTimer: ReturnType<typeof setInterval> | null = null
let isActive = true

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function syncPolling() {
  const shouldPoll = isActive && isContainerBot.value && containerRunning.value
  if (shouldPoll && !pollTimer) {
    pollTimer = setInterval(() => {
      void refetchMetrics()
      void refetchContainer()
    }, POLL_MS)
  } else if (!shouldPoll) {
    stopPolling()
  }
}

watch([isContainerBot, containerRunning], syncPolling, { immediate: true })

onActivated(() => {
  isActive = true
  syncPolling()
})

onDeactivated(() => {
  isActive = false
  stopPolling()
})

onBeforeUnmount(stopPolling)

// Status dot + label reuse the Container tab's status vocabulary so the two
// pages never disagree on "is it running".
const runtimeStatusKey = computed(() => {
  const status = (container.value?.status ?? '').trim().toLowerCase()
  if (status === 'running') return 'running'
  if (status === 'created') return 'created'
  if (status === 'stopped' || status === 'exited') return 'stopped'
  return containerRunning.value ? 'running' : 'unknown'
})

// Status label reuses the Container tab vocabulary. Badge is always secondary
// (neutral gray) — green "Running" was too loud for a quiet telemetry row.
const runtimeStatusLabel = computed(() => {
  switch (runtimeStatusKey.value) {
    case 'running': return t('bots.container.statusRunning')
    case 'created': return t('bots.container.statusCreated')
    case 'stopped': return t('bots.container.statusStopped')
    default: return t('bots.container.statusUnknown')
  }
})

const runtimeSampledAt = computed(() => {
  const ts = containerMetrics.value?.sampled_at
  return ts ? formatDateTime(ts) : ''
})

const cpuMetrics = computed(() => containerMetrics.value?.metrics?.cpu)
const memoryMetrics = computed(() => containerMetrics.value?.metrics?.memory)
const storageMetrics = computed(() => containerMetrics.value?.metrics?.storage)

const runtimeHasMetrics = computed(
  () => !!cpuMetrics.value || !!memoryMetrics.value || !!storageMetrics.value,
)

const runtimeMetricCards = computed(() => {
  const mem = memoryMetrics.value
  const memLimit = mem?.limit_bytes
  return [
    {
      key: 'cpu',
      label: t('bots.container.metricsLabels.cpu'),
      value: formatMetricPercent(cpuMetrics.value?.usage_percent),
      sub: '',
    },
    {
      key: 'memory',
      label: t('bots.container.metricsLabels.memory'),
      value: formatMetricBytes(mem?.usage_bytes),
      sub: memLimit && memLimit > 0 ? `/ ${formatMetricBytes(memLimit)}` : '',
    },
    {
      key: 'storage',
      label: t('bots.container.metricsLabels.storage'),
      value: formatMetricBytes(storageMetrics.value?.used_bytes),
      sub: '',
    },
  ]
})

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

const isDark = useDark()

// echarts paints on a <canvas> and can't read our CSS custom properties (the
// tokens are oklch + nested vars), so resolve each design token to a concrete
// color through a probe element, then rasterize it to a single pixel and read
// the bytes back as rgb/rgba. The pixel round-trip matters: echarts' default
// hover (emphasis) state runs the bar's fill through zrender's `liftColor`,
// which only parses #hex/rgb/rgba/hsl — NOT oklch/color(). On Electron 34's
// Chromium 132, `getComputedStyle(...).color` (and a canvas `fillStyle`
// round-trip) keep CSS Color 4 values as `oklch(...)`, so liftColor returns
// undefined and the hovered bar paints transparent — i.e. "the bar vanishes on
// hover". Painting a pixel collapses any renderable color to concrete sRGB
// bytes, so the value zrender sees is always parseable. `void isDark.value`
// re-runs this when the theme flips so the chart tracks light/dark.
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
    const [r, g, b, a] = colorCanvas.getImageData(0, 0, 1, 1).data
    return a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`
  }
  catch {
    return fallback
  }
}

const chartTheme = computed(() => {
  void isDark.value
  const fontFamily = typeof document !== 'undefined'
    ? getComputedStyle(document.body).fontFamily
    : 'inherit'
  return {
    // Black / white / grey only. `--primary` is a violet in this theme, so the
    // bars use the neutral foreground (input) + muted-foreground (output); no
    // brand or accent color anywhere in the chart.
    bar: readColor('--foreground', '#18181b'),
    barMuted: readColor('--muted-foreground', '#a1a1aa'),
    text: readColor('--muted-foreground', '#a1a1aa'),
    line: readColor('--border', '#e4e4e7'),
    fontFamily,
  }
})

const dailyOption = computed(() => {
  const days = usageDays.value
  const maps = dayMaps.value
  const c = chartTheme.value
  const inputLabel = t('bots.overview.usageInput')
  const outputLabel = t('bots.overview.usageOutput')
  const sumDay = (day: string, field: 'input_tokens' | 'output_tokens') => {
    let sum = 0
    for (const tp of ['chat', 'schedule'] as const) {
      sum += maps[tp].get(day)?.[field] ?? 0
    }
    return sum
  }
  return {
    textStyle: { fontFamily: c.fontFamily },
    // The tooltip is real DOM (not canvas), so its CSS references the SAME
    // tokens as Popover/HoverCard directly — shell radius, the --border-menu
    // hairline and --shadow-dropdown — for a pixel-identical surface. echarts'
    // own background/border/padding are zeroed so they don't fight the token
    // CSS. Body copy uses --text-body (the popover's own size/leading) + the
    // page font. axisPointer is a soft solid hairline, not echarts' dashed line.
    tooltip: {
      trigger: 'axis' as const,
      backgroundColor: 'transparent',
      borderWidth: 0,
      padding: 0,
      extraCssText: [
        'background: var(--popover)',
        'color: var(--popover-foreground)',
        'border: 1px solid var(--border-menu)',
        'border-radius: var(--radius-menu-shell)',
        'box-shadow: var(--shadow-dropdown)',
        'padding: 12px 14px',
        `font-family: ${c.fontFamily}`,
      ].join('; '),
      // `shadow`, NOT `line`: a 1px line pointer paints ON TOP of the bar, and a
      // 30-day bar is only a few px wide, so the line fully covers the hovered
      // bar — which reads as "the bar vanishes when I hover it". A shadow band
      // paints a faint wash BEHIND the bars, so the hovered bar stays visible.
      axisPointer: {
        type: 'shadow' as const,
        shadowStyle: { color: 'rgba(128, 128, 128, 0.12)' },
      },
      formatter: (params: { seriesName?: string, value?: number, color?: string, axisValueLabel?: string }[]) => {
        const list = Array.isArray(params) ? params : [params]
        const head = list[0]?.axisValueLabel ?? ''
        const rows = list.map((p) => {
          const val = formatNumber(typeof p.value === 'number' ? p.value : 0)
          const dot = `<span style="display:inline-block;width:6px;height:6px;border-radius:9999px;margin-right:7px;background:${p.color ?? 'var(--muted-foreground)'};"></span>`
          return '<div style="display:flex;align-items:center;justify-content:space-between;gap:24px;line-height:1.7;">'
            + `<span style="color:var(--muted-foreground);">${dot}${p.seriesName ?? ''}</span>`
            + `<span style="color:var(--popover-foreground);font-weight:500;font-variant-numeric:tabular-nums;">${val}</span></div>`
        }).join('')
        return '<div style="font-size:var(--text-body);line-height:var(--text-body--line-height);letter-spacing:var(--text-body--letter-spacing);min-width:132px;">'
          + `<div style="color:var(--muted-foreground);margin-bottom:3px;">${head}</div>${rows}</div>`
      },
    },
    legend: {
      data: [inputLabel, outputLabel],
      bottom: 0,
      itemGap: 16,
      icon: 'roundRect',
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
      {
        name: inputLabel,
        type: 'bar' as const,
        stack: 'tokens',
        itemStyle: { color: c.bar },
        data: days.map((d) => sumDay(d, 'input_tokens')),
      },
      {
        name: outputLabel,
        type: 'bar' as const,
        stack: 'tokens',
        itemStyle: { color: c.barMuted, borderRadius: [3, 3, 0, 0] as [number, number, number, number] },
        data: days.map((d) => sumDay(d, 'output_tokens')),
      },
    ],
  }
})

function go(tab: string, section?: string) {
  // One atomic navigation writing both tab and (optional) section: doing two
  // separate query writes races, because each spreads a possibly-stale
  // route.query and the second can clobber the first. The target tab reads
  // `section` on mount and scrolls to it. activeTab's own param watcher syncs
  // this back into its model, so the tab still switches.
  const query = { ...route.query, tab }
  if (section) query.section = section
  else delete query.section
  void router.replace({ query })
}
</script>
