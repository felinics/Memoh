<template>
  <!-- Rows of the caller's SettingsSection: the setup-mode select sits above
       this in the same card, so the section belongs to the caller. Each state
       renders a single root, which keeps the card's row dividers intact. -->
  <SettingsRow
    v-if="state === 'idle'"
    :label="$t('bots.channels.feishuQr.rowLabel')"
    :description="$t('bots.channels.feishuQr.rowDescription')"
    stack="sm"
  >
    <Button
      size="sm"
      :loading="isStarting"
      @click="start"
    >
      <QrCode />
      {{ $t('bots.channels.feishuQr.start') }}
    </Button>
  </SettingsRow>

  <!-- Same rhythm as DeviceCodePanel: one hint → the QR code → the single
       action → the countdown, evenly spaced. The QR image carries its own
       white quiet zone, so it reads the same in light and dark. -->
  <div
    v-else-if="state === 'showing'"
    class="flex flex-col items-center gap-4 px-4 py-6 text-center"
  >
    <p class="text-body text-muted-foreground">
      {{ $t('bots.channels.feishuQr.hint') }}
    </p>
    <img
      :src="qrImageDataUrl"
      :alt="$t('bots.channels.feishuQr.qrAlt')"
      class="size-64 rounded-md"
    >
    <div class="flex items-center gap-2">
      <Button
        as="a"
        variant="outline"
        size="sm"
        :href="verificationUrl"
        target="_blank"
        rel="noopener noreferrer"
      >
        {{ $t('bots.channels.feishuQr.openInFeishu') }}
        <ExternalLink />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        @click="cancel"
      >
        {{ $t('common.cancel') }}
      </Button>
    </div>
    <p class="text-body tabular-nums text-muted-foreground">
      {{ $t('bots.channels.feishuQr.expiresIn', { time: remainingLabel }) }}
    </p>
  </div>

  <div
    v-else
    class="flex flex-col items-center gap-4 px-4 py-6 text-center"
  >
    <p
      class="text-body"
      :class="endReason === 'denied' ? 'text-muted-foreground' : 'text-destructive'"
    >
      {{ endMessage }}
    </p>
    <Button
      variant="outline"
      size="sm"
      :loading="isStarting"
      loading-mode="manual"
      @click="start"
    >
      <Spinner v-if="isStarting" />
      <RefreshCw v-else />
      {{ $t('bots.channels.feishuQr.retry') }}
    </Button>
  </div>
</template>

<script setup lang="ts">
import { Button, SettingsRow, Spinner, toast } from '@felinic/ui'
import { ExternalLink, QrCode, RefreshCw } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { postBotsByIdChannelFeishuRegistrationPoll, postBotsByIdChannelFeishuRegistrationStart } from '@memohai/sdk'
import type { HandlersFeishuRegistrationPollResponse } from '@memohai/sdk'
import { apiErrorStatus, resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
}>()

const emit = defineEmits<{
  /** The new app was saved as this bot's Feishu channel. */
  saved: []
}>()

type State = 'idle' | 'showing' | 'ended'
type EndReason = 'expired' | 'denied' | 'failed'
type PollStatus = NonNullable<HandlersFeishuRegistrationPollResponse['status']>

const { t } = useI18n()

const state = ref<State>('idle')
const endReason = ref<EndReason>('expired')
const isStarting = ref(false)
const sessionId = ref('')
const verificationUrl = ref('')
const qrImageDataUrl = ref('')
const expiresAtMs = ref(0)
const nowMs = ref(Date.now())

let pollIntervalMs = 5000
let pollTimer: ReturnType<typeof setTimeout> | undefined
let ticker: ReturnType<typeof setInterval> | undefined
// Bumped on every start / cancel / unmount so a poll answer that arrives for a
// session the user already left is dropped instead of reviving it.
let generation = 0

const remainingLabel = computed(() => {
  const totalSec = Math.ceil(Math.max(0, expiresAtMs.value - nowMs.value) / 1000)
  return `${Math.floor(totalSec / 60)}:${String(totalSec % 60).padStart(2, '0')}`
})

const endMessage = computed(() => {
  switch (endReason.value) {
    case 'denied': return t('bots.channels.feishuQr.denied')
    case 'failed': return t('bots.channels.feishuQr.failed')
    default: return t('bots.channels.feishuQr.expired')
  }
})

