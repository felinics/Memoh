import { computed, watch, type Ref } from 'vue'
import type { ChatViewEntry, ChatViewTarget } from '@/store/chat-list'
import type { ChatWorkspaceTargetSelectionSource } from '@/store/chat/types'
import {
  fetchModelPreferenceSeed,
  fetchSession,
  updateSessionModelPreference,
  type ModelPreferenceSeed,
  type SessionSummary,
} from '@/composables/api/useChat'
import { carriedPairForSource, readComposerPairDraft, writeComposerPairDraft } from '../components/chat-pane-send'
import { isApiErrorCode } from '@/utils/api-error'

// The composer (model, effort) pair (issue #879). The values live on the
// shared ChatViewEntry so same-session tabs share one pair; this composable
// owns every rule that changes them — seeding, bot-default following, session
// reload, runtime-namespace resets, picker persistence and the send handshake
// — so the pane only reports user actions and reads the result.

export type ComposerPairSource = ChatWorkspaceTargetSelectionSource

export interface ComposerPair {
  modelId: string
  reasoningEffort: string
}

export interface ComposerPairSnapshot {
  modelId: string
  effort: string
  source: ComposerPairSource
}

export interface ComposerPairApi {
  fetchSession: (botId: string, sessionId: string) => Promise<SessionSummary>
  fetchSeed: (botId: string) => Promise<ModelPreferenceSeed>
  updatePreference: (botId: string, sessionId: string, modelId: string, effort: string, revision: string) => Promise<SessionSummary>
}

export interface ComposerPairDirectCatalog {
  configuredModelId: string
  defaultModelId: string
  configuredReasoningEffort: string
  defaultReasoningEffort: string
  reasoningEfforts?: Array<{ id?: string }>
}

export interface ComposerPairDeps {
  view: Ref<ChatViewEntry>
  target: Ref<ChatViewTarget>
  visible: Ref<boolean>
  botId: Ref<string | null | undefined>
  activeSession: Ref<SessionSummary | null | undefined>
  botSettings: Ref<{ chat_model_id?: string, reasoning_effort?: string } | null | undefined>
  pinnedSubagentModelId: Ref<string>
  usesExternalAgentComposer: Ref<boolean>
  usesDirectRuntime: Ref<boolean>
  usesACPRuntime: Ref<boolean>
  /**
   * Names the namespace the pair's model IDs belong to: runtime type, Agent,
   * ACP agent/project. When this changes under the SAME view the old pair is
   * meaningless (an external model ID handed to the native resolver fails the
   * send), so the pair is dropped and reloaded. A view change never resets:
   * each view carries its own pair.
   */
  runtimeIdentity: Ref<string>
  directCatalog: Ref<ComposerPairDirectCatalog>
  /** Draft→session promotion flickers several identity pieces; skip those ticks. */
  draftPromotionPending: () => boolean
  /** The picker's PATCH lost the revision race; the pane shows the conflict hint. */
  onPreferenceConflict?: (error: unknown) => void
  api?: Partial<ComposerPairApi>
}

