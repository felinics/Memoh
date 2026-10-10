<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle } from 'lucide-vue-next'
import ChatExampleCard from '@/components/chat-examples/chat-example-card.vue'
import { useChatExampleEntries } from '@/components/chat-examples/use-chat-example-action'
import { useOnboarding } from '@/composables/useOnboarding'
import { localizeText } from '@/lib/chat-examples/select'
import type { ChatExample } from '@/lib/chat-examples/types'
import { useComposerPrefillStore } from '@/store/composer-prefill'
import { nextFrame } from '../useStepTransition'
import StepExitShell from '../components/step-exit-shell.vue'
import HintBox from '../components/hint-box.vue'
import { safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'
import { ONBOARDING_KEYS } from '../constants'
import { readOnboardingBotResult } from '../session'
import { finishOnboardingWithPrompt } from '../finish-with-prompt'

const { t, locale } = useI18n()
const { complete, completing } = useOnboarding()
const prefillStore = useComposerPrefillStore()

const visible = ref(false)
const exiting = ref(false)
const botResult = readOnboardingBotResult()
const hasConfiguredAI = botResult?.modelConfigured === true || !!botResult?.agent

// Onboarding examples need no capability, so no bot probe is needed here.
const starters = useChatExampleEntries('', { surface: 'onboarding', limit: 3 })

onMounted(() => {
  nextFrame(() => {
    visible.value = true
  })
})

/** Finish onboarding; with an example, the first chat opens with its prompt in the composer. */
async function handleComplete(example?: ChatExample) {
  if (completing.value) return
  exiting.value = true
  safeSessionSet(ONBOARDING_KEYS.entryAnimation, '1')
  const ok = await finishOnboardingWithPrompt({
    complete: () => complete(175),
    prefill: prefillStore,
    botId: botResult?.botId,
    prompt: example ? localizeText(example.prompt, locale.value) : undefined,
  })
  if (!ok) {
    exiting.value = false
    safeSessionRemove(ONBOARDING_KEYS.entryAnimation)
  }
}
</script>

<template>
  <StepExitShell
    class="text-center"
    :exiting="exiting"
  >
    <h2
      class="text-5xl font-semibold tracking-tight mb-4 transition-all duration-[350ms] ease-out"
      :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
    >
      {{ t('onboarding.complete.title') }}
    </h2>
    <p
      class="text-base text-muted-foreground mb-12 transition-all duration-[350ms] ease-out delay-[80ms]"
      :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
    >
      {{ t('onboarding.complete.subtitle') }}
    </p>

    <div
      v-if="starters?.length"
      class="mx-auto mb-12 max-w-md space-y-2 text-left transition-all duration-[350ms] ease-out delay-[160ms]"
      :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
    >
      <p class="px-1 text-caption font-medium text-muted-foreground">
        {{ t('onboarding.complete.examplesHint') }}
      </p>
      <ChatExampleCard
        v-for="entry in starters"
        :key="entry.example.id"
        :example="entry.example"
        :missing="entry.missing"
        @select="handleComplete(entry.example)"
      />
    </div>

    <div
      v-if="!hasConfiguredAI"
      class="mb-8 flex justify-center transition-all duration-[350ms] ease-out delay-[200ms]"
      :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
    >
      <HintBox
        tone="warning"
        class="inline-block text-left"
      >
        <template #icon>
          <AlertTriangle class="size-4 shrink-0 text-warning-foreground mt-0.5" />
        </template>
        <p class="text-muted-foreground">
          {{ t('onboarding.complete.noModelWarning') }}
        </p>
      </HintBox>
    </div>

    <div
      class="transition-all duration-[350ms] ease-out delay-[240ms]"
      :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
    >
      <button
        class="inline-flex h-[2.625rem] w-[240px] items-center justify-center rounded-lg bg-primary px-5 font-normal text-primary-foreground shadow-none transition-colors hover:bg-primary/90 disabled:opacity-50 disabled:pointer-events-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
        :disabled="completing"
        @click="handleComplete()"
      >
        {{ t('onboarding.complete.action') }}
      </button>
    </div>
  </StepExitShell>
</template>
