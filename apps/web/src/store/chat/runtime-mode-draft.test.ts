import { ref } from 'vue'
import { expect, it, vi } from 'vitest'
import { createACPRuntimeRegistry } from './acp-runtime-registry'
import { createExternalAgentStaging } from './external-agent-staging'
import { createExternalAgentOrchestration } from './external-agent-orchestration'
import { externalAgentSessionMetadata } from './external-agent-sessions'
import type { ChatViewTarget } from './types'

function setup() {
  const currentBotId = ref<string | null>('bot-1')
  const sessionId = ref<string | null>(null)
  const runtimeRegistry = createACPRuntimeRegistry({ currentBotId, sessionId })
  const staging = createExternalAgentStaging({
    currentBotId, sessionId, runtimeRegistry,
    draftIntent: ref(true), explicitSessionSelection: ref(true),
    bumpSelectSessionRequest: vi.fn(), clearTranscriptForDraft: vi.fn(),
  })
  const first: ChatViewTarget = { botId: 'bot-1', sessionId: null, viewId: 'first' }
  const second = { ...first, viewId: 'second' }
  const drafts = createExternalAgentOrchestration({
    staging, runtimeRegistry,
    normalizeTarget: target => ({ ...first, ...target }),
    invalidateDraftCommand: vi.fn(), forgetDraftCommand: vi.fn(), resetWorkspaceTargetSelection: vi.fn(),
  })
  const input = { agentId: 'codex', botAgentId: 'agent-1', runtime: 'codex' as const }
  drafts.stageNewExternalAgentSession(input, first)
  return { drafts, first, second, input }
}

it('计划和权限独立保存，退出计划显式恢复 default，草稿互不串用', () => {
  const { drafts, first, second, input } = setup()
  drafts.setPendingRuntimeMode('yolo', first)
  drafts.setPendingRuntimeMode('plan', first, 'plan')
  drafts.stageDefaultExternalAgentSession(input, first)
  expect(externalAgentSessionMetadata(drafts.pendingExternalAgentStateFor(first)!.input)).toEqual({ permission_mode: 'yolo', collaboration_mode: 'plan' })
  drafts.stageNewExternalAgentSession(input, second)
  expect(externalAgentSessionMetadata(drafts.pendingExternalAgentStateFor(second)!.input)).toEqual({})
  drafts.setPendingRuntimeMode('default', first, 'plan')
  expect(externalAgentSessionMetadata(drafts.pendingExternalAgentStateFor(first)!.input)).toEqual({ permission_mode: 'yolo', collaboration_mode: 'default' })
  drafts.stageNewExternalAgentSession({ ...input, botAgentId: 'agent-2' }, first)
  expect(externalAgentSessionMetadata(drafts.pendingExternalAgentStateFor(first)!.input)).toEqual({})
})
