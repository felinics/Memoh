<template>
  <div
    ref="rootRef"
    class=" inset-0 flex flex-col z-(--z-raised) "
    :class="isFullScroll?'fixed':'absolute'"
    @click="closeStatsMenu"
  >
    <Maximize
      ref="fullScreenIcon"
      class="absolute top-4 right-4 text-background/80 transition-all opacity-0 duration-500"
      @click="()=>toggle()"
    />
    <video
      ref="videoRef"
      class="size-full min-h-0 flex-1 object-contain"
      autoplay
      playsinline
      muted
      tabindex="0"
      @playing="videoReady = true"
      @contextmenu.prevent="openStatsMenu"
      @mousedown.prevent="onPointerDown"
      @mousemove="onPointerMove"
      @mouseup.prevent="onPointerUp"
      @mouseleave="onPointerLeave"
      @wheel.prevent="onWheel"
      @keydown.prevent="onKeyDown"
      @keyup.prevent="onKeyUp"
    />
    <div
      v-if="prepareProgress"
      class="absolute inset-0 flex items-center justify-center px-6"
    >
      <SurfaceCard class="w-full max-w-[520px]">
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <p class="text-sm font-medium text-foreground">
              {{ t('chat.display.prepare.title') }}
            </p>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ prepareProgress.message }}
            </p>
          </div>
          <span class="shrink-0 font-mono text-xs text-muted-foreground tabular-nums">
            {{ preparePercent }}%
          </span>
        </div>
        <div class="mt-4 h-2 w-full overflow-hidden rounded-full bg-muted">
          <div
            class="h-full rounded-full bg-foreground transition-all duration-300 ease-out"
            :style="{ width: `${preparePercent}%` }"
          />
        </div>
        <div
          class="mt-5 grid gap-2"
          :class="prepareStageGridClass"
        >
          <div
            v-for="stage in prepareStages"
            :key="stage.key"
            class="flex min-w-0 flex-col items-center gap-2 rounded-md border border-border bg-surface-editor px-2 py-3 text-center"
            :class="stage.active ? 'text-foreground' : 'text-muted-foreground'"
          >
            <component
              :is="stage.icon"
              class="size-4"
              :class="{ 'animate-pulse': stage.active }"
            />
            <span class="w-full truncate text-[11px] font-medium">
              {{ stage.label }}
            </span>
          </div>
        </div>
      </SurfaceCard>
    </div>
    <div
      v-if="status === 'connected' || displaySessionId"
      class="absolute right-2 top-2 flex items-center gap-1 rounded-md border border-border bg-surface-editor/95 p-1 text-xs text-muted-foreground"
    >
      <span class="max-w-[180px] truncate px-2">
        {{ title || t('chat.display.title') }}
      </span>
      <button
        v-if="closable !== false"
        type="button"
        class="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
        :title="t('chat.display.closeSession')"
        :aria-label="t('chat.display.closeSession')"
        @click="closeDisplayWindow"
      >
        <X class="size-3.5" />
      </button>
    </div>
    <div
      v-if="statsMenu.open"
      class="absolute z-(--z-sticky) w-44 rounded-md border border-border bg-surface-editor/95 p-1 text-xs text-foreground shadow-lg"
      :style="{ left: `${statsMenu.x}px`, top: `${statsMenu.y}px` }"
      @click.stop
      @mousedown.stop
      @mouseup.stop
      @wheel.stop
    >
      <button
        type="button"
        class="flex w-full items-center justify-between rounded px-2 py-1.5 text-left hover:bg-accent"
        @click="toggleStatsOverlay"
      >
        <span>{{ statsVisible ? t('chat.display.stats.hide') : t('chat.display.stats.show') }}</span>
        <Activity class="size-3.5 text-muted-foreground" />
      </button>
    </div>
    <div
      v-if="statsVisible"
      class="pointer-events-none absolute left-3 top-12 z-(--z-raised) w-[min(400px,calc(100%-24px))] rounded-md border border-white/20 bg-black/85 p-3 font-mono text-[11px] leading-5 text-white shadow-xl"
    >
      <div class="mb-2 flex items-center justify-between gap-3 font-sans text-xs font-medium">
        <span>{{ t('chat.display.stats.title') }}</span>
        <span class="font-mono text-[10px] text-white/65">{{ statsUpdatedLabel }}</span>
      </div>
      <div class="mb-2 rounded border border-white/15 bg-white/[0.03] px-2 py-1.5">
        <div class="mb-1 flex items-center justify-between gap-3">
          <span class="text-white/55">{{ t('chat.display.stats.bitrate') }}</span>
          <span class="text-white">{{ displayStats.bitrate }}</span>
        </div>
        <div class="grid grid-cols-[46px_minmax(0,1fr)] gap-2">
          <div class="flex flex-col justify-between py-1 text-right text-[10px] leading-none text-white/45">
            <span>{{ bitrateChartMaxLabel }}</span>
            <span>0</span>
          </div>
          <div class="min-w-0">
            <svg
              class="h-14 w-full overflow-visible"
              viewBox="0 0 320 58"
              preserveAspectRatio="none"
              aria-hidden="true"
            >
              <line
                v-for="line in bitrateChartGrid"
                :key="line"
                x1="0"
                x2="320"
                :y1="line"
                :y2="line"
                stroke="rgba(255,255,255,0.12)"
                stroke-width="1"
              />
              <polygon
                v-if="bitrateChartArea"
                :points="bitrateChartArea"
                fill="rgba(255,255,255,0.14)"
              />
              <polyline
                v-if="bitrateChartPoints"
                :points="bitrateChartPoints"
                fill="none"
                stroke="rgba(255,255,255,0.92)"
                stroke-width="2"
                stroke-linecap="round"
                stroke-linejoin="round"
                vector-effect="non-scaling-stroke"
              />
            </svg>
            <div class="-mt-1 flex items-center justify-end text-[10px] text-white/45">
              <span>{{ bitrateChartEndLabel }}</span>
            </div>
          </div>
        </div>
      </div>
      <div class="grid grid-cols-[112px_minmax(0,1fr)] gap-x-3 gap-y-1">
        <template
          v-for="row in statsRows"
          :key="row.key"
        >
          <div class="truncate text-white/55">
            {{ row.label }}
          </div>
          <div class="truncate text-white">
            {{ row.value }}
          </div>
        </template>
      </div>
    </div>
    <!-- Stay-local (not PanePlaceholder): this is one branch of a state machine
         shared with the prepareProgress compound card above it — the two are
         mutually exclusive views over the same video surface, not an
         independent empty/loading pane. It's also positioned absolutely over
         a live <video>, which PanePlaceholder (a frameless CONTENT-area fill)
         doesn't model. Splitting one state machine across an owner + a hand
         card would fragment it instead of unifying it. -->
    <div
      v-if="!prepareProgress && (status !== 'connected' || !videoReady)"
      class="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Spinner
        v-if="status !== 'disconnected' && status !== 'unavailable'"
        class="size-5 text-muted-foreground"
      />
      <p class="text-sm text-muted-foreground">
        {{ (status === 'disconnected' || status === 'unavailable') ? statusLabel : t('chat.display.status.connecting') }}
      </p>
      <Button
        v-if="status === 'disconnected' || status === 'unavailable'"
        size="sm"
        variant="outline"
        @click="connect"
      >
        {{ t('chat.display.reconnect') }}
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch, type Component, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Spinner, SurfaceCard } from '@felinic/ui'
import { Activity, Globe, Maximize, Monitor, Package, Wrench, X } from 'lucide-vue-next'
import { useMagicKeys, useMouseInElement, useToggle } from '@vueuse/core'
import { useBotDisplayConnection } from '@/composables/useBotDisplayConnection'
import { captureDisplaySnapshot } from '@/utils/display-snapshot'

