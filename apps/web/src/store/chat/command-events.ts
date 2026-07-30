import { computed, ref, type Ref } from 'vue'
import type { CommandEventResponse } from '@/composables/api/useChat'
import { createInvocationId } from '../chat-list.normalize'

// Slash-command event registry — remembers the latest command_result /
// command_error per scope so the composer that issued the command (and only
// it) can render the outcome. Scoping is three-keyed:
//   session:<bot>:<session>  — command ran inside a real session
//   composer:<bot>:<scope>   — command ran from a draft composer (no session)
//
// Extracted from the chat store as a plain composable factory; the store
// spreads the public surface into its return so consumers are unchanged.

export type StoreCommandEvent = CommandEventResponse & {
  bot_id?: string
}

export interface CommandEventScope {
  botId?: string
  sessionId?: string
  composerScope?: string
}

export interface CommandEventRegistryDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
}

export function createCommandEventRegistry({ currentBotId, sessionId }: CommandEventRegistryDeps) {
  const commandEvents = ref<Record<string, StoreCommandEvent>>({})

  function currentCommandScope(composerScope = 'chat'): CommandEventScope {
    return {
      botId: currentBotId.value ?? undefined,
      sessionId: sessionId.value ?? undefined,
      composerScope,
    }
  }

  function commandEventKey(scope: CommandEventScope) {
    const bid = (scope.botId ?? '').trim()
    const sid = (scope.sessionId ?? '').trim()
    if (bid && sid) return `session:${bid}:${sid}`
    return `composer:${bid}:${(scope.composerScope ?? 'chat').trim() || 'chat'}`
  }

  function commandEventForScope(scope: CommandEventScope = currentCommandScope()): StoreCommandEvent | null {
    return commandEvents.value[commandEventKey(scope)] ?? null
  }

  const commandEvent = computed(() => commandEventForScope())

  function rememberCommandEvent(event: CommandEventResponse | null, scope: CommandEventScope = {}) {
    if (!event) {
      clearCommandEvent(scope)
      return
    }
    const scoped: StoreCommandEvent = {
      ...event,
      composer_scope: event.composer_scope || scope.composerScope || 'chat',
    }
    const bid = (scope.botId ?? '').trim()
    if (bid) scoped.bot_id = bid
    if (!scoped.session_id) {
      const sid = (scope.sessionId ?? '').trim()
      if (sid) scoped.session_id = sid
    }
    const key = commandEventKey({
      botId: scoped.bot_id || scope.botId,
      sessionId: scoped.session_id,
      composerScope: scoped.composer_scope,
    })
    commandEvents.value = {
      ...commandEvents.value,
      [key]: scoped,
    }
  }

  function showCommandError(code: string, message: string, scope: CommandEventScope = currentCommandScope()) {
    rememberCommandEvent({
      type: 'command_error',
      invocation_id: createInvocationId(),
      composer_scope: scope.composerScope || 'chat',
      terminal: true,
      error: { code, message },
    }, scope)
  }

  function clearCommandEvent(scope: CommandEventScope = currentCommandScope()) {
    const key = commandEventKey(scope)
    if (!commandEvents.value[key]) return
    const next = { ...commandEvents.value }
    delete next[key]
    commandEvents.value = next
  }

  // A deferred session died before it materialized: its session-scoped event
  // must survive on the composer key so the draft composer still shows the
  // failure. Returns the composer scope the event carried (used to route the
  // deleted-session signal back to the right composer pane).
  function rescopeSessionCommandEventToComposer(botId: string, targetSessionId: string): string {
    const sessionCommandKey = commandEventKey({ botId, sessionId: targetSessionId })
    const sessionCommandEvent = commandEvents.value[sessionCommandKey]
    if (!sessionCommandEvent) return ''
    const scoped = { ...sessionCommandEvent }
    delete scoped.session_id
    const next = { ...commandEvents.value }
    delete next[sessionCommandKey]
    next[commandEventKey({ botId, composerScope: scoped.composer_scope || 'chat' })] = scoped
    commandEvents.value = next
    return scoped.composer_scope?.trim() || ''
  }

  function resetCommandEvents() {
    commandEvents.value = {}
  }

  return {
    commandEvents,
    commandEvent,
    currentCommandScope,
    commandEventKey,
    commandEventForScope,
    rememberCommandEvent,
    showCommandError,
    clearCommandEvent,
    rescopeSessionCommandEventToComposer,
    resetCommandEvents,
  }
}