export function useComposerPair(deps: ComposerPairDeps) {
  const api: ComposerPairApi = {
    fetchSession,
    fetchSeed: fetchModelPreferenceSeed,
    updatePreference: updateSessionModelPreference,
    ...deps.api,
  }

  const source = computed(() => deps.view.value.pairSource.value)

  // Only explicit sources (user pick, remembered session) travel with a send;
  // default/unset pairs are omitted so the server can tell "never picked"
  // from "picked the default". While a refresh is pending, sending confirms
  // whatever is displayed, including a cached default.
  function carriedFor(view: ChatViewEntry): ComposerPair {
    return carriedPairForSource(
      view.pairSync.refreshing.value ? 'session' : view.pairSource.value,
      view.pairModelId.value,
      view.pairEffort.value,
    )
  }
  const carried = computed(() => carriedFor(deps.view.value))

  function setPair(modelId: string, effort: string, src: ComposerPairSource, view: ChatViewEntry = deps.view.value) {
    view.pairModelId.value = modelId
    view.pairEffort.value = effort
    view.pairSource.value = src
  }
  function setSource(src: ComposerPairSource) {
    deps.view.value.pairSource.value = src
  }
  function selectModel(value: string): boolean {
    const view = deps.view.value
    const direct = deps.usesDirectRuntime.value
    // Default is an explicit pick of the model displayed by the catalog.
    // Resolve it before persisting or capturing a send: an empty model on
    // the wire means "reuse the session preference", not "restore default".
    const modelId = value.trim() || (direct
      ? deps.directCatalog.value.configuredModelId || deps.directCatalog.value.defaultModelId
      : '')
    if (direct && !modelId) return false
    view.pairModelId.value = modelId
    view.pairSource.value = 'user'
    if (direct) {
      // The catalog reacts to the selected model, so read it after setting
      // the ID and replace the old model's effort with the new one's default.
      const catalog = deps.directCatalog.value
      const effort = catalog.defaultReasoningEffort
      view.pairEffort.value = catalog.reasoningEfforts?.some(option => option.id === effort) ? effort : ''
    }
    return true
  }
  function snapshot(): ComposerPairSnapshot {
    const view = deps.view.value
    return { modelId: view.pairModelId.value, effort: view.pairEffort.value, source: view.pairSource.value }
  }
  function restore(saved: ComposerPairSnapshot) {
    setPair(saved.modelId, saved.effort, saved.source)
  }

  function reset(view: ChatViewEntry) {
    view.pairSync.invalidate()
    setPair('', '', 'unset', view)
  }

  // A send that carries a displayed-but-unconfirmed pair (refresh pending)
  // fixes it as the user's choice so later bot-default changes leave it alone.
  function confirmDisplayed(view: ChatViewEntry, pair: ComposerPair) {
    if (pair.modelId && (view.pairSource.value === 'default' || view.pairSource.value === 'unset')) {
      view.pairSource.value = 'user'
    }
  }

  function beginSend(view: ChatViewEntry = deps.view.value) {
    const pair = carriedFor(view)
    confirmDisplayed(view, pair)
    return { pair, finish: view.pairSync.beginSend() }
  }

  // handleSend prepares its snapshot before the send is admitted (attachments
  // may still be encoding, the text may turn out to be a command), so reads
  // are blocked immediately and the send itself begins later, or never.
  function captureSend(view: ChatViewEntry = deps.view.value) {
    const pair = carriedFor(view)
    const send = view.pairSync.prepareSend()
    return {
      pair,
      releaseReads: send.release,
      begin() {
        send.begin()
        confirmDisplayed(view, pair)
      },
      finish(confirmed: boolean) {
        send.finish(confirmed)
      },
    }
  }

  function applySessionPair(view: ChatViewEntry, sess: SessionSummary) {
    const direct = deps.usesDirectRuntime.value
    const catalog = deps.directCatalog.value
    const modelId = direct ? sess.preferred_external_model_id : sess.preferred_chat_model_id
    if (modelId) {
      setPair(modelId, sess.preferred_reasoning_effort || '', 'session', view)
      return
    }
    setPair(
      direct
        ? catalog.configuredModelId || catalog.defaultModelId
        : deps.pinnedSubagentModelId.value || deps.botSettings.value?.chat_model_id || '',
      direct ? catalog.configuredReasoningEffort : deps.botSettings.value?.reasoning_effort || 'medium',
      'default',
      view,
    )
  }

  // Picker persistence: PATCH when a session is open, per-bot localStorage
  // draft on the welcome composer. Both are best-effort — the next sent
  // message writes the resolved pair back server-side.
  function persist() {
    if (deps.usesACPRuntime.value) return
    const botId = deps.botId.value
    if (!botId) return
    const view = deps.view.value
    const modelId = view.pairModelId.value.trim()
    if (!modelId) return
    const effort = view.pairEffort.value.trim()
    const sessionId = deps.target.value.sessionId
    if (!sessionId) {
      if (!deps.usesExternalAgentComposer.value) writeComposerPairDraft(botId, { model_id: modelId, reasoning_effort: effort })
      return
    }
    void view.pairSync.write(
      () => api.fetchSession(botId, sessionId),
      row => api.updatePreference(botId, sessionId, modelId, effort, row.model_preference_revision || ''),
      // Applies to the captured view even if this pane was repointed since.
      // Read the column for THIS runtime's namespace, like applySessionPair:
      // an external id must never land in a native composer.
      (row) => {
        const applied = deps.usesDirectRuntime.value ? row.preferred_external_model_id : row.preferred_chat_model_id
        setPair(applied || modelId, row.preferred_reasoning_effort || '', 'session', view)
      },
      (error) => {
        if (!isApiErrorCode(error, 'session.model_preference_conflict')) return
        // The pick lost to a newer write elsewhere (another tab, a send).
        // Drop the unsaved protection, adopt the server's pair, and tell the
        // user their pick didn't stick.
        view.pairSync.dropUnsavedChoice()
        void view.pairSync.refresh(
          () => api.fetchSession(botId, sessionId),
          (row) => { if (deps.view.value === view && deps.visible.value) applySessionPair(view, row) },
        )
        deps.onPreferenceConflict?.(error)
      },
    )
  }

  // Welcome seed chain: this device's draft (source user) > the user's latest
  // native session pair (source session, carried on first send) > bot default
  // (source default, owned by initFromBotSettings).
  async function seedWelcome() {
    if (deps.usesExternalAgentComposer.value || deps.target.value.sessionId) return
    const view = deps.view.value
    if (view.pairSource.value === 'user') return
    const botId = deps.botId.value
    if (!botId) return
    const draft = readComposerPairDraft(botId)
    if (draft) {
      setPair(draft.model_id, draft.reasoning_effort || deps.botSettings.value?.reasoning_effort || 'medium', 'user', view)
      return
    }
    // The seed is a native pair. A fresh draft can still be native on its
    // first tick and have the bot's default external Agent staged onto it
    // while the request is in flight; that reset must win, otherwise a native
    // UUID lands in a direct composer.
    const identity = deps.runtimeIdentity.value
    await view.pairSync.refresh(
      () => api.fetchSeed(botId),
      (seed) => {
        if (deps.botId.value !== botId || deps.view.value !== view || deps.runtimeIdentity.value !== identity) return
        if (!seed.model_id || deps.target.value.sessionId || deps.view.value.pairSource.value === 'user') return
        setPair(seed.model_id, seed.reasoning_effort || deps.botSettings.value?.reasoning_effort || 'medium', 'session', view)
      },
    )
  }

  // The bot default is the lowest seed level: it only feeds views with no
  // explicit pair, marks them default-sourced so sends omit the pair, and
  // follows admin changes to the default live.
  function initFromBotSettings() {
    const settings = deps.botSettings.value
    if (deps.usesExternalAgentComposer.value || !settings) return
    const view = deps.view.value
    const current = view.pairSource.value
    if (current !== 'unset' && current !== 'default') return
    const modelId = deps.pinnedSubagentModelId.value || settings.chat_model_id || ''
    const effort = settings.reasoning_effort || 'medium'
    if (current === 'default') {
      setPair(modelId, effort, 'default', view)
      return
    }
    if (!view.pairModelId.value) view.pairModelId.value = modelId
    if (!view.pairEffort.value) view.pairEffort.value = effort
    if (view.pairModelId.value) view.pairSource.value = 'default'
  }

  watch([deps.botSettings, deps.usesExternalAgentComposer], () => initFromBotSettings(), { immediate: true })

  // The pinned subagent model routinely lands after bot settings seeded the
  // default. Adopt it then too — never over a remembered or user-picked pair.
  watch(deps.pinnedSubagentModelId, (pinned, previous) => {
    if (deps.usesExternalAgentComposer.value) return
    const view = deps.view.value
    if (view.pairSource.value !== 'unset' && view.pairSource.value !== 'default') return
    if (pinned) {
      view.pairModelId.value = pinned
      view.pairSource.value = 'default'
      return
    }
    // Repointed off a subagent: back to the bot's own default.
    if (previous) view.pairModelId.value = deps.botSettings.value?.chat_model_id ?? ''
  }, { immediate: true })

  function reload(view: ChatViewEntry) {
    // ACP pairs are the runtime's own state; the ACP config reconcile owns them.
    if (deps.usesACPRuntime.value) return
    const { botId, sessionId } = deps.target.value
    if (!sessionId) {
      if (!deps.usesExternalAgentComposer.value) {
        initFromBotSettings()
        void seedWelcome()
      }
      return
    }
    // Show the cached row immediately, then revalidate; the sync controller
    // drops reads overtaken by a pick, a send or a runtime reset.
    const cached = deps.activeSession.value
    if (view.pairSource.value === 'unset' && cached?.id === sessionId) applySessionPair(view, cached)
    if (view.pairSource.value === 'unset') initFromBotSettings()
    void view.pairSync.refresh(
      () => api.fetchSession(botId, sessionId),
      (row) => { if (deps.view.value === view && deps.visible.value) applySessionPair(view, row) },
    )
  }

  watch(
    [deps.view, deps.visible, () => deps.activeSession.value?.id, deps.runtimeIdentity],
    ([view, visible, , identity], previous) => {
      if (deps.draftPromotionPending()) return
      const previousView = previous?.[0]
      const previousIdentity = previous?.[3]
      if (previousView === view && previousIdentity !== undefined && identity !== previousIdentity) reset(view)
      if (!visible) return
      reload(view)
    },
    { immediate: true },
  )

  return {
    source,
    carried,
    setPair,
    setSource,
    selectModel,
    snapshot,
    restore,
    persist,
    beginSend,
    captureSend,
  }
}