const screenEl = useTemplateRef('rootRef')
const fullScreenIcon = useTemplateRef('fullScreenIcon')
const { isOutside, x, y } = useMouseInElement(screenEl)

let fullScreenIconTimer: ReturnType<typeof setTimeout> | null = null
watch([isOutside, x, y], () => {
  const icon = fullScreenIcon.value
  if (!icon) {
    return
  }
  if (!isOutside.value) {
    icon.classList.remove('opacity-0')
    if (fullScreenIconTimer) {
      clearTimeout(fullScreenIconTimer)
    }
    fullScreenIconTimer = setTimeout(() => {
      fullScreenIcon.value?.classList.add('opacity-0')
      fullScreenIconTimer = null
    }, 5000)
  } else {
    icon.classList.add('opacity-0')
  }
}, {
  deep: true,
})

const [isFullScroll, toggle] = useToggle()

const { current } = useMagicKeys()
watch(current, () => {
  if (isFullScroll.value&& current.has('escape')) {
    toggle(false)
  }
})

const props = defineProps<{
  botId: string
  tabId: string
  title?: string
  active?: boolean
  closable?: boolean
}>()

const emit = defineEmits<{
  close: []
  snapshot: [payload: { tabId: string; sessionId?: string; dataUrl: string }]
}>()

