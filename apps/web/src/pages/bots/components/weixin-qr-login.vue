<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <div>
        <h4 class="text-xs font-medium">
          {{ $t('bots.channels.weixinQr.title') }}
        </h4>
        <p class="text-xs text-muted-foreground mt-1">
          {{ $t('bots.channels.weixinQr.description') }}
        </p>
      </div>
    </div>

    <!-- QR code display -->
    <div
      v-if="qrState === 'idle'"
      class="flex flex-col items-center gap-3 py-4"
    >
      <Button
        :loading="isStarting"
        @click="startLogin"
      >
        <QrCode class="mr-1.5 size-3.5" />
        {{ $t('bots.channels.weixinQr.startScan') }}
      </Button>
    </div>

    <div
      v-else-if="qrState === 'showing'"
      class="flex flex-col items-center gap-4 py-4"
    >
      <div class="relative rounded-lg border bg-white p-3">
        <img
          v-if="qrImageDataUrl"
          :src="qrImageDataUrl"
          alt="WeChat QR Code"
          class="size-52"
        >
        <div
          v-else
          class="size-52 flex items-center justify-center text-muted-foreground"
        >
          <Spinner />
        </div>

        <!-- Overlay for scanned state -->
        <div
          v-if="pollStatus === 'scanned' || pollStatus === 'need_verify_code'"
          class="absolute inset-0 flex items-center justify-center rounded-lg bg-background/80"
        >
          <div class="text-center">
            <Smartphone
              class="size-8 text-primary mb-2"
            />
            <p class="text-xs font-medium text-foreground">
              {{ $t('bots.channels.weixinQr.scanned') }}
            </p>
          </div>
        </div>

        <!-- Overlay for expired state; a blocked verification code also needs a fresh QR code -->
        <div
          v-if="pollStatus === 'expired' || pollStatus === 'verify_code_blocked'"
          class="absolute inset-0 flex flex-col items-center justify-center rounded-lg bg-background/80 gap-2"
        >
          <p class="text-xs text-muted-foreground text-center px-3">
            {{ pollStatus === 'expired' ? $t('bots.channels.weixinQr.expired') : $t('bots.channels.weixinQr.verifyCodeBlocked') }}
          </p>
          <Button
            size="sm"
            variant="outline"
            @click="startLogin"
          >
            {{ $t('bots.channels.weixinQr.refresh') }}
          </Button>
        </div>
      </div>

      <p
        class="text-xs text-center max-w-xs"
        :class="verifyCodeRejected ? 'text-destructive' : 'text-muted-foreground'"
      >
        {{ statusText }}
      </p>

      <!-- WeChat asks for the number shown on the phone before confirming the login -->
      <form
        v-if="pollStatus === 'need_verify_code'"
        class="flex w-full max-w-xs items-center gap-2"
        @submit.prevent="submitVerifyCode"
      >
        <Input
          v-model="verifyCodeInput"
          inputmode="numeric"
          autocomplete="one-time-code"
          :placeholder="$t('bots.channels.weixinQr.verifyCodePlaceholder')"
          class="flex-1"
        />
        <Button
          type="submit"
          size="sm"
          :disabled="!verifyCodeInput.trim()"
        >
          {{ $t('bots.channels.weixinQr.verifyCodeSubmit') }}
        </Button>
      </form>

      <Button
        variant="ghost"
        size="sm"
        @click="cancel"
      >
        {{ $t('common.cancel') }}
      </Button>
    </div>

    <div
      v-else-if="qrState === 'success'"
      class="flex flex-col items-center gap-3 py-4"
    >
      <div class="flex size-12 items-center justify-center rounded-full bg-success-soft">
        <Check
          class="size-5 text-success-foreground"
        />
      </div>
      <p class="text-xs font-medium">
        {{ alreadyBound ? $t('bots.channels.weixinQr.alreadyBound') : $t('bots.channels.weixinQr.success') }}
      </p>
    </div>

    <div
      v-else-if="qrState === 'error'"
      class="flex flex-col items-center gap-3 py-4"
    >
      <p class="text-xs text-destructive">
        {{ errorMessage }}
      </p>
      <Button
        variant="outline"
        size="sm"
        @click="startLogin"
      >
        {{ $t('bots.channels.weixinQr.retry') }}
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { QrCode, Smartphone, Check } from 'lucide-vue-next'
import { ref, computed, onUnmounted } from 'vue'
import { Button, Spinner } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { Input } from '@felinic/ui'
import QRCode from 'qrcode'
import { client } from '@memohai/sdk/client'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
}>()

const emit = defineEmits<{
  loginSuccess: []
}>()

const { t } = useI18n()

type QRState = 'idle' | 'showing' | 'success' | 'error'

interface WeixinQrStartResponse {
  qr_code_url?: string
  qr_code?: string
  message?: string
}

interface WeixinQrPollResponse {
  status?: string
  message?: string
  poll_host?: string
}

