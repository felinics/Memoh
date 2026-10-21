import { computed, nextTick, onBeforeUnmount, onMounted, ref, useTemplateRef } from 'vue'

// Composer layout: element refs, textarea focus, and the width clamp for the
// model trigger. Pure measurement of the input surface, independent of chat state.
//
// The composer is a fixed two-row card now (textarea on its own row, controls
// below), so there is no pill↔multiline reflow to detect and no height morph
// to drive — the card simply grows with the textarea's field-sizing. What
// remains genuinely dynamic is the model trigger's max-width: the clamp
// reserves space for the sibling controls so the row never overflows, and
// floors at 72px — below that the trigger's own `shrink` lets it truncate
// rather than push past the box.

// The context row owns all space down to the bottom edge; no extra strip follows it.
export const COMPOSER_MASK_BELOW_PX = 0

const MODEL_TRIGGER_MAX = 240 // max-w-60
// Reserve the mobile touch-target widths for plus and send. On desktop this
// conservatively clamps the label before either control could be crowded.
const PLUS_SLOT = 48 // ＋ circle at its largest (max-md size-11 = 44) + gap-1
const SEND_SLOT = 44 // mic/send circle at its largest (max-md size-11)
const CLUSTER_GAP = 8 // gap-2 between controls-row children

export function useComposerLayout() {
  const textareaEl = ref<HTMLTextAreaElement | null>(null)
  // Read straight from the caller's `ref="composerEl"`: only this composable
  // measures the card, so the component keeps no binding of its own.
  const composerEl = useTemplateRef<HTMLElement>('composerEl')

  const composerInnerWidth = ref(0)

  function focusTextarea() {
    textareaEl.value?.focus()
  }

  function recomputeComposerFit() {
    const el = composerEl.value
    if (!el) return
    const cs = getComputedStyle(el)
    const padX = Number.parseFloat(cs.paddingLeft) + Number.parseFloat(cs.paddingRight)
    const inner = el.clientWidth - padX
    if (inner <= 1) return
    composerInnerWidth.value = inner
  }

  // Only the plus and send controls share the model row. Session controls
  // are outside the input and must not reduce the model label width.
  const modelTriggerMaxWidth = computed(() => {
    const inner = composerInnerWidth.value
    if (inner <= 1) return MODEL_TRIGGER_MAX
    const reserved = PLUS_SLOT + CLUSTER_GAP + SEND_SLOT
    return Math.max(72, Math.min(MODEL_TRIGGER_MAX, inner - reserved))
  })

  let composerSizeObserver: ResizeObserver | null = null
  onMounted(() => {
    void nextTick(recomputeComposerFit)
    const el = composerEl.value
    if (el && typeof ResizeObserver !== 'undefined') {
      composerSizeObserver = new ResizeObserver(recomputeComposerFit)
      composerSizeObserver.observe(el)
    }
  })

  onBeforeUnmount(() => {
    composerSizeObserver?.disconnect()
    composerSizeObserver = null
  })

  return {
    textareaEl,
    focusTextarea,
    modelTriggerMaxWidth,
  }
}