const connection = useBotDisplayConnection(props.botId)

interface PrepareStage {
  key: string
  label: string
  icon: Component
  active: boolean
}

interface DisplayStats {
  session: string
  resolution: string
  viewport: string
  codec: string
  connection: string
  ice: string
  fps: string
  bitrate: string
  decoded: string
  dropped: string
  packetsLost: string
  jitter: string
  rtt: string
}

interface StatsSample {
  timestamp: number
  bytesReceived: number
  framesDecoded: number
}

interface BitrateSample {
  timestamp: number
  kbps: number
}

type StatsRecord = Record<string, unknown>
type VideoWithFrameCallback = HTMLVideoElement & {
  requestVideoFrameCallback?: (callback: (now: number, metadata: unknown) => void) => number
  cancelVideoFrameCallback?: (handle: number) => void
}

const ACTIVE_SNAPSHOT_INTERVAL_MS = 10000
const INACTIVE_SNAPSHOT_INTERVAL_MS = 5000
const FIRST_SNAPSHOT_DELAY_MS = 350
const BITRATE_SAMPLE_LIMIT = 60
const BITRATE_AXIS_MIN_KBPS = 100
const BITRATE_CHART_WIDTH = 320
const BITRATE_CHART_HEIGHT = 58
const BITRATE_CHART_PADDING = 4
const BITRATE_CHART_GRID = [14, 30, 46]
const FALLBACK_DISPLAY_WIDTH = 1280
const FALLBACK_DISPLAY_HEIGHT = 960

const { t } = useI18n()
const rootRef = ref<HTMLElement | null>(null)
const videoRef = ref<HTMLVideoElement | null>(null)
const status = connection.status
const videoReady = ref(false)
const unavailableReason = connection.unavailableReason
const prepareProgress = connection.prepareProgress
const displaySessionId = connection.sessionId
const statsVisible = ref(false)
const statsUpdatedAt = ref<number | null>(null)
const displayStats = ref<DisplayStats>(emptyDisplayStats())
const bitrateSamples = ref<BitrateSample[]>([])
const statsMenu = reactive({
  open: false,
  x: 0,
  y: 0,
})
let pointerMask = 0
let lastPointerPoint: { x: number; y: number } | null = null
let snapshotTimer: ReturnType<typeof window.setTimeout> | null = null
let snapshotFrameRequest: number | null = null
let statsTimer: ReturnType<typeof window.setInterval> | null = null
let lastStatsSample: StatsSample | null = null

const statusLabel = computed(() => {
  if (status.value === 'unavailable') {
    return unavailableReason.value || t('chat.display.status.unavailable')
  }
  switch (status.value) {
    case 'connecting': return t('chat.display.status.connecting')
    case 'connected': return t('chat.display.status.connected')
    case 'disconnected': return t('chat.display.status.disconnected')
    default: return t('chat.display.status.idle')
  }
})

const preparePercent = computed(() => Math.max(0, Math.min(100, Math.round(prepareProgress.value?.percent ?? 0))))

const prepareStageOrder = ['checking', 'system', 'installing', 'browser', 'starting', 'desktop', 'styling', 'complete']

const prepareStageDefs: PrepareStage[] = [
  { key: 'checking', label: t('chat.display.prepare.stageCheck'), icon: Wrench, active: false },
  { key: 'installing', label: t('chat.display.prepare.stageSetup'), icon: Package, active: false },
  { key: 'browser', label: t('chat.display.prepare.stageBrowser'), icon: Globe, active: false },
  { key: 'desktop', label: t('chat.display.prepare.stageDesktop'), icon: Monitor, active: false },
]

