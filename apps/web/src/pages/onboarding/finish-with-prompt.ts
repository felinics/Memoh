export interface FinishWithPromptDeps {
  /** Finishes onboarding and navigates to the new bot's chat; false when saving failed. */
  complete: () => Promise<boolean>
  prefill: {
    request: (botId: string, text: string) => void
    clear: () => void
  }
  botId?: string
  prompt?: string
}

/**
 * Finish onboarding, optionally landing in the first chat with `prompt`
 * already in the composer. The prompt is queued before navigating so the
 * chat page picks it up on arrival, and withdrawn if finishing fails so it
 * cannot surface in some later chat.
 */
export async function finishOnboardingWithPrompt(deps: FinishWithPromptDeps): Promise<boolean> {
  const { botId, prompt } = deps
  const queued = !!botId && !!prompt
  if (queued) deps.prefill.request(botId, prompt)
  const ok = await deps.complete()
  if (!ok && queued) deps.prefill.clear()
  return ok
}
