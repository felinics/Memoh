import { computed, onDeactivated, onUnmounted, ref, watch } from 'vue'
import {
  getBotsByBotIdAgentsByIdCredential,
  postBotsByBotIdAgentsByIdCodexLoginDeviceAuthorize,
  postBotsByBotIdAgentsByIdCodexLoginDeviceCancel,
  postBotsByBotIdAgentsByIdCodexLoginDevicePoll,
} from '@memohai/sdk'
import { apiErrorStatus, resolveApiErrorMessage } from '@/utils/api-error'

export interface CodexDeviceLogin {
  login_id: string
  user_code: string
  verification_url: string
  status: 'pending' | 'success' | 'error' | 'unknown'
}

export function useCodexDeviceLogin(
  getBotId: () => string,
  getBotAgentId: () => string,
) {
  const authorized = ref(false)
  const loadingStatus = ref(false)
  const authorizing = ref(false)
  const error = ref('')
  const deviceLogin = ref<CodexDeviceLogin | null>(null)

  let pollTimer: ReturnType<typeof globalThis.setTimeout> | undefined
  let generation = 0

  const devicePending = computed(() => deviceLogin.value?.status === 'pending')
  const busy = computed(() => authorizing.value || devicePending.value)

  function target() {
    return {
      botId: getBotId().trim(),
      botAgentId: getBotAgentId().trim(),
    }
  }

  function clearPollTimer() {
    if (pollTimer === undefined) return
    globalThis.clearTimeout(pollTimer)
    pollTimer = undefined
  }

  function reset() {
    generation += 1
    clearPollTimer()
    authorized.value = false
    loadingStatus.value = false
    authorizing.value = false
    error.value = ''
    deviceLogin.value = null
  }

  async function loadStatus(): Promise<boolean> {
    const { botId, botAgentId } = target()
    if (!botId || !botAgentId) return false
    const currentGeneration = generation
    loadingStatus.value = true
    error.value = ''
    try {
      const { data } = await getBotsByBotIdAgentsByIdCredential({
        path: { bot_id: botId, id: botAgentId },
        throwOnError: true,
      })
      if (currentGeneration !== generation) return false
      authorized.value = !data.revoked && data.auth_kind === 'openai_codex_oauth'
      return authorized.value
    } catch (cause) {
      if (currentGeneration !== generation) return false
      authorized.value = false
      if (apiErrorStatus(cause) !== 404) error.value = resolveApiErrorMessage(cause, 'Authorization status failed')
      return false
    } finally {
      if (currentGeneration === generation) loadingStatus.value = false
    }
  }

  function schedulePoll(currentGeneration: number) {
    clearPollTimer()
    pollTimer = globalThis.setTimeout(() => {
      pollTimer = undefined
      void pollCodex(currentGeneration)
    }, 2000)
  }

  async function pollCodex(currentGeneration = generation): Promise<void> {
    const session = deviceLogin.value
    const { botId, botAgentId } = target()
    if (!session || !botId || !botAgentId || currentGeneration !== generation) return
    try {
      const { data } = await postBotsByBotIdAgentsByIdCodexLoginDevicePoll({
        path: { bot_id: botId, id: botAgentId },
        body: { login_id: session.login_id },
        throwOnError: true,
      })
      if (currentGeneration !== generation || session.login_id !== deviceLogin.value?.login_id) return
      const status = data.status as CodexDeviceLogin['status']
      deviceLogin.value = { ...session, status }
      if (status === 'success') {
        authorized.value = true
        clearPollTimer()
      } else if (status === 'pending') {
        schedulePoll(currentGeneration)
      } else {
        error.value = 'Codex account authorization failed'
        clearPollTimer()
      }
    } catch (cause) {
      if (currentGeneration !== generation) return
      error.value = resolveApiErrorMessage(cause, 'Codex account authorization failed')
      if (deviceLogin.value) deviceLogin.value = { ...deviceLogin.value, status: 'error' }
      clearPollTimer()
    }
  }

  async function authorizeCodex(): Promise<boolean> {
    const { botId, botAgentId } = target()
    if (!botId || !botAgentId || authorizing.value) return false
    const previous = deviceLogin.value
    if (previous?.status === 'pending') void cancelSession(botId, botAgentId, previous.login_id)
    reset()
    authorizing.value = true
    const currentGeneration = generation
    try {
      const { data } = await postBotsByBotIdAgentsByIdCodexLoginDeviceAuthorize({
        path: { bot_id: botId, id: botAgentId },
        throwOnError: true,
      })
      if (currentGeneration !== generation) {
        if (data.login_id) void cancelSession(botId, botAgentId, data.login_id)
        return false
      }
      const loginId = data.login_id?.trim()
      const userCode = data.user_code?.trim()
      const verificationURL = data.verification_url?.trim()
      if (!loginId || !userCode || !verificationURL) throw new Error('Device authorization failed')
      deviceLogin.value = {
        login_id: loginId,
        user_code: userCode,
        verification_url: verificationURL,
        status: 'pending',
      }
      schedulePoll(currentGeneration)
      return true
    } catch (cause) {
      if (currentGeneration === generation) error.value = resolveApiErrorMessage(cause, 'Codex account authorization failed')
      return false
    } finally {
      if (currentGeneration === generation) authorizing.value = false
    }
  }

  async function cancelCodex(): Promise<void> {
    const session = deviceLogin.value
    const { botId, botAgentId } = target()
    generation += 1
    clearPollTimer()
    authorizing.value = false
    loadingStatus.value = false
    deviceLogin.value = null
    if (!session || !botId || !botAgentId || session.status !== 'pending') return
    await cancelSession(botId, botAgentId, session.login_id)
  }

  async function cancelSession(botId: string, botAgentId: string, loginId: string) {
    try {
      await postBotsByBotIdAgentsByIdCodexLoginDeviceCancel({
        path: { bot_id: botId, id: botAgentId },
        body: { login_id: loginId },
        throwOnError: true,
      })
    } catch {
      // Best effort only. The driver's in-memory login registry also expires.
    }
  }

  watch([getBotId, getBotAgentId], (_next, previous) => {
    const session = deviceLogin.value
    if (session?.status === 'pending') void cancelSession(previous[0], previous[1], session.login_id)
    reset()
  }, { flush: 'sync' })

  onDeactivated(() => { void cancelCodex() })
  onUnmounted(() => { void cancelCodex() })

  return {
    authorized,
    loadingStatus,
    authorizing,
    busy,
    error,
    deviceLogin,
    devicePending,
    loadStatus,
    authorizeCodex,
    cancelCodex,
    reset,
  }
}