const prepareStages = computed<PrepareStage[]>(() => {
  const current = prepareProgress.value?.step ?? 'checking'
  const currentIndex = Math.max(0, prepareStageOrder.indexOf(current))
  return prepareStageDefs
    .filter((stage) => prepareStageOrder.indexOf(stage.key) <= currentIndex)
    .map((stage) => ({
      ...stage,
      active: prepareStageOrder.indexOf(stage.key) <= currentIndex,
    }))
})

const prepareStageGridClass = computed(() => {
  const count = prepareStages.value.length
  if (count <= 1) return 'grid-cols-1'
  if (count <= 2) return 'grid-cols-2'
  if (count <= 3) return 'grid-cols-3'
  return 'grid-cols-4'
})

const statsRows = computed(() => [
  { key: 'session', label: t('chat.display.stats.session'), value: displayStats.value.session },
  { key: 'resolution', label: t('chat.display.stats.resolution'), value: displayStats.value.resolution },
  { key: 'viewport', label: t('chat.display.stats.viewport'), value: displayStats.value.viewport },
  { key: 'codec', label: t('chat.display.stats.codec'), value: displayStats.value.codec },
  { key: 'connection', label: t('chat.display.stats.connection'), value: displayStats.value.connection },
  { key: 'ice', label: t('chat.display.stats.ice'), value: displayStats.value.ice },
  { key: 'fps', label: t('chat.display.stats.fps'), value: displayStats.value.fps },
  { key: 'decoded', label: t('chat.display.stats.decoded'), value: displayStats.value.decoded },
  { key: 'dropped', label: t('chat.display.stats.dropped'), value: displayStats.value.dropped },
  { key: 'loss', label: t('chat.display.stats.packetsLost'), value: displayStats.value.packetsLost },
  { key: 'jitter', label: t('chat.display.stats.jitter'), value: displayStats.value.jitter },
  { key: 'rtt', label: t('chat.display.stats.rtt'), value: displayStats.value.rtt },
])

const statsUpdatedLabel = computed(() =>
  statsUpdatedAt.value ? new Date(statsUpdatedAt.value).toLocaleTimeString() : '--',
)

const bitrateChartGrid = BITRATE_CHART_GRID

const bitrateChartMax = computed(() => {
  const max = Math.max(0, ...bitrateSamples.value.map(sample => sample.kbps))
  return roundBitrateAxisMax(max)
})

const bitrateChartMaxLabel = computed(() => formatBitrateAxisKbps(bitrateChartMax.value))

const bitrateChartPoints = computed(() => buildBitrateChartPoints(bitrateSamples.value, bitrateChartMax.value))

const bitrateChartWindowMs = computed(() => {
  const first = bitrateSamples.value.at(0)?.timestamp
  const last = bitrateSamples.value.at(-1)?.timestamp
  return first && last ? Math.max(0, last - first) : 0
})

const bitrateChartEndLabel = computed(() => bitrateSamples.value.length ? formatChartDuration(bitrateChartWindowMs.value) : '--')

const bitrateChartArea = computed(() => {
  if (!bitrateChartPoints.value) return ''
  const bottom = BITRATE_CHART_HEIGHT - BITRATE_CHART_PADDING
  return `${BITRATE_CHART_PADDING},${bottom} ${bitrateChartPoints.value} ${BITRATE_CHART_WIDTH - BITRATE_CHART_PADDING},${bottom}`
})

function emptyDisplayStats(): DisplayStats {
  return {
    session: '-',
    resolution: '-',
    viewport: '-',
    codec: '-',
    connection: '-',
    ice: '-',
    fps: '-',
    bitrate: '-',
    decoded: '-',
    dropped: '-',
    packetsLost: '-',
    jitter: '-',
    rtt: '-',
  }
}

function cleanupLocalUI() {
  stopSnapshotCapture()
  stopStatsPolling()
  closeStatsMenu()
  videoReady.value = false
  pointerMask = 0
  lastPointerPoint = null
  if (videoRef.value) {
    videoRef.value.srcObject = null
  }
}

function closeDisplayWindow() {
  cleanupLocalUI()
  emit('close')
}

function startSnapshotCapture() {
  if (snapshotTimer || snapshotFrameRequest !== null || document.visibilityState === 'hidden') return
  scheduleSnapshotCapture(FIRST_SNAPSHOT_DELAY_MS)
}

