import { computed, ref, type Ref } from 'vue'
import type { ChatViewTarget } from './types'
import type { StartupSendFailure } from './send'
import { nextId } from '../chat-list.normalize'

export function createStartupSendFailures(deps: {
  currentBotId: Ref<string | null>
  focusedViewId: Ref<string>
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
}) {
  const failures = ref<Record<string, StartupSendFailure>>({})

  function key(botId: string, sessionId: string, composerScope = '') {
    const bid = botId.trim()
    const scope = composerScope.trim()
    if (scope) return `composer:${bid}:${scope}`
    return `session:${bid}:${sessionId.trim()}`
  }

  function failureFor(
    target: ChatViewTarget,
    composerScope = '',
  ): StartupSendFailure | null {
    const resolved = deps.normalizeTarget(target)
    const scoped = failures.value[
      key(resolved.botId, resolved.sessionId ?? '', composerScope)
    ]
    if (scoped) return scoped
    if (!resolved.sessionId) return null
    return failures.value[key(resolved.botId, resolved.sessionId)] ?? null
  }

  const activeFailure = computed(() => failureFor(
    deps.normalizeTarget(),
    deps.focusedViewId.value === 'chat'
      ? 'chat'
      : `${(deps.currentBotId.value ?? '').trim()}:${deps.focusedViewId.value}`,
  ))

  function remember(failure: Omit<StartupSendFailure, 'id'>) {
    const stored: StartupSendFailure = {
      ...failure,
      id: nextId(),
      restoreAttachments: failure.restoreAttachments
        ? [...failure.restoreAttachments]
        : undefined,
      restoreRequestedSkills: failure.restoreRequestedSkills
        ? failure.restoreRequestedSkills.map(skill => ({ ...skill }))
        : undefined,
    }
    failures.value = {
      ...failures.value,
      [key(failure.botId, failure.sessionId, failure.composerScope)]: stored,
    }
  }

  function clear(id?: string) {
    if (!id) {
      failures.value = {}
      return
    }
    const next = { ...failures.value }
    for (const [failureKey, failure] of Object.entries(next)) {
      if (failure.id === id) delete next[failureKey]
    }
    failures.value = next
  }

  return {
    activeFailure,
    failureFor,
    remember,
    clear,
    reset: () => { failures.value = {} },
  }
}