const qrState = ref<QRState>('idle')
const qrCode = ref('')
const qrImageDataUrl = ref('')
const pollStatus = ref('')
const isStarting = ref(false)
// The poll endpoint is stateless, so the login's iLink host and the entered
// verification code live here and ride along on every poll.
const pollHost = ref('')
const verifyCodeInput = ref('')
const pendingVerifyCode = ref('')
// need_verify_code coming back while a code was pending means it was wrong.
const verifyCodeRejected = ref(false)
const alreadyBound = ref(false)
const errorMessage = ref('')
let pollTimer: ReturnType<typeof setTimeout> | null = null
let aborted = false

const statusText = computed(() => {
  switch (pollStatus.value) {
    case 'wait':
      return t('bots.channels.weixinQr.waitingScan')
    case 'scanned':
      return t('bots.channels.weixinQr.scanned')
    case 'need_verify_code':
      return verifyCodeRejected.value
        ? t('bots.channels.weixinQr.verifyCodeWrong')
        : t('bots.channels.weixinQr.verifyCodePrompt')
    case 'verify_code_blocked':
      return t('bots.channels.weixinQr.verifyCodeBlocked')
    case 'expired':
      return t('bots.channels.weixinQr.expired')
    default:
      return t('bots.channels.weixinQr.waitingScan')
  }
})

async function startLogin() {
  aborted = false
  isStarting.value = true
  errorMessage.value = ''
  pollStatus.value = ''
  qrImageDataUrl.value = ''
  resetLoginState()

  try {
    const { data } = await client.post<{ 200: WeixinQrStartResponse }, unknown, true>({
      url: '/bots/{bot_id}/channel/weixin/qr/start',
      path: { bot_id: props.botId },
      body: {},
      throwOnError: true,
    })
    const qrContent = data.qr_code_url || data.qr_code || ''
    if (!qrContent) {
      throw new Error('No QR code data returned')
    }

    qrCode.value = data.qr_code || ''
    qrImageDataUrl.value = await QRCode.toDataURL(qrContent, { width: 208, margin: 1 })
    qrState.value = 'showing'

    startPolling()
  } catch (err) {
    errorMessage.value = resolveApiErrorMessage(err, err instanceof Error ? err.message : String(err))
    qrState.value = 'error'
  } finally {
    isStarting.value = false
  }
}

function startPolling() {
  if (aborted) return
  pollOnce()
}

async function pollOnce() {
  if (aborted || qrState.value !== 'showing') return

  try {
    const { data } = await client.post<{ 200: WeixinQrPollResponse }, unknown, true>({
      url: '/bots/{bot_id}/channel/weixin/qr/poll',
      path: { bot_id: props.botId },
      body: {
        qr_code: qrCode.value,
        poll_host: pollHost.value || undefined,
        verify_code: pendingVerifyCode.value || undefined,
      },
      throwOnError: true,
    })
    if (aborted) return
    pollStatus.value = data.status ?? ''
    if (data.poll_host) pollHost.value = data.poll_host

    switch (data.status) {
      case 'confirmed':
      case 'already_bound':
        alreadyBound.value = data.status === 'already_bound'
        qrState.value = 'success'
        toast.success(alreadyBound.value
          ? t('bots.channels.weixinQr.alreadyBound')
          : t('bots.channels.weixinQr.success'))
        emit('loginSuccess')
        return
      case 'need_verify_code':
        // Wait for the user to type the number; submitVerifyCode resumes polling.
        verifyCodeRejected.value = pendingVerifyCode.value !== ''
        pendingVerifyCode.value = ''
        verifyCodeInput.value = ''
        return
      case 'expired':
      case 'verify_code_blocked':
        return
      case 'scanned':
        // WeChat accepted the code (or never asked for one).
        pendingVerifyCode.value = ''
        verifyCodeRejected.value = false
        if (!aborted) {
          pollTimer = setTimeout(pollOnce, 1500)
        }
        return
      case 'wait':
        if (!aborted) {
          pollTimer = setTimeout(pollOnce, 1500)
        }
        return
      default:
        if (!aborted) {
          pollTimer = setTimeout(pollOnce, 2000)
        }
    }
  } catch {
    if (!aborted) {
      pollTimer = setTimeout(pollOnce, 3000)
    }
  }
}

function submitVerifyCode() {
  const code = verifyCodeInput.value.trim()
  if (!code || aborted) return
  pendingVerifyCode.value = code
  pollStatus.value = 'scanned'
  pollOnce()
}

function resetLoginState() {
  pollHost.value = ''
  verifyCodeInput.value = ''
  pendingVerifyCode.value = ''
  verifyCodeRejected.value = false
  alreadyBound.value = false
}

function cancel() {
  aborted = true
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
  qrState.value = 'idle'
  qrCode.value = ''
  qrImageDataUrl.value = ''
  pollStatus.value = ''
  resetLoginState()
}

onUnmounted(() => {
  aborted = true
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
})
</script>
