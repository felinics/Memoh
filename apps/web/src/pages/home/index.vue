<template>
  <div class="flex h-full overflow-hidden">
    <ChatWorkspace v-if="currentBotId && !urlBotPending && !urlBotFailedName" />
    <div
      v-else
      class="flex-1 bg-card"
    >
      <PanePlaceholder
        v-if="missingBotName"
        :title="t('bots.notFound')"
      >
        {{ t('bots.notFoundDescription') }}
      </PanePlaceholder>
      <PanePlaceholder v-else-if="urlBotFailedName">
        {{ t('bots.loadFailed') }}
        <template #action>
          <Button
            variant="outline"
            @click="retryBotFromUrl"
          >
            {{ t('common.retry') }}
          </Button>
        </template>
      </PanePlaceholder>
      <PanePlaceholder
        v-else-if="!urlBotPending"
        :title="t('chat.selectBot')"
      >
        {{ t('chat.selectBotHint') }}
      </PanePlaceholder>
    </div>
  </div>
</template>

<script setup lang="ts">
import { normalizeAgentID } from '@/utils/external-agent'
import { nextTick, ref, watch } from 'vue'
import { useTitle } from '@vueuse/core'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getBotsByBotIdAgents, getBotsById } from '@memohai/sdk'
import { fetchBot } from '@/composables/api/useBot'
import { Button, PanePlaceholder } from '@felinic/ui'
import { CHAT_ROUTE_NAMES } from '@/constants/chat-routes'
import { useChatStore } from '@/store/chat-list'
import { routeConversationLabel } from '@/store/chat-list.utils'
import { useComposerPrefillStore } from '@/store/composer-prefill'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { useTabScopedStorage } from '@/utils/tab-scoped-storage'
import { ACP_NO_PROJECT_MODE, createACPNoProjectPath } from '@/utils/acp'
import { botAgentProvider } from '@/utils/bot-agent'
import ChatWorkspace from './components/chat-workspace.vue'
import { useComposerPrefillResolver } from './composables/useComposerPrefillResolver'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()
const { currentBotId, bots, activeSession } = storeToRefs(chatStore)
const composerPrefill = useComposerPrefillStore()

useComposerPrefillResolver({
  pending: () => composerPrefill.pending,
  currentBotId: () => currentBotId.value,
  // Wait out a bot switch: until the new bot's chats load, the dock may still
  // show the previous bot's panels.
  dockReady: () => !!workspaceTabs.api && !chatStore.loadingChats,
  activeChatWritable: () => !!workspaceTabs.activeId && workspaceTabs.activePanelIsChat && !chatStore.activeChatReadOnly,
  openDraftChat: () => workspaceTabs.openDraftChat({ title: t('chat.newSession'), explicitSelection: false }),
})

