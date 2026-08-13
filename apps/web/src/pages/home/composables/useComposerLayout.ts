import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type ComputedRef } from 'vue'

// Composer layout: element refs, textarea focus, and the width clamp for the
// model trigger. Pure measurement — no chat state; everything it reads
// arrives as reactive deps.
//
// The composer is a fixed two-row card now (textarea on its own row, controls
// below), so there is no pill↔multiline reflow to detect and no height morph
// to drive — the card simply grows with the textarea's field-sizing. What
// remains genuinely dynamic is the model trigger's max-width: the trigger
// inherits the Button's shrink-0, so without a hard clamp a long model name
// would push past the box instead of ellipsising.

// The strip beneath the bottom box (pb-8). Shared with the dock so the mask
// can apply the same half-height rule to whatever box replaces the composer.
export const COMPOSER_MASK_BELOW_PX = 32

export interface ComposerLayoutDeps {
  // Whether the Continue-on destination pill is in the row; it reserves space.
  continueOnVisible: ComputedRef<boolean>
}

const MODEL_TRIGGER_MAX = 240 // max-w-60
const PLUS_SLOT = 40 // ＋ button (32) + gap
const CONTINUE_ON_SLOT = 168 // destination pill cap (max-w-40) + gap
const SEND_SLOT = 36 // mic/send circle size-8
const CLUSTER_GAP = 8 // gap-2 between controls-row children

export function useComposerLayout(deps: ComposerLayoutDeps) {
  const { continueOnVisible } = deps

  const textareaEl = ref<HTMLTextAreaElement | null>(null)
  const composerEl = ref<HTMLElement | null>(null)

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

  // Sized to whatever the controls row can spare after the ＋, the Continue-on
  // pill, and the mic/send circle. Only bites when space is tight; otherwise
  // it rests at the 240px cap and hugs a short name.
  const modelTriggerMaxWidth = computed(() => {
    const inner = composerInnerWidth.value
    if (inner <= 1) return MODEL_TRIGGER_MAX
    let reserved = PLUS_SLOT + CLUSTER_GAP + SEND_SLOT
    if (continueOnVisible.value) {
      reserved += CONTINUE_ON_SLOT
    }
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

  // The Continue-on pill appears/disappears without a box resize, so re-run
  // the fit pass when its visibility changes.
  watch(continueOnVisible, () => {
    void nextTick(recomputeComposerFit)
  })

  onBeforeUnmount(() => {
    composerSizeObserver?.disconnect()
    composerSizeObserver = null
  })

  return {
    textareaEl,
    composerEl,
    focusTextarea,
    modelTriggerMaxWidth,
  }
}
