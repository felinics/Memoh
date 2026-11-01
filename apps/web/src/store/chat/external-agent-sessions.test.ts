import { expect, it } from 'vitest'
import { externalAgentSessionMetadata } from './external-agent-sessions'

it('leaves the ACP agent to the Agent instance, naming it only when none is chosen', () => {
  expect(externalAgentSessionMetadata({ runtime: 'acp', botAgentId: 'agent-1', agentId: 'gemini', projectPath: '/data/app' }))
    .toEqual({ project_path: '/data/app' })
  expect(externalAgentSessionMetadata({ runtime: 'acp', agentId: 'gemini' }))
    .toEqual({ project_path: '/data', acp_agent_id: 'gemini' })
})