function stopSnapshotCapture() {
  if (snapshotTimer) {
    window.clearTimeout(snapshotTimer)
    snapshotTimer = null
  }
  const video = videoRef.value as VideoWithFrameCallback | null
  if (snapshotFrameRequest !== null && video?.cancelVideoFrameCallback) {
    video.cancelVideoFrameCallback(snapshotFrameRequest)
  }
  snapshotFrameRequest = null
}

function restartSnapshotCapture() {
  if (status.value !== 'connected') return
  stopSnapshotCapture()
  startSnapshotCapture()
}

function scheduleSnapshotCapture(delayMs = snapshotIntervalMs()) {
  if (status.value !== 'connected' || document.visibilityState === 'hidden') return
  snapshotTimer = window.setTimeout(() => {
    snapshotTimer = null
    const video = videoRef.value as VideoWithFrameCallback | null
    if (video?.requestVideoFrameCallback) {
      snapshotFrameRequest = video.requestVideoFrameCallback(() => {
        snapshotFrameRequest = null
        captureSnapshot()
        scheduleSnapshotCapture()
      })
      return
    }
    captureSnapshot()
    scheduleSnapshotCapture()
  }, delayMs)
}

function snapshotIntervalMs() {
  return props.active ? ACTIVE_SNAPSHOT_INTERVAL_MS : INACTIVE_SNAPSHOT_INTERVAL_MS
}

function captureSnapshot() {
  const video = videoRef.value
  if (!video) {
    return
  }
  try {
    const dataUrl = captureDisplaySnapshot(video)
    if (!dataUrl) return
    emit('snapshot', {
      tabId: props.tabId,
      sessionId: displaySessionId.value || undefined,
      dataUrl,
    })
  } catch {
    // Some browsers can briefly refuse drawing a still-starting WebRTC frame.
  }
}

function openStatsMenu(event: MouseEvent) {
  const root = rootRef.value
  const rect = root?.getBoundingClientRect()
  const rawX = rect ? event.clientX - rect.left : event.clientX
  const rawY = rect ? event.clientY - rect.top : event.clientY
  const width = rect?.width ?? window.innerWidth
  const height = rect?.height ?? window.innerHeight
  statsMenu.x = clamp(rawX, 8, Math.max(8, width - 184))
  statsMenu.y = clamp(rawY, 8, Math.max(8, height - 48))
  statsMenu.open = true
}

function closeStatsMenu() {
  statsMenu.open = false
}

function toggleStatsOverlay() {
  statsVisible.value = !statsVisible.value
  closeStatsMenu()
  if (statsVisible.value) {
    startStatsPolling()
  } else {
    stopStatsPolling()
  }
}

function startStatsPolling() {
  if (statsTimer || status.value !== 'connected' || document.visibilityState === 'hidden') return
  void updateDisplayStats()
  statsTimer = window.setInterval(() => {
    void updateDisplayStats()
  }, 1000)
}

function stopStatsPolling() {
  if (!statsTimer) return
  window.clearInterval(statsTimer)
  statsTimer = null
  lastStatsSample = null
}

function resetStatsState() {
  displayStats.value = emptyDisplayStats()
  statsUpdatedAt.value = null
  lastStatsSample = null
  bitrateSamples.value = []
}

