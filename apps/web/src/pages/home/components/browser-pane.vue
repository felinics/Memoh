<template>
  <div class="absolute inset-0 flex flex-col bg-background">
    <form
      class="h-11 shrink-0 flex items-center gap-2 px-2 border-b border-border bg-background"
      @submit.prevent="navigate"
    >
      <Globe class="size-4 shrink-0 text-muted-foreground" />
      <Input
        v-model="addressDraft"
        class="h-8 text-xs"
        :placeholder="t('chat.browser.placeholder')"
        :aria-label="t('chat.browser.address')"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
      />
      <Button
        type="submit"
        size="sm"
        class="h-8 gap-1.5 px-3"
        :disabled="status === 'connecting'"
      >
        <ArrowRight class="size-3.5" />
        {{ t('chat.browser.go') }}
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        class="size-8 shrink-0"
        :title="t('chat.browser.refresh')"
        :aria-label="t('chat.browser.refresh')"
        :disabled="status === 'connecting'"
        @click="refresh"
      >
        <RefreshCw class="size-4" />
      </Button>
      <span
        v-if="statusLabel"
        class="hidden sm:inline-flex max-w-52 truncate text-[11px] text-muted-foreground"
        :title="statusLabel"
      >
        {{ statusLabel }}
      </span>
    </form>

    <div class="relative flex-1 min-h-0 bg-card">
      <iframe
        v-if="frameSrc"
        :key="iframeKey"
        class="absolute inset-0 size-full border-0 bg-white"
        :src="frameSrc"
        sandbox="allow-downloads allow-forms allow-modals allow-popups allow-same-origin allow-scripts"
        @load="handleFrameLoad"
      />
      <!-- PanePlaceholder's title/loading/empty branches are a mutually-exclusive
           v-if/v-else-if/v-else chain, so exactly one renders — Vue forwards the
           passed-through `class` to whichever branch is active, same as a single-root
           component. That lets `absolute inset-0` land directly on the owner instead
           of an extra wrapper div. -->
      <PanePlaceholder
        v-else
        :title="emptyTitle"
        class="absolute inset-0"
      >
        <!-- #default must be named explicitly: a bare `<template v-if>` compiles as
             always-present implicit slot content ($slots.default stays truthy even
             when the condition is false), which defeats the owner's empty-subtitle
             guard. A named template + v-if compiles to a conditional slot entry, so
             the subtitle <p> truly disappears while connecting/idle. -->
        <template
          v-if="statusMessage"
          #default
        >
          {{ statusMessage }}
        </template>
      </PanePlaceholder>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Input, PanePlaceholder } from '@felinic/ui'
import { ArrowRight, Globe, RefreshCw } from 'lucide-vue-next'
import { sdkApiUrl } from '@/lib/api-client'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { defaultBrowserAddress, parseBrowserAddress } from '@/utils/browser-address'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'

const props = withDefaults(defineProps<{
  botId: string
  tabId: string
  address?: string
  active?: boolean
}>(), {
  address: '',
  active: false,
})

const { t } = useI18n()
const tabsStore = useWorkspaceTabsStore()

type BrowserStatus = 'idle' | 'connecting' | 'connected' | 'error' | 'expired'

const addressDraft = ref(props.address || defaultBrowserAddress())
const frameSrc = ref('')
const iframeKey = ref(0)
const status = ref<BrowserStatus>('idle')
const statusMessage = ref('')
const sessionId = ref('')

let requestSeq = 0
let keepaliveTimer: ReturnType<typeof setTimeout> | null = null
const keepaliveSkewMs = 60_000
const minKeepaliveDelayMs = 5_000

const statusLabel = computed(() => {
  if (status.value === 'expired') return t('chat.browser.status.expired')
  if (status.value === 'error') return statusMessage.value || t('chat.browser.status.error')
  return ''
})

const emptyTitle = computed(() => {
  if (status.value === 'connecting') return t('chat.browser.status.connecting')
  if (status.value === 'expired') return t('chat.browser.status.expired')
  if (status.value === 'error') return t('chat.browser.status.error')
  return t('chat.browser.title')
})

function authHeaders(): HeadersInit {
  const headers: Record<string, string> = {}
  const token = localStorage.getItem('token')?.trim()
  if (token) headers.Authorization = `Bearer ${token}`
  return headers
}

async function readErrorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const data = await response.json() as { message?: string, error?: string }
    return data.message || data.error || fallback
  } catch {
    const text = await response.text().catch(() => '')
    return text || fallback
  }
}