// Resolve a bot UUID from a URL name slug. Prefers the already-loaded bot list,
// falling back to the API (which accepts both name and UUID identifiers).
// Returns null when the server reports no such bot; throws on other failures
// so a transient error isn't mistaken for a missing bot.
async function resolveBotIdFromName(nameOrId: string): Promise<string | null> {
  const value = nameOrId.trim()
  if (!value) return null
  const cached = bots.value.find((b) => b.name === value || b.id === value)
  if (cached?.id) return cached.id
  const bot = await fetchBot(value)
  return bot?.id ?? null
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

let suppressUrlSync = 0

// The URL name of the bot this tab last had open, as `<bot id>\n<name>`,
// seeded across reloads like the bot id itself. On a cold start the bot list
// is not loaded yet; this tells without a request whether `/bot/<name>` is the
// remembered bot (its chat renders at once) or another one.
const currentBotRoute = useTabScopedStorage<string>('chat-bot-route', '', { seed: true })
watch([currentBotId, bots], ([botId, list]) => {
  const id = (botId ?? '').trim()
  const name = list.find(bot => bot.id === id)?.name?.trim()
  if (id && name) currentBotRoute.value = `${id}\n${name}`
}, { immediate: true })

function currentBotUrlName(): string {
  const id = (currentBotId.value ?? '').trim()
  const listed = bots.value.find(bot => bot.id === id)?.name?.trim()
  if (listed) return listed
  const [rememberedId, rememberedName] = currentBotRoute.value.split('\n')
  return rememberedId === id ? rememberedName ?? '' : ''
}

// Set while the URL names a bot other than the selected one and that bot is
// still being looked up. The selected bot's chat is not shown meanwhile:
// typing into it would land in the wrong bot's draft, and its composer would
// be swapped out the moment the lookup returns.
const urlBotPending = ref(false)
let urlSyncRun = 0

// The URL's bot identifier when the server has no such bot. While set, the
// pane shows the not-found state and the URL is left as the user typed it.
const missingBotName = ref('')
// A different bot's lookup failed. Keep the selected bot hidden rather than
// presenting its composer under this URL; the user can retry the lookup.
const urlBotFailedName = ref('')

// home is now mounted persistently (via main-container, which App.vue keeps
// alive across chat↔settings) rather than torn down when leaving chat. So these
// route watchers keep running while the user is in settings. The settings route
// shares the :botName param, so without this guard the route.params.botName
// watcher would "canonicalize" that UUID and router.replace back to /bot/<name>,
// yanking the user out of settings. URL sync must only run while a chat route
// (home/bot) is current. route.name is the reliable signal (no mount/unmount
// timing to race).
const isChatRoute = () => CHAT_ROUTE_NAMES.has(route.name as string)

function retryBotFromUrl() {
  if (!isChatRoute()) return
  void syncStoreFromUrl((route.params.botName as string) ?? '')
}

// Home remains mounted behind settings, so the route gates the browser title.
// A first send's session is titled like the draft it came from until the
// server confirms the send (see isSessionTentative).
useTitle(() => {
  const session = activeSession.value
  if (!isChatRoute() || !currentBotId.value || !session || chatStore.isSessionTentative(session.id)) return 'Memoh'
  const title = (session.title ?? '').trim() || routeConversationLabel(session) || t('chat.untitledSession')
  return `Memoh · ${title}`
}, { restoreOnUnmount: () => 'Memoh' })

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
  const agentId = normalizeAgentID(raw)
  try {
    const botId = currentBotId.value?.trim() ?? ''
    if (agentId && botId) {
      const { data } = await getBotsByBotIdAgents({ path: { bot_id: botId }, throwOnError: true })
      const botAgent = data.items?.find(agent => agent.enabled !== false && botAgentProvider(agent) === agentId)
      if (!botAgent?.id) return
      const startedOnPanel = workspaceTabs.activeId
      const { session } = await chatStore.createExternalAgentSession({
        botAgentId: botAgent.id,
        agentId,
        projectMode: ACP_NO_PROJECT_MODE,
        projectPath: createACPNoProjectPath(),
      })
      // Creation is async. If the user moved to another tab meanwhile, leave the
      // dock alone: opening a tab now would replace the group's preview tab,
      // which may be the one they are reading. A draft that was focused when
      // creation started already shows the session, and Recents lists it.
      if (workspaceTabs.activeId === startedOnPanel) {
        workspaceTabs.openSessionChat({ sessionId: session.id })
      }
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
  const run = ++urlSyncRun
  const selectedId = (currentBotId.value ?? '').trim()
  const urlMatchesSelected = !!urlName && !!selectedId
    && (urlName === selectedId || urlName === currentBotUrlName())
  // Every await can finish after a newer navigation. Only the run that still
  // owns this exact chat URL may change the selection or settle the pane.
  const isCurrent = () => run === urlSyncRun && isChatRoute()
    && (((route.params.botName as string) ?? '').trim() === urlName)
  urlBotFailedName.value = ''
  missingBotName.value = ''
  urlBotPending.value = !!urlName && !urlMatchesSelected
  const settleUrlBot = () => { if (run === urlSyncRun) urlBotPending.value = false }
  if (!urlName) {
    if (!currentBotId.value) {
      await chatStore.initializeWithRecovery()
    }
    if (!isCurrent()) return
    await maybeStartExternalAgentSession()
    if (!isCurrent()) return
    // A bot selected before this page mounted (remembered for the tab, or
    // restored at sign-in) never reaches the currentBotId watcher below, so
    // move `/` to that bot's URL here.
    const selected = (currentBotId.value ?? '').trim()
    const botName = selected ? await resolveBotNameFromId(selected) : null
    if (botName && isCurrent() && !((route.params.botName as string) ?? '').trim()
      && (currentBotId.value ?? '').trim() === selected) {
      void router.replace({ name: 'bot', params: { botName } })
    }
    return
  }
  let resolvedId: string | null
  try {
    resolvedId = await resolveBotIdFromName(urlName)
  } catch {
    if (!isCurrent()) return
    // A URL already known to name the selected bot needs no server answer to
    // keep its composer. Only an unresolved, different bot becomes an error.
    if (!urlMatchesSelected || (currentBotId.value ?? '').trim() !== selectedId) {
      urlBotFailedName.value = urlName
    }
    settleUrlBot()
    return
  }
  if (!isCurrent()) return
  if (!resolvedId) {
    missingBotName.value = urlName
    // Drop the previously selected bot rather than showing its chat under
    // this URL. Clearing the selection resets the chat store, bot list
    // included, so reload the list for the bot switcher afterwards.
    if (currentBotId.value) {
      currentBotId.value = null
      await nextTick()
    }
    settleUrlBot()
    void chatStore.refreshBots()
    return
  }
  if (resolvedId !== (currentBotId.value ?? '').trim()) {
    suppressUrlSync += 1
    try {
      // selectBot switches the selection before it loads the bot, so the
      // URL's bot can render as soon as the call starts.
      const switching = chatStore.selectBot(resolvedId)
      settleUrlBot()
      await switching
    } finally {
      suppressUrlSync -= 1
    }
  } else {
    settleUrlBot()
  }
  if (!isCurrent()) return
  await maybeStartExternalAgentSession()
  if (!isCurrent()) return
  // Canonicalize the URL to the bot's name slug. This covers entry points that
  // navigate with a UUID (e.g. returning from settings), where currentBotId is
  // unchanged so the watcher below never fires.
  const canonicalName = await resolveBotNameFromId(resolvedId)
  if (canonicalName && isCurrent() && urlName !== canonicalName) {
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
    if (missingBotName.value) return
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
