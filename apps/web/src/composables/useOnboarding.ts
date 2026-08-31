import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { putUsersMe } from '@memohai/sdk'
import { toast } from '@felinic/ui'
import { useUserStore } from '@/store/user'
import { ONBOARDING_KEYS } from '@/pages/onboarding/constants'
import { readOnboardingBotResult, resetOnboardingSession } from '@/pages/onboarding/session'
import { safeLocalGet, safeLocalRemove, safeLocalSet } from '@/utils/safe-storage'

export const LAST_STEP_INDEX = 4
export const STEP_COUNT = 5

const currentStep = ref(0)
const completing = ref(false)
const introTextVisible = ref(false)

export function resetOnboardingState() {
  currentStep.value = 0
  completing.value = false
  introTextVisible.value = false
}

export function useOnboarding() {
  const router = useRouter()
  const { t } = useI18n()

  const isFirstStep = computed(() => currentStep.value === 0)
  const isLastStep = computed(() => currentStep.value === LAST_STEP_INDEX)

  function nextStep() {
    if (currentStep.value < LAST_STEP_INDEX) {
      currentStep.value++
    }
  }

  function prevStep() {
    if (currentStep.value > 0) {
      currentStep.value--
    }
  }

  function goToStep(step: number) {
    if (step >= 0 && step <= LAST_STEP_INDEX) {
      currentStep.value = step
    }
  }

  function skipToEnd() {
    currentStep.value = LAST_STEP_INDEX
  }

  async function complete(minTransitionMs = 0): Promise<boolean> {
    completing.value = true
    const minWait = new Promise<void>((resolve) => setTimeout(resolve, minTransitionMs))
    try {
      await putUsersMe({
        body: { metadata: { onboarding_completed: true } },
        throwOnError: true,
      })
      const userStore = useUserStore()
      userStore.onboardingCompleted = true
    } catch {
      toast.error(t('onboarding.complete.saveFailed'))
      completing.value = false
      return false
    }
    await minWait
    const result = readOnboardingBotResult()
    const launchAgentId = result?.agent?.agentId ?? ''
    const forceOnboarding = safeLocalGet(ONBOARDING_KEYS.forceOnboarding)
    safeLocalRemove(ONBOARDING_KEYS.forceOnboarding)
    try {
      // Use the `bot` route directly (not the `/chat/...` redirect, which drops
      // the query) so the chat page can read `?agent=` on landing.
      const destination = result?.botId
        ? launchAgentId
          ? { name: 'bot', params: { botName: result.botId }, query: { agent: launchAgentId } }
          : { name: 'bot', params: { botName: result.botId } }
        : '/'
      const navigationFailure = await router.replace(destination)
      if (navigationFailure) throw navigationFailure
    } catch {
      if (forceOnboarding !== null) {
        safeLocalSet(ONBOARDING_KEYS.forceOnboarding, forceOnboarding)
      }
      toast.error(t('onboarding.complete.navigationFailed'))
      completing.value = false
      return false
    }
    resetOnboardingSession()
    completing.value = false
    return true
  }

  return {
    currentStep,
    completing,
    introTextVisible,
    isFirstStep,
    isLastStep,
    nextStep,
    prevStep,
    goToStep,
    skipToEnd,
    complete,
  }
}
