import { useUserStore } from '@/store/user'
import { ONBOARDING_KEYS } from '@/pages/onboarding/constants'
import { readOnboardingBotResult } from '@/pages/onboarding/session'
import { readCreatedBotSession } from '@/pages/bots/created-bot-session'

function shouldForceOnboarding(): boolean {
  return import.meta.env.DEV && localStorage.getItem(ONBOARDING_KEYS.forceOnboarding)?.trim() === '1'
}

export async function ensureOnboarding(): Promise<boolean> {
  if (shouldForceOnboarding()) return false
  const store = useUserStore()
  if (store.onboardingCompleted) return true
  const fetched = await store.fetchMe()
  if (!fetched) return true
  return store.onboardingCompleted
}

// Onboarding is marked complete as soon as its Bot exists, while this tab may
// still be installing it, applying its settings, or showing the final step.
// Such a tab keeps its place in the wizard, refreshes included.
export function hasOnboardingInProgress(): boolean {
  return !!readCreatedBotSession(true) || !!readOnboardingBotResult()
}
