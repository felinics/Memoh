<template>
  <div class="flex h-full overflow-hidden">
    <ChatWorkspace v-if="currentBotId" />
    <div
      v-else
      class="flex-1 bg-card"
    >
      <PanePlaceholder :title="t('chat.selectBot')">
        {{ t('chat.selectBotHint') }}
      </PanePlaceholder>
    </div>
  </div>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getBotsByBotIdAgents, getBotsById } from '@memohai/sdk'
import { PanePlaceholder } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { ACP_NO_PROJECT_MODE, createACPNoProjectPath, normalizeACPAgentID } from '@/utils/acp'
import { botAgentProvider } from '@/utils/bot-agent'
import ChatWorkspace from './components/chat-workspace.vue'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()
const { currentBotId, bots } = storeToRefs(chatStore)

// Resolve a bot UUID from a URL name slug. Prefers the already-loaded bot list,
// falling back to the API (which accepts both name and UUID identifiers).
async function resolveBotIdFromName(nameOrId: string): Promise<string | null> {
  const value = nameOrId.trim()
  if (!value) return null
  const cached = bots.value.find((b) => b.name === value || b.id === value)
  if (cached?.id) return cached.id
  try {
    const { data } = await getBotsById({ path: { id: value }, throwOnError: true })
    return data?.id ?? null
  } catch {
    return null
  }
}

// Resolve a URL name slug from a bot UUID, preferring the loaded bot list.
async function resolveBotNameFromId(botId: string): Promise<string | null> {
  const value = botId.trim()
  if (!value) return null
  const cached = bots.value.find((b) => b.id === value)
  if (cached?.name) return cached.name
  try {
    const { data } = await getBotsById({ path: { id: value }, throwOnError: true })
    return data?.name ?? null
  } catch {
    return null
  }
}

let suppressUrlSync = false

// home is now mounted persistently (via main-container, which App.vue keeps
// alive across chat↔settings) rather than torn down when leaving chat. So these
// route watchers keep running while the user is in settings. The settings route
// shares the :botName param, so without this guard the route.params.botName
// watcher would "canonicalize" that UUID and router.replace back to /bot/<name>,
// yanking the user out of settings. URL sync must only run while a chat route
// (home/bot) is current. route.name is the reliable signal (no mount/unmount
// timing to race).
const CHAT_ROUTE_NAMES = new Set(['home', 'bot'])
const isChatRoute = () => CHAT_ROUTE_NAMES.has(route.name as string)

// One-shot guard so concurrent syncStoreFromUrl() calls can't both start a
// session for the same redirect. Set synchronously before the first await.
let agentStartConsumed = false

function stripAgentQuery() {
  if (route.query.agent === undefined && route.query.acp === undefined) return
  const query = { ...route.query }
  delete query.agent
  delete query.acp
  void router.replace({ query })
}

// When onboarding redirects here with ?agent=<id>, open an External Agent
// session so the user lands inside it. The old ?acp= spelling remains a
// read-only compatibility alias for links created before this rename.
async function maybeStartExternalAgentSession() {
  if (agentStartConsumed) return
  const raw = route.query.agent ?? route.query.acp
  if (typeof raw !== 'string' || raw === '') {
    stripAgentQuery()
    return
  }
  agentStartConsumed = true
  const agentId = normalizeACPAgentID(raw)
  try {
    const botId = currentBotId.value?.trim() ?? ''
    if (agentId && botId) {
      const { data } = await getBotsByBotIdAgents({ path: { bot_id: botId }, throwOnError: true })
      const botAgent = data.items?.find(agent => agent.enabled !== false && botAgentProvider(agent) === agentId)
      if (!botAgent?.id) return
      const { session } = await chatStore.createExternalAgentSession({
        botAgentId: botAgent.id,
        agentId,
        projectMode: ACP_NO_PROJECT_MODE,
        projectPath: createACPNoProjectPath(),
      })
      // Open (or focus) the tab for the freshly created session; activation selects
      // it. ensureChatPanel covers the case where the dock mounts later.
      workspaceTabs.openSessionChat({ sessionId: session.id })
    }
  } catch {
    // Bot may not have the agent enabled; user can still pick it from the composer.
  } finally {
    // Always strip the one-shot query param, even for malformed/empty values.
    stripAgentQuery()
  }
}

async function syncStoreFromUrl(rawName: string) {
  const urlName = rawName.trim()
  if (!urlName) {
    if (!currentBotId.value) {
      await chatStore.initializeWithRecovery()
    }
    await maybeStartExternalAgentSession()
    return
  }
  const resolvedId = await resolveBotIdFromName(urlName)
  if (!resolvedId) return
  if (resolvedId !== (currentBotId.value ?? '').trim()) {
    suppressUrlSync = true
    try {
      await chatStore.selectBot(resolvedId)
    } finally {
      suppressUrlSync = false
    }
  }
  await maybeStartExternalAgentSession()
  // Canonicalize the URL to the bot's name slug. This covers entry points that
  // navigate with a UUID (e.g. returning from settings), where currentBotId is
  // unchanged so the watcher below never fires.
  const canonicalName = await resolveBotNameFromId(resolvedId)
  if (canonicalName && urlName !== canonicalName) {
    void router.replace({ name: 'bot', params: { botName: canonicalName } })
  }
}

watch(
  () => [route.name, route.params.botName] as const,
  ([routeName, paramBotName], prev) => {
    if (!CHAT_ROUTE_NAMES.has(routeName as string)) return
    // Returning to chat from a non-chat route (settings overlay): per-bot config
    // edited there — enabled agents, model, name — won't have reached the store,
    // which loads bots once and isn't wired to the settings query cache. Re-pull
    // so the composer's agent menu reflects the change. Fire-and-forget; the bot
    // list swaps in place and currentBot recomputes. Skips the initial run (no
    // prev) since initialize() already loads a fresh list. Desktop is a separate
    // window whose route never enters settings — it refreshes via the
    // cross-window invalidate listener in the renderer bootstrap instead.
    const prevName = prev?.[0] as string | undefined
    if (prevName !== undefined && !CHAT_ROUTE_NAMES.has(prevName)) {
      void chatStore.refreshBots()
    }
    void syncStoreFromUrl((paramBotName as string) ?? '')
  },
  { immediate: true },
)

watch(currentBotId, async (newBotId) => {
  if (suppressUrlSync) return
  // Don't touch the URL while a non-chat route (e.g. settings) is current; home
  // stays mounted during /settings route changes and must not redirect away from it.
  if (!isChatRoute()) return
  const storeBot = (newBotId ?? '').trim()
  if (!storeBot) {
    if (route.name !== 'home') {
      void router.replace({ name: 'home' })
    }
    return
  }
  const botName = await resolveBotNameFromId(storeBot)
  if (!botName) return
  if (((route.params.botName as string) ?? '').trim() === botName) return
  void router.replace({
    name: 'bot',
    params: { botName },
  })
})

</script>