async function updateDisplayStats() {
  const peer = connection.peer.value
  if (!peer) {
    resetStatsState()
    return
  }
  try {
    const report = await peer.getStats()
    const now = Date.now()
    const inbound = findStats(report, item =>
      item.type === 'inbound-rtp'
      && (item.kind === 'video' || item.mediaType === 'video')
      && item.isRemote !== true,
    )
    const candidatePair = findSelectedCandidatePair(report)
    const codec = inbound ? report.get(String(inbound.codecId || '')) as StatsRecord | undefined : undefined
    const video = videoRef.value
    const bytesReceived = numberStat(inbound, 'bytesReceived')
    const framesDecoded = numberStat(inbound, 'framesDecoded')
    const currentSample: StatsSample = {
      timestamp: numberStat(inbound, 'timestamp') || now,
      bytesReceived,
      framesDecoded,
    }
    const previousSample = lastStatsSample
    lastStatsSample = currentSample
    const elapsedMs = previousSample ? currentSample.timestamp - previousSample.timestamp : 0
    const bitrateKbps = previousSample && elapsedMs > 0
      ? Math.max(0, (bytesReceived - previousSample.bytesReceived) * 8 / elapsedMs)
      : null
    const decodedFps = previousSample && elapsedMs > 0
      ? Math.max(0, (framesDecoded - previousSample.framesDecoded) * 1000 / elapsedMs)
      : 0
    const fps = numberStat(inbound, 'framesPerSecond') || decodedFps
    if (bitrateKbps !== null) {
      appendBitrateSample(now, bitrateKbps)
    }

    displayStats.value = {
      session: displaySessionId.value ? shortID(displaySessionId.value) : '-',
      resolution: video?.videoWidth && video.videoHeight ? `${video.videoWidth}x${video.videoHeight}` : '-',
      viewport: video ? `${Math.round(video.clientWidth)}x${Math.round(video.clientHeight)}` : '-',
      codec: stringStat(codec, 'mimeType') || stringStat(inbound, 'codecId') || '-',
      connection: peer.connectionState || '-',
      ice: peer.iceConnectionState || '-',
      fps: fps ? fps.toFixed(1) : '-',
      bitrate: bitrateKbps !== null ? formatBitrateKbps(bitrateKbps) : displayStats.value.bitrate,
      decoded: framesDecoded ? formatNumber(framesDecoded) : '-',
      dropped: formatNumber(numberStat(inbound, 'framesDropped')),
      packetsLost: formatNumber(numberStat(inbound, 'packetsLost')),
      jitter: formatMs(numberStat(inbound, 'jitter') * 1000),
      rtt: formatMs(numberStat(candidatePair, 'currentRoundTripTime') * 1000),
    }
    statsUpdatedAt.value = now
  } catch {
    resetStatsState()
  }
}

function sendInput(payload: Record<string, unknown>) {
  // Only the active pane drives input. There is one shared input channel per
  // bot, so a background/inactive pane forwarding pointer + key events would
  // race the focused pane's input on the desktop.
  if (!props.active) return
  connection.sendInput(payload)
}

function inputReady() {
  return connection.inputReady()
}

function buttonBit(button: number): number {
  switch (button) {
    case 0: return 1
    case 1: return 2
    case 2: return 4
    default: return 0
  }
}

function resolveVideoPoint(event: MouseEvent | WheelEvent): { x: number; y: number } | null {
  const video = videoRef.value
  if (!video) return null
  const rect = video.getBoundingClientRect()
  const sourceWidth = video.videoWidth || FALLBACK_DISPLAY_WIDTH
  const sourceHeight = video.videoHeight || FALLBACK_DISPLAY_HEIGHT
  const scale = Math.min(rect.width / sourceWidth, rect.height / sourceHeight)
  const width = sourceWidth * scale
  const height = sourceHeight * scale
  const offsetX = (rect.width - width) / 2
  const offsetY = (rect.height - height) / 2
  const x = (event.clientX - rect.left - offsetX) / scale
  const y = (event.clientY - rect.top - offsetY) / scale
  if (x < 0 || y < 0 || x > sourceWidth || y > sourceHeight) {
    return null
  }
  return {
    x: Math.max(0, Math.min(sourceWidth - 1, Math.round(x))),
    y: Math.max(0, Math.min(sourceHeight - 1, Math.round(y))),
  }
}

function sendPointer(event: MouseEvent | WheelEvent, mask = pointerMask) {
  if (!inputReady()) return
  const point = resolveVideoPoint(event)
  if (!point) return
  lastPointerPoint = point
  sendInput({
    type: 'pointer',
    x: point.x,
    y: point.y,
    button_mask: mask,
  })
}

function onPointerDown(event: MouseEvent) {
  videoRef.value?.focus()
  pointerMask |= buttonBit(event.button)
  sendPointer(event)
}

function onPointerMove(event: MouseEvent) {
  sendPointer(event)
}

function onPointerUp(event: MouseEvent) {
  pointerMask &= ~buttonBit(event.button)
  sendPointer(event)
}

function onPointerLeave(event: MouseEvent) {
  pointerMask = 0
  const point = resolveVideoPoint(event) ?? lastPointerPoint
  if (!point) return
  sendInput({
    type: 'pointer',
    x: point.x,
    y: point.y,
    button_mask: 0,
  })
}

