<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useEventListener, usePreferredReducedMotion } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { Button, CalloutBanner, TextButton } from '@felinic/ui'
import { useChatExamplesUi } from '@/components/chat-examples/use-chat-examples-ui'
import { useStarterPromptsQuery } from '@/composables/api/useStarterPrompts'
import { parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import { useComposerPrefill } from '@/composables/useComposerPrefill'
import { COMPOSER_PREVIEW_LANDING_MS, type ComposerPreview } from '../composables/useComposerPreview'
import StarterPromptList, { type StarterPromptRow } from './starter-prompt-list.vue'

/**
 * The bot's own starter prompts for this user, under the welcome composer.
 * They are always generated, from the bot's name alone when it has nothing
 * else yet; while a set is on its way (the first one, or a refresh) the rows
 * show a skeleton, and failures keep a retryable notice with its request ID. With no title
 * model configured the section stays folded away. "Refresh" asks for a different set. Hiding the section is
 * remembered on this device; while hidden, the composer's context row shows
 * the entry that brings it back. Hidden by the welcome column's height tiers
 * (style.css) before anything else yields.
 *
 * Hovering or focusing a row previews its prompt in the composer through the
 * `preview` model, and while a preview shows, Enter picks that row (the "↵"
 * hint on the row says so) wherever focus is. Picking the row that is being
 * previewed first "lands" the preview (it inks into the real text) and then
 * prefills, so the text never blinks out and back in.
 */
const props = defineProps<{
  botId: string
  /**
   * The composer holds no text of the user's own (empty, or an unedited
   * prefill), so a row may preview into it and Enter may pick a row.
   */
  composerReplaceable: boolean
}>()

const preview = defineModel<ComposerPreview | null>('preview', { default: null })

const { t } = useI18n()
const { welcomeDismissed } = useChatExamplesUi()
const { prefillComposer } = useComposerPrefill()
const reducedMotion = usePreferredReducedMotion()

const { data: starterSet, status: starterStatus, refresh: refreshStarterSet, refreshing, error: starterError } = useStarterPromptsQuery(() => props.botId)

/** The generated rows, or null while a set is on its way (skeleton). */
const rows = computed<StarterPromptRow[] | null>(() => {
  if (refreshing.value || (!starterSet.value && starterStatus.value === 'pending')) return null
  return (starterSet.value?.items ?? []).map((item, index) => ({
    key: `starter:${index}`,
    title: item.title,
    prompt: item.prompt,
    previewable: true,
  }))
})

/** Whether the composer is currently showing the preview. */
const previewShown = computed(() => props.composerReplaceable && preview.value !== null)

const visible = computed(() => !welcomeDismissed.value && (rows.value === null || rows.value.length > 0 || !!starterError.value))
const errorDismissed = ref(false)
/** Dismissal belongs to one failure on one bot, never to future attempts. */
watch([() => props.botId, starterError], () => { errorDismissed.value = false })
const errorMessage = computed(() => starterError.value
  ? resolveApiErrorMessage(starterError.value, t('chatExamples.refreshFailed'))
  : '')
const requestId = computed(() => parseMemohError(starterError.value)?.requestId)

async function refresh() {
  errorDismissed.value = false
  preview.value = null
  await refreshStarterSet()
}

function setPreview(prompt: string | null) {
  if (preview.value?.landing) return
  preview.value = prompt ? { text: prompt, landing: false } : null
}

/**
 * Enter picks the previewed row. Captured on the window so it runs before the
 * composer (which would send) and before a focused row's own activation.
 */
useEventListener(window, 'keydown', (event: KeyboardEvent) => {
  if (event.key !== 'Enter' || event.isComposing || event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return
  const previewed = preview.value
  if (!previewShown.value || !previewed || previewed.landing) return
  const row = rows.value?.find(candidate => candidate.prompt === previewed.text)
  if (!row) return
  event.preventDefault()
  event.stopPropagation()
  void select(row)
}, { capture: true })

async function select(row: StarterPromptRow) {
  if (previewShown.value && preview.value?.text === row.prompt && reducedMotion.value !== 'reduce') {
    preview.value = { text: row.prompt, landing: true }
    await new Promise(resolve => setTimeout(resolve, COMPOSER_PREVIEW_LANDING_MS))
  }
  try {
    await prefillComposer(row.prompt, { botId: props.botId })
  }
  finally {
    preview.value = null
  }
}
</script>

<template>
  <section
    v-if="visible"
    data-welcome-suggestions
    class="@container pointer-events-auto mx-auto w-full max-w-[44rem] px-4 pb-8 sm:px-6 lg:px-10 md:-translate-x-0.5"
    :aria-label="$t('chatExamples.tryThese')"
  >
    <!-- pb-8 keeps the welcome column's 2rem rhythm below the last row: in a
         short pane the anchor spacer shrinks first, so the rows stop a gap
         short of the pane edge instead of resting on it. -->
    <!-- Same box model as the rows below (1px edge + px-4), so the label
         starts on the rows' text column and the close glyph ends on their
         trailing column. The section shares the composer's 2px optical nudge.
         The label follows the section-title standard (text-label, medium). -->
    <div class="mb-1 flex items-center justify-between gap-2 border border-transparent px-4">
      <h2 class="text-label font-medium text-muted-foreground">
        {{ $t('chatExamples.tryThese') }}
      </h2>
      <div class="flex items-center gap-1">
        <!-- Same type as the composer's session controls one row up
             (text-label, regular weight), not TextButton's 14px medium. -->
        <TextButton
          type="button"
          class="text-label font-normal"
          :disabled="rows === null"
          @click="refresh"
        >
          {{ $t('chatExamples.refresh') }}
        </TextButton>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          shape="circle"
          class="-mr-2 text-muted-foreground"
          :aria-label="$t('chatExamples.dismiss')"
          @click="welcomeDismissed = true"
        >
          <X class="size-4" />
        </Button>
      </div>
    </div>
    <CalloutBanner
      v-if="starterError && !refreshing && !errorDismissed"
      tone="destructive"
      size="sm"
      :description="errorMessage"
      :dismiss-label="$t('chatExamples.dismissError')"
      @dismiss="errorDismissed = true"
    >
      <template
        v-if="requestId"
        #details
      >
        {{ $t('common.requestId') }}: {{ requestId }}
      </template>
    </CalloutBanner>
    <StarterPromptList
      :rows="rows"
      :enter-hint="composerReplaceable"
      @select="select"
      @preview="setPreview"
    />
  </section>
</template>