function stopTimers() {
  if (pollTimer !== undefined) {
    clearTimeout(pollTimer)
    pollTimer = undefined
  }
  if (ticker !== undefined) {
    clearInterval(ticker)
    ticker = undefined
  }
}

function end(reason: EndReason) {
  generation++
  stopTimers()
  endReason.value = reason
  state.value = 'ended'
}

function startTicker() {
  nowMs.value = Date.now()
  ticker = setInterval(() => {
    // A background tab skips the tick; visibilitychange recalibrates on return.
    if (document.visibilityState === 'hidden') return
    nowMs.value = Date.now()
    if (nowMs.value >= expiresAtMs.value) end('expired')
  }, 1000)
}

function onVisibilityChange() {
  if (document.visibilityState !== 'visible' || state.value !== 'showing') return
  nowMs.value = Date.now()
  if (nowMs.value >= expiresAtMs.value) end('expired')
}

async function start() {
  const current = ++generation
  stopTimers()
  isStarting.value = true
  try {
    const { data } = await postBotsByIdChannelFeishuRegistrationStart({ path: { id: props.botId }, throwOnError: true })
    if (current !== generation) return
    const url = data.verification_url ?? ''
    if (!data.session_id || !url) throw new Error('incomplete registration response')
    sessionId.value = data.session_id
    verificationUrl.value = url
    // Count down from receipt with the server's remaining seconds: the
    // browser clock may be skewed against the server's expires_at.
    expiresAtMs.value = Date.now() + Math.max(0, data.expires_in_seconds ?? 600) * 1000
    pollIntervalMs = Math.max(1, data.interval_seconds ?? 5) * 1000
    // The URL carries the app preset and scope addons (~350 chars), so the
    // code is dense (version 14). Measured with a decoder at 1x: 16rem with
    // the standard 4-module quiet zone decodes on light and dark cards, while
    // smaller sizes sit on the edge. Rendered at 2x for sharp modules.
    qrImageDataUrl.value = await QRCode.toDataURL(url, { width: 512, margin: 4 })
    if (current !== generation) return
    state.value = 'showing'
    startTicker()
    schedulePoll(current)
  } catch (err) {
    if (current !== generation) return
    toast.error(resolveApiErrorMessage(err, t('bots.channels.feishuQr.startFailed')))
  } finally {
    if (current === generation) isStarting.value = false
  }
}

function schedulePoll(current: number) {
  pollTimer = setTimeout(() => { void poll(current) }, pollIntervalMs)
}

async function poll(current: number) {
  let status: PollStatus | undefined
  try {
    const { data } = await postBotsByIdChannelFeishuRegistrationPoll({
      path: { id: props.botId },
      body: { session_id: sessionId.value },
      throwOnError: true,
    })
    status = data.status
  } catch (err) {
    if (current !== generation) return
    // A refused request (the bot got a Feishu channel meanwhile, access was
    // revoked) will not change by retrying; anything else is transient.
    const httpStatus = apiErrorStatus(err) ?? 0
    if (httpStatus >= 400 && httpStatus < 500) {
      toast.error(resolveApiErrorMessage(err, t('bots.channels.feishuQr.failed')))
      end('failed')
      return
    }
    schedulePoll(current)
    return
  }
  if (current !== generation) return
  switch (status) {
    case 'connected':
    case 'needs_activation':
    case 'saved_disabled':
      finish(status)
      return
    case 'denied':
    case 'expired':
    case 'failed':
      end(status)
      return
    default:
      schedulePoll(current)
  }
}

function finish(status: 'connected' | 'needs_activation' | 'saved_disabled') {
  // The saved channel turns the panel into edit mode, which unmounts this
  // section; until the refetch lands, fold the spent QR code away.
  cancel()
  if (status === 'connected') {
    toast.success(t('bots.channels.feishuQr.connected'))
  } else {
    toast.warning(t(status === 'needs_activation' ? 'bots.channels.feishuQr.needsActivation' : 'bots.channels.feishuQr.savedDisabled'), { duration: 12000 })
  }
  emit('saved')
}

function cancel() {
  generation++
  stopTimers()
  isStarting.value = false
  state.value = 'idle'
  sessionId.value = ''
  verificationUrl.value = ''
  qrImageDataUrl.value = ''
}

onMounted(() => {
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onBeforeUnmount(() => {
  generation++
  stopTimers()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>