function onWheel(event: WheelEvent) {
  const bit = event.deltaY < 0 ? 8 : 16
  sendPointer(event, pointerMask | bit)
  sendPointer(event, pointerMask)
}

function keysymForEvent(event: KeyboardEvent): number | null {
  if (event.key.length === 1) {
    return event.key.codePointAt(0) ?? null
  }
  const keysyms: Record<string, number> = {
    Backspace: 0xff08,
    Tab: 0xff09,
    Enter: 0xff0d,
    Escape: 0xff1b,
    Delete: 0xffff,
    Home: 0xff50,
    ArrowLeft: 0xff51,
    ArrowUp: 0xff52,
    ArrowRight: 0xff53,
    ArrowDown: 0xff54,
    PageUp: 0xff55,
    PageDown: 0xff56,
    End: 0xff57,
    Insert: 0xff63,
    Shift: 0xffe1,
    Control: 0xffe3,
    Alt: 0xffe9,
    Meta: 0xffeb,
  }
  if (/^F([1-9]|1[0-2])$/.test(event.key)) {
    return 0xffbe + Number(event.key.slice(1)) - 1
  }
  return keysyms[event.key] ?? null
}

function sendKey(event: KeyboardEvent, down: boolean) {
  const keysym = keysymForEvent(event)
  if (!keysym) return
  sendInput({
    type: 'key',
    keysym,
    down,
  })
}

function onKeyDown(event: KeyboardEvent) {
  if (event.repeat) return
  sendKey(event, true)
}

function onKeyUp(event: KeyboardEvent) {
  sendKey(event, false)
}

function clamp(value: number, min: number, max: number) {
  return Math.max(min, Math.min(max, value))
}

function findStats(report: RTCStatsReport, predicate: (item: StatsRecord) => boolean): StatsRecord | undefined {
  for (const item of report.values()) {
    const record = item as StatsRecord
    if (predicate(record)) return record
  }
  return undefined
}

function findSelectedCandidatePair(report: RTCStatsReport): StatsRecord | undefined {
  const transport = findStats(report, item => item.type === 'transport' && typeof item.selectedCandidatePairId === 'string')
  if (transport?.selectedCandidatePairId) {
    const selected = report.get(String(transport.selectedCandidatePairId)) as StatsRecord | undefined
    if (selected) return selected
  }
  return findStats(report, item =>
    item.type === 'candidate-pair'
    && item.state === 'succeeded'
    && (item.selected === true || item.nominated === true),
  )
}

function stringStat(item: StatsRecord | undefined, key: string) {
  const value = item?.[key]
  return typeof value === 'string' && value.trim() ? value : ''
}

