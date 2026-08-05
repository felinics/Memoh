import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { createACPDefaults } from './acp-defaults'

const sdk = vi.hoisted(() => ({
  getBotsByBotIdSettings: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdSettings: sdk.getBotsByBotIdSettings,
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

describe('ACP defaults', () => {
  it('does not cache settings that resolve after the auth scope changes', async () => {
    const response = deferred<{ data: Record<string, unknown> }>()
    sdk.getBotsByBotIdSettings.mockReturnValueOnce(response.promise)
    let generation = 0
    const rememberDefault = vi.fn()
    const defaults = createACPDefaults({
      currentBotId: ref('bot-1'),
      sessionId: ref(null),
      explicitSessionSelection: ref(false),
      userScopeGeneration: () => generation,
      currentSelectRequest: () => 0,
      rememberDefault,
      cachedDefault: () => ({ loaded: false, input: null }),
      pendingMatches: () => false,
      stageDefault: vi.fn(),
    })

    const loading = defaults.defaultRuntimeIsACP('bot-1')
    generation += 1
    response.resolve({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
      },
    })

    await expect(loading).resolves.toBe(false)
    expect(rememberDefault).not.toHaveBeenCalled()
  })
})
