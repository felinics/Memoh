import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref, watch, type Ref } from 'vue'
import { getWebhookTunnelStatus, type WebhooktunnelStatus } from '@memohai/sdk'
import { normalizePublicWebhookBase } from '@/utils/webhook-public-base'

type PublicBaseState = { url: string; reason: 'missing' | 'invalid' | '' }

export function useLineWebhookPublicBase(isLineWebhook: Readonly<Ref<boolean>>) {
  const webhookTunnelStatus = ref<WebhooktunnelStatus | null>(null)
  const isPanelActive = ref(false)
  let webhookTunnelTimer: ReturnType<typeof setInterval> | null = null
  let webhookTunnelRequestSeq = 0

  const publicBase = computed<PublicBaseState>(() => {
    const raw = webhookTunnelStatus.value?.status === 'ready'
      ? String(webhookTunnelStatus.value.public_base_url || '').trim()
      : ''
    if (!raw) return { url: '', reason: 'missing' }
    const normalized = normalizePublicWebhookBase(raw)
    return normalized ? { url: normalized, reason: '' } : { url: '', reason: 'invalid' }
  })

  const warningKey = computed(() => {
    if (!isLineWebhook.value || publicBase.value.url) return ''
    if (publicBase.value.reason === 'invalid') return 'bots.channels.lineWebhookPublicBaseInvalid'
    if (webhookTunnelStatus.value?.enabled) {
      return webhookTunnelStatus.value.status === 'starting'
        ? 'bots.channels.lineWebhookTunnelStarting'
        : 'bots.channels.lineWebhookTunnelUnavailable'
    }
    return 'bots.channels.lineWebhookPublicBaseMissing'
  })

  watch(isLineWebhook, (enabled) => {
    if (enabled) {
      if (isPanelActive.value) startPolling()
    } else {
      stopPolling()
      webhookTunnelStatus.value = null
    }
  }, { immediate: true })

  onMounted(() => {
    isPanelActive.value = true
    document.addEventListener('visibilitychange', handleVisibilityChange)
    if (isLineWebhook.value) startPolling()
  })

  onActivated(() => {
    isPanelActive.value = true
    if (isLineWebhook.value) startPolling()
  })

  onDeactivated(() => {
    isPanelActive.value = false
    stopPolling()
  })

  onUnmounted(() => {
    isPanelActive.value = false
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    stopPolling()
  })

  async function refreshStatus() {
    if (!canRefresh()) return
    const requestSeq = ++webhookTunnelRequestSeq
    try {
      const { data } = await getWebhookTunnelStatus({ throwOnError: true })
      if (requestSeq !== webhookTunnelRequestSeq || !canRefresh()) return
      webhookTunnelStatus.value = data
    } catch {
      if (requestSeq !== webhookTunnelRequestSeq || !canRefresh()) return
      webhookTunnelStatus.value = null
    }
  }

  function startPolling() {
    if (!canRefresh()) return
    if (webhookTunnelTimer) return
    void refreshStatus()
    webhookTunnelTimer = setInterval(() => {
      void refreshStatus()
    }, 5000)
  }

  function stopPolling() {
    webhookTunnelRequestSeq++
    if (webhookTunnelTimer) {
      clearInterval(webhookTunnelTimer)
      webhookTunnelTimer = null
    }
  }

  function canRefresh() {
    return isPanelActive.value && isLineWebhook.value && !isDocumentHidden()
  }

  function isDocumentHidden() {
    return document.visibilityState === 'hidden'
  }

  function handleVisibilityChange() {
    if (!isLineWebhook.value || !isPanelActive.value) return
    if (isDocumentHidden()) {
      stopPolling()
    } else {
      startPolling()
    }
  }

  return {
    publicBase,
    warningKey,
  }
}