function numberStat(item: StatsRecord | undefined, key: string) {
  const value = item?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function formatNumber(value: number) {
  return value ? Math.round(value).toLocaleString() : '0'
}

function formatMs(value: number) {
  return value ? `${Math.round(value)} ms` : '-'
}

function formatBitrateKbps(kbps: number) {
  if (!Number.isFinite(kbps)) return '-'
  if (kbps >= 1000) {
    return `${(kbps / 1000).toFixed(kbps >= 10000 ? 0 : 1)} Mbps`
  }
  return `${Math.round(Math.max(0, kbps))} kbps`
}

function roundBitrateAxisMax(kbps: number) {
  if (!Number.isFinite(kbps) || kbps <= 0) return BITRATE_AXIS_MIN_KBPS
  if (kbps <= 1000) {
    return Math.max(BITRATE_AXIS_MIN_KBPS, Math.ceil(kbps / 100) * 100)
  }
  return Math.ceil(kbps / 1000) * 1000
}

function formatBitrateAxisKbps(kbps: number) {
  if (!Number.isFinite(kbps)) return '-'
  if (kbps >= 1000) {
    return `${Math.ceil(kbps / 1000)} Mbps`
  }
  return `${Math.ceil(Math.max(0, kbps))} kbps`
}

function appendBitrateSample(timestamp: number, kbps: number) {
  bitrateSamples.value = [
    ...bitrateSamples.value,
    { timestamp, kbps: Math.max(0, kbps) },
  ].slice(-BITRATE_SAMPLE_LIMIT)
}

function buildBitrateChartPoints(samples: BitrateSample[], maxKbps: number) {
  if (!samples.length || maxKbps <= 0) return ''
  const drawableWidth = BITRATE_CHART_WIDTH - BITRATE_CHART_PADDING * 2
  const drawableHeight = BITRATE_CHART_HEIGHT - BITRATE_CHART_PADDING * 2
  const firstTimestamp = samples[0]?.timestamp ?? 0
  const lastTimestamp = samples.at(-1)?.timestamp ?? firstTimestamp
  const duration = lastTimestamp - firstTimestamp
  return samples.map((sample) => {
    const x = duration <= 0
      ? BITRATE_CHART_WIDTH - BITRATE_CHART_PADDING
      : BITRATE_CHART_PADDING + ((sample.timestamp - firstTimestamp) / duration) * drawableWidth
    const ratio = Math.min(1, sample.kbps / maxKbps)
    const y = BITRATE_CHART_HEIGHT - BITRATE_CHART_PADDING - ratio * drawableHeight
    return `${roundChartCoord(x)},${roundChartCoord(y)}`
  }).join(' ')
}

function formatChartDuration(durationMs: number) {
  if (!Number.isFinite(durationMs) || durationMs <= 0) return '1s'
  const seconds = Math.max(1, Math.ceil(durationMs / 1000))
  if (seconds < 60) return `${seconds}s`
  return `${Math.ceil(seconds / 60)}m`
}

function roundChartCoord(value: number) {
  return Math.round(value * 10) / 10
}

function shortID(value: string) {
  return value.length > 12 ? value.slice(0, 8) : value
}

function bindStream() {
  const video = videoRef.value
  const stream = connection.stream.value
  if (!video || !stream) return
  // Skip if already bound to this exact stream — multiple watchers (stream,
  // status, connect) can fire bindStream back-to-back and re-assigning
  // srcObject aborts the in-flight play() promise (AbortError).
  if (video.srcObject === stream) {
    // Already bound but maybe not playing yet (e.g. panel was hidden) — nudge it.
    if (video.paused) video.play().catch(() => {})
    return
  }
  video.srcObject = stream
  // play() resolves once playback begins; set videoReady here as the
  // authoritative signal instead of relying solely on the @playing event,
  // which can race or fail to fire when the panel visibility transitions.
  video.play().then(() => { videoReady.value = true }).catch(() => {})
}

async function connect() {
  const ok = await connection.acquireConnection()
  if (ok) bindStream()
}

// Snapshot/stats follow the shared peer state + this pane's visibility, so a
// background pane never captures and a connected pane resumes on focus.
watch([() => status.value, () => props.active], ([st, active]) => {
  if (st === 'connected' && active) {
    bindStream()
    startSnapshotCapture()
    if (statsVisible.value) startStatsPolling()
  } else {
    stopSnapshotCapture()
    stopStatsPolling()
  }
})

// The track event can land before connectionstatechange flips to 'connected'
// (track is negotiated at setRemoteDescription, ICE may still be gathering),
// so bind as soon as the stream arrives rather than gating on status.
watch(() => connection.stream.value, () => {
  bindStream()
})

function handleVisibilityChange() {
  if (document.visibilityState === 'hidden') {
    stopSnapshotCapture()
    stopStatsPolling()
    return
  }
  if (status.value === 'connected' && props.active) {
    startSnapshotCapture()
    if (statsVisible.value) startStatsPolling()
    return
  }
  // The peer can die while the tab is hidden (ICE timeout in a backgrounded
  // browser tab). On return, auto-reconnect a dead peer instead of leaving a
  // disconnected pane for a manual Reconnect click. acquireConnection is
  // idempotent (concurrent callers share acquirePromise), so this is safe even
  // if a reconnect is already in flight.
  if (props.active && status.value !== 'connected') {
    void connect()
  }
}

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  if (props.active) {
    void connect()
  }
})

watch(() => props.active, (active) => {
  if (!active) {
    stopSnapshotCapture()
    stopStatsPolling()
    return
  }
  if (status.value === 'connected') {
    bindStream()
    restartSnapshotCapture()
    return
  }
  void connect()
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  if (fullScreenIconTimer) {
    clearTimeout(fullScreenIconTimer)
    fullScreenIconTimer = null
  }
  cleanupLocalUI()
})
</script>
