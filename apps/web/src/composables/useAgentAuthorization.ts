import { computed, onDeactivated, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  deleteAgentAuthorizationsById, getAgentAuthorizationsById, postAgentAuthorizations,
  postAgentAuthorizationsByIdPoll, postAgentAuthorizationsByIdExchange, type AgentcredentialAuthorization,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { safeSessionGet, safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'

export function readAgentAuthorizationDraft(key: string): { id: string, runtime: string } | null {
  try {
    const value = JSON.parse(safeSessionGet(key) || 'null')
    return typeof value?.id === 'string' && ['codex', 'claude-code'].includes(value.runtime) ? value : null
  } catch { return null }
}

export function useAgentAuthorization(getRuntime: () => string, storageKey: string) {
  const { t } = useI18n()
  const session = ref<AgentcredentialAuthorization | null>(null)
  const loading = ref(false)
  const error = ref('')
  const now = ref(Date.now())
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let transferred = false
  const pending = computed(() => session.value?.status === 'pending')
  const ready = computed(() => session.value?.status === 'ready'
    && session.value.runtime === getRuntime() && Date.parse(session.value.expires_at) > now.value)
  const busy = computed(() => loading.value || pending.value)

  function clearTimer() { clearTimeout(timer); timer = undefined }
  async function discard(id: string) {
    try { await deleteAgentAuthorizationsById({ path: { id }, throwOnError: true }) } catch { /* Server TTL also bounds abandoned sessions. */ }
  }
  function cancel() {
    generation += 1
    clearTimer()
    const id = session.value?.id ?? readAgentAuthorizationDraft(storageKey)?.id
    session.value = null
    loading.value = false
    error.value = ''
    safeSessionRemove(storageKey)
    if (id) void discard(id)
  }
  function accept(value: AgentcredentialAuthorization) {
    session.value = value
    now.value = Date.now()
    safeSessionSet(storageKey, JSON.stringify({ id: value.id, runtime: value.runtime }))
    clearTimer()
    const current = generation
    const remaining = Date.parse(value.expires_at) - now.value
    const devicePending = value.status === 'pending' && value.auth_kind === 'openai_codex_oauth'
    if (remaining <= 0) { cancel(); error.value = t('errors.agent_authorization.expired'); return }
    timer = setTimeout(() => {
      now.value = Date.now()
      if (current !== generation) return
      if (Date.parse(value.expires_at) <= now.value) {
        cancel()
        error.value = t('errors.agent_authorization.expired')
      } else if (devicePending) { void poll(current) }
    }, devicePending ? Math.min(remaining, Math.max(5, value.interval_seconds ?? 5) * 1000) : remaining)
  }
  async function poll(current: number) {
    const id = session.value?.id
    if (!id) return
    try {
      const { data } = await postAgentAuthorizationsByIdPoll({ path: { id }, throwOnError: true })
      if (current === generation) accept(data)
    } catch (cause) {
      if (current !== generation) return
      cancel()
      error.value = resolveApiErrorMessage(cause, t('errors.agent_authorization.failed'))
    }
  }
  async function start(authKind: string, secret?: string) {
    if (loading.value) return
    cancel()
    transferred = false
    const current = generation
    loading.value = true
    try {
      const { data } = await postAgentAuthorizations({
        body: { runtime: getRuntime() as 'codex' | 'claude-code', auth_kind: authKind,
          ...(secret && { secret: { [authKind === 'claude_code_oauth' ? 'oauth_token' : 'api_key']: secret } }) },
        throwOnError: true,
      })
      if (current !== generation) { void discard(data.id); return }
      accept(data)
    } catch (cause) {
      if (current === generation) error.value = resolveApiErrorMessage(cause, t('errors.agent_authorization.failed'))
    } finally { if (current === generation) loading.value = false }
  }
  async function restore() {
    const draft = readAgentAuthorizationDraft(storageKey)
    if (!draft || draft.runtime !== getRuntime()) return
    const current = generation
    loading.value = true
    try {
      const { data } = await getAgentAuthorizationsById({ path: { id: draft.id }, throwOnError: true })
      if (current !== generation) return
      if (data.status === 'claimed') { safeSessionRemove(storageKey); return }
      accept(data)
    } catch (cause) {
      if (current !== generation) return
      safeSessionRemove(storageKey)
      error.value = resolveApiErrorMessage(cause, t('errors.agent_authorization.expired'))
    } finally { if (current === generation) loading.value = false }
  }
  async function exchange(code: string) {
    const id = session.value?.id
    if (!id || loading.value || !pending.value || !code.trim()) return
    const current = generation
    loading.value = true
    error.value = ''
    try {
      const { data } = await postAgentAuthorizationsByIdExchange({ path: { id }, body: { code: code.trim() }, throwOnError: true })
      if (current === generation) accept(data)
    } catch (cause) {
      if (current === generation) error.value = resolveApiErrorMessage(cause, t('errors.agent_authorization.failed'))
    } finally { if (current === generation) loading.value = false }
  }
  // The create-progress store owns this ID after navigation. No secret is ever
  // handed to the store or browser storage, and leaving the form must not revoke it.
  function handoff() {
    transferred = true
    clearTimer()
    safeSessionRemove(storageKey)
  }
  function resume() {
    transferred = false
    if (session.value) accept(session.value)
  }
  watch(getRuntime, () => { cancel(); transferred = false })
  function dispose() { if (transferred) clearTimer(); else cancel() }
  onDeactivated(dispose)
  onUnmounted(dispose)
  void restore()
  return { session, ready, pending, busy, loading, error, start, exchange, cancel, handoff, resume }
}