function clearKeepaliveTimer() {
  if (keepaliveTimer) {
    clearTimeout(keepaliveTimer)
    keepaliveTimer = null
  }
}

function scheduleKeepalive(expiresAt: string) {
  clearKeepaliveTimer()
  const deadline = Date.parse(expiresAt)
  if (!Number.isFinite(deadline)) return
  const delay = Math.max(minKeepaliveDelayMs, deadline - Date.now() - keepaliveSkewMs)
  keepaliveTimer = setTimeout(() => {
    void keepaliveSession()
  }, delay)
}

function markExpired() {
  clearKeepaliveTimer()
  status.value = 'expired'
  statusMessage.value = t('chat.browser.status.expired')
  frameSrc.value = ''
  sessionId.value = ''
}

async function keepaliveSession(expectedSessionId = sessionId.value) {
  const sid = expectedSessionId.trim()
  if (!sid || sid !== sessionId.value || status.value !== 'connected') return
  try {
    const response = await fetch(sdkApiUrl({
      url: '/bots/{bot_id}/container/browser/sessions/{session_id}/keepalive',
      path: { bot_id: props.botId, session_id: sid },
    }), {
      method: 'POST',
      headers: authHeaders(),
    })
    if (sid !== sessionId.value) return
    if (!response.ok) {
      if (response.status === 404) {
        markExpired()
      } else if (response.status === 401 || response.status === 403) {
        clearKeepaliveTimer()
        status.value = 'error'
        statusMessage.value = await readErrorMessage(response, t('chat.browser.status.error'))
        frameSrc.value = ''
      } else {
        scheduleKeepalive(new Date(Date.now() + minKeepaliveDelayMs).toISOString())
      }
      return
    }
    const payload = await response.json() as { expires_at?: string }
    if (sid === sessionId.value && payload.expires_at) {
      scheduleKeepalive(payload.expires_at)
    }
  } catch {
    if (sid === sessionId.value) {
      scheduleKeepalive(new Date(Date.now() + minKeepaliveDelayMs).toISOString())
    }
  }
}

async function deleteSession(id = sessionId.value) {
  const sid = id.trim()
  if (!sid || !props.botId) return
  try {
    await fetch(sdkApiUrl({
      url: '/bots/{bot_id}/container/browser/sessions/{session_id}',
      path: { bot_id: props.botId, session_id: sid },
    }), {
      method: 'DELETE',
      headers: authHeaders(),
    })
  } catch {
    // Session cleanup is best effort; expired sessions are pruned server-side.
  }
}

async function createSession(address: string) {
  const parsed = parseBrowserAddress(address)
  const seq = ++requestSeq
  status.value = 'connecting'
  statusMessage.value = ''
  frameSrc.value = ''

  const previousSession = sessionId.value
  sessionId.value = ''
  clearKeepaliveTimer()
  if (previousSession) void deleteSession(previousSession)

  const response = await fetch(sdkApiUrl({
    url: '/bots/{bot_id}/container/browser/sessions',
    path: { bot_id: props.botId },
  }), {
    method: 'POST',
    headers: {
      ...authHeaders(),
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ port: parsed.port, path: parsed.path }),
  })

  if (!response.ok) {
    throw new Error(await readErrorMessage(response, t('chat.browser.status.error')))
  }

  const payload = await response.json() as { id?: string, url?: string, expires_at?: string }
  if (seq !== requestSeq) {
    if (payload.id) void deleteSession(payload.id)
    return
  }
  if (!payload.id || !payload.url) {
    throw new Error(t('chat.browser.status.error'))
  }

  sessionId.value = payload.id
  addressDraft.value = parsed.display
  tabsStore.updateBrowserAddress(props.tabId, parsed.display)
  frameSrc.value = payload.url
  iframeKey.value += 1
  status.value = 'connected'
  if (payload.expires_at) scheduleKeepalive(payload.expires_at)
}

async function navigate() {
  if (!props.botId) return
  try {
    await createSession(addressDraft.value)
  } catch (error) {
    status.value = 'error'
    statusMessage.value = resolveApiErrorMessage(error, t('chat.browser.status.error'))
    frameSrc.value = ''
  }
}

function refresh() {
  if (!frameSrc.value || status.value === 'expired') {
    void navigate()
    return
  }
  iframeKey.value += 1
}

function handleFrameLoad() {
  if (status.value === 'connecting') {
    status.value = 'connected'
  }
}

onMounted(() => {
  void navigate()
})

onBeforeUnmount(() => {
  clearKeepaliveTimer()
  const sid = sessionId.value
  sessionId.value = ''
  if (sid) void deleteSession(sid)
})
</script>
