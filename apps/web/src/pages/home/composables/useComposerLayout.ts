import { computed, nextTick, onBeforeUnmount, onDeactivated, onMounted, ref, watch, type ComputedRef, type Ref } from 'vue'
import { ATTACHMENT_ANIM_MS } from './useComposerAttachments'

// Composer layout engine: pill↔multiline reflow decisions (text wrap + width
// budget), the border-radius morph clock, the JS height morph with
// bottom-pinned controls, and the width clamps for the model trigger. Pure
// measurement + animation — no chat state; everything it reads arrives as
// reactive deps.

// The strip beneath the bottom box (pb-8). Shared with chat-pane so the mask
// can apply the same half-height rule to whatever box replaces the composer.
export const COMPOSER_MASK_BELOW_PX = 32

export interface ComposerLayoutDeps {
  inputText: Ref<string>
  isActive: ComputedRef<boolean>
  showAttachmentGrid: ComputedRef<boolean>
  // The shell's mobile breakpoint (useIsMobile). When true the composer is
  // ALWAYS expanded — the single-row pill is desktop-only affordance, too
  // cramped a touch target on a phone.
  mobileMultiline: Ref<boolean>
  // Right-cluster label source; a change re-runs the width fit.
  modelTriggerLabel: ComputedRef<string>
}

export function useComposerLayout(deps: ComposerLayoutDeps) {
  const { inputText, isActive, showAttachmentGrid, mobileMultiline, modelTriggerLabel } = deps

  const textareaEl = ref<HTMLTextAreaElement | null>(null)
  const composerEl = ref<HTMLElement | null>(null)
  const modelLabelEl = ref<HTMLElement | null>(null)
  // The composer lifts to its multiline layout (textarea on its own row, controls
  // below) for three independent reasons: the shell is in mobile mode
  // (mobileMultiline — the pill is a desktop affordance and never the default on
  // a phone), the typed text wraps or holds a newline (textMultiline), or the pane
  // is too narrow to seat the input + model capsule + send on one pill row
  // (narrowMultiline). Any trigger flips isMultiline, so a cramped pane reflows
  // into multiline instead of letting the pill explode.
  const textMultiline = ref(false)
  const narrowMultiline = ref(false)
  const isMultiline = computed(() => mobileMultiline.value || textMultiline.value || narrowMultiline.value)
  const compactContentWidth = ref(0)
  const composerInnerWidth = ref(0)
  const composerBoxHeight = ref(0)

  // Border-radius morph, kept on the SAME clock as whatever box change drives it.
  // An attachment open/close keys off showAttachmentGrid — which flips at the START
  // of the collapse, before the card is spliced out — and borrows the grid reveal's
  // duration + curve, so the corner rounds in lockstep with the height instead of
  // lagging a beat behind it (the height was finishing first, then the corner moved
  // only once the card was finally removed). A pill↔multiline text change keeps the
  // form ease, matched to the JS height morph. Pre-flush watchers set the timing
  // before the radius class flips, so the corner uses it.
  const RADIUS_EASE_FORM = 'cubic-bezier(0.33, 1, 0.68, 1)'
  const composerRadiusMs = ref(220)
  const composerRadiusEase = ref(RADIUS_EASE_FORM)
  watch(showAttachmentGrid, () => {
    composerRadiusMs.value = ATTACHMENT_ANIM_MS
    composerRadiusEase.value = 'cubic-bezier(0.25, 0.1, 0.25, 1)'
  })
  watch(isMultiline, () => {
    if (!showAttachmentGrid.value) {
      composerRadiusMs.value = 220
      composerRadiusEase.value = RADIUS_EASE_FORM
    }
  })

  function focusTextarea() {
    textareaEl.value?.focus()
  }

  function measureWraps(text: string, width: number): boolean {
    const el = textareaEl.value
    if (!el || width <= 1) return false
    const cs = getComputedStyle(el)
    const mirror = document.createElement('div')
    const s = mirror.style
    s.position = 'fixed'
    s.left = '-9999px'
    s.top = '0'
    s.visibility = 'hidden'
    s.pointerEvents = 'none'
    s.whiteSpace = 'pre-wrap'
    s.overflowWrap = 'anywhere'
    s.wordBreak = 'break-word'
    s.boxSizing = 'content-box'
    s.width = `${width}px`
    s.fontFamily = cs.fontFamily
    s.fontSize = cs.fontSize
    s.fontWeight = cs.fontWeight
    s.fontStyle = cs.fontStyle
    s.letterSpacing = cs.letterSpacing
    s.lineHeight = cs.lineHeight
    mirror.textContent = text.length ? text : 'x'
    document.body.appendChild(mirror)
    const h = mirror.getBoundingClientRect().height
    mirror.remove()
    const lh = Number.parseFloat(cs.lineHeight) || 20
    return h > lh * 1.5
  }

  function syncMultiline() {
    const text = inputText.value
    if (text.includes('\n')) {
      textMultiline.value = true
      return
    }
    const el = textareaEl.value
    if (el && !isMultiline.value) {
      const cs = getComputedStyle(el)
      const padX = Number.parseFloat(cs.paddingLeft) + Number.parseFloat(cs.paddingRight)
      const w = el.clientWidth - padX
      if (w > 1) compactContentWidth.value = w
    }
    textMultiline.value = measureWraps(text, compactContentWidth.value)
  }

  // Pixel budget for the compact (pill) row. The right cluster's *natural* width is
  // derived from intrinsic measurements (the model label's scrollWidth, which a
  // `truncate` span still reports in full) so the verdict never depends on the
  // current layout — switching to multiline can't change the inputs and oscillate.
  // When the inline textarea would be squeezed under MIN_INLINE_TEXTAREA, the pill
  // can't host input + capsule + send on one line, so we reflow to multiline.
  const MIN_INLINE_TEXTAREA = 120
  const MODEL_TRIGGER_MAX = 240 // max-w-60
  const PLUS_SLOT = 40 // size-9 (36) + gap-1 (4)
  const SEND_SLOT = 36 // send / ring size-9
  const MODEL_CHROME = 46 // px-3 ×2 + gap-1 + chevron + a little slack
  const CLUSTER_GAP = 8 // gap-2 between cluster children
  const ROW_GAPS = 8 // gap-1 on each flank of the textarea

  function rightClusterNaturalWidth(): number {
    const modelLabel = modelLabelEl.value?.scrollWidth ?? 0
    const modelWidth = modelLabel > 0
      ? Math.min(MODEL_TRIGGER_MAX, modelLabel + MODEL_CHROME)
      : MODEL_TRIGGER_MAX
    return modelWidth + CLUSTER_GAP + SEND_SLOT
  }

  function recomputeComposerFit() {
    const el = composerEl.value
    if (!el) return
    const cs = getComputedStyle(el)
    const padX = Number.parseFloat(cs.paddingLeft) + Number.parseFloat(cs.paddingRight)
    const inner = el.clientWidth - padX
    if (inner <= 1) return
    composerInnerWidth.value = inner
    const room = inner - PLUS_SLOT - ROW_GAPS - rightClusterNaturalWidth()
    narrowMultiline.value = room < MIN_INLINE_TEXTAREA
  }

  // The model trigger inherits the Button's `shrink-0`, so it won't yield in a
  // flex row — a long name would push past the box instead of truncating. A hard
  // max-width clamps it regardless of flex-shrink (the min-w-0 label then ellipses
  // within), sized to whatever the controls row can spare after the ＋ and send.
  // It only bites when space is tight; otherwise it rests at the 240px cap and
  // the button still hugs a short name.
  const modelTriggerMaxWidth = computed(() => {
    const inner = composerInnerWidth.value
    if (inner <= 1) return MODEL_TRIGGER_MAX
    const reserved = PLUS_SLOT + ROW_GAPS + CLUSTER_GAP + SEND_SLOT
    return Math.max(72, Math.min(MODEL_TRIGGER_MAX, inner - reserved))
  })

  // NOTE: the bottom backdrop mask used to be derived here
  // (composerMaskHeight). It moved to composer-dock.vue, which owns dock-wide
  // geometry now; this composable only keeps the composer's own morph
  // measurements (composerBoxHeight stays internal to the morph).

  let composerResizeObserver: ResizeObserver | null = null
  onMounted(() => {
    void nextTick(() => {
      syncMultiline()
      recomputeComposerFit()
    })
    if (typeof ResizeObserver !== 'undefined' && textareaEl.value) {
      composerResizeObserver = new ResizeObserver(() => syncMultiline())
      composerResizeObserver.observe(textareaEl.value)
    }
  })

  // A different model name changes the right cluster's natural width, so re-run
  // the fit check when the label changes.
  watch(modelTriggerLabel, () => {
    void nextTick(recomputeComposerFit)
  })
  onBeforeUnmount(() => {
    composerResizeObserver?.disconnect()
    composerResizeObserver = null
  })
  watch(inputText, () => {
    void nextTick(syncMultiline)
  })

  // Smooth height morph for the compact↔multiline change. The composer is
  // bottom-anchored, so animating its height makes the top edge rise and the text
  // appears to slide up. Pure CSS can't transition between two content-driven
  // (auto) heights, so we measure the natural height and let the browser's
  // animation engine fill the gap — no permanent inline height, no fight with the
  // textarea's field-sizing. During the morph the box is clipped and its content is
  // bottom-pinned: the left (＋) and right (model) controls stay welded to the
  // bottom edge — which never moves — so they don't twitch, while the textarea
  // grows above them and the text is revealed from the top.
  let composerHeight = 0
  let composerHeightAnim: Animation | null = null
  let composerHeightReady = false
  // Last-seen layout mode, so we can tell a pill↔multiline form change from a
  // grow/shrink that happens entirely within multiline.
  let composerMultiline = false
  // A session/draft switch replaces the text wholesale — snap to the new size
  // once instead of animating between two unrelated drafts.
  let composerSnapNext = false
  // Tracks layout-driven height changes (e.g. window/pane resize re-wrapping a
  // multiline draft) that don't go through inputText/isMultiline, so the next
  // morph starts from the real current height instead of a stale baseline.
  let composerSizeObserver: ResizeObserver | null = null

  // External trigger for the snap (a draft-key swap replaces the text wholesale).
  function snapComposerNext() {
    composerSnapNext = true
  }

  function prefersReducedMotion() {
    return typeof window !== 'undefined'
      && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true
  }

  // Bottom-pin the controls directly: the compact row carries `self-center`
  // (align-self) on each control, which would override a container-level
  // align-items and let the ＋ jump to center mid-shrink. Overriding each child's
  // align-self welds the controls to the bottom in both directions. The textarea
  // is skipped on purpose — it stays centered in the compact row, so it slides
  // smoothly instead of snapping from bottom-pinned back to centered when the
  // morph ends (which made the placeholder jump on shrink).
  function pinComposerChildrenBottom(el: HTMLElement, pinned: boolean) {
    const value = pinned ? 'flex-end' : ''
    for (const child of Array.from(el.children)) {
      if (child instanceof HTMLElement && child.tagName !== 'TEXTAREA') {
        child.style.alignSelf = value
      }
    }
  }

  function clearComposerMorphStyles(el: HTMLElement) {
    el.style.overflow = ''
    el.style.alignContent = ''
    pinComposerChildrenBottom(el, false)
  }

  function animateComposerHeight() {
    const el = composerEl.value
    if (!el) return
    // Start from the live visual height when a morph is already running, so a
    // fresh trigger continues from where the eye is instead of snapping back.
    const from = composerHeightAnim ? el.offsetHeight : composerHeight
    composerHeightAnim?.cancel()
    composerHeightAnim = null
    clearComposerMorphStyles(el)
    const target = el.offsetHeight
    composerHeight = target
    composerBoxHeight.value = target
    // Only a pill↔multiline form change earns the height morph. Attachment rows
    // now reveal via their own grid 0fr↔1fr track (card stays put, box grows), and
    // plain line-wraps within multiline snap, so they're deliberately excluded.
    const formChanged = isMultiline.value !== composerMultiline
    composerMultiline = isMultiline.value
    if (!composerHeightReady || composerSnapNext) {
      composerSnapNext = false
      return
    }
    if (!formChanged) return
    if (!isActive.value || !from || Math.abs(target - from) < 0.5 || prefersReducedMotion()) return
    // Pin every line to the bottom and clip the overflow: the control row stays
    // welded to the fixed bottom edge (no twitch) while the box grows/shrinks and
    // the textarea is revealed/concealed from the top.
    el.style.overflow = 'hidden'
    el.style.alignContent = 'flex-end'
    pinComposerChildrenBottom(el, true)
    // A gentle ease-out whose tail decelerates to a soft stop — monotonic, so the
    // height moves to its target and stops without overshooting and bouncing back.
    const anim = el.animate(
      [{ height: `${from}px` }, { height: `${target}px` }],
      { duration: 220, easing: 'cubic-bezier(0.33, 1, 0.68, 1)' },
    )
    composerHeightAnim = anim
    anim.onfinish = () => {
      if (composerHeightAnim === anim) {
        clearComposerMorphStyles(el)
        composerHeightAnim = null
      }
    }
  }

  watch([inputText, isMultiline], () => {
    void nextTick(animateComposerHeight)
  })

  onMounted(() => {
    void nextTick(() => {
      composerHeight = composerEl.value?.offsetHeight ?? 0
      composerBoxHeight.value = composerHeight
      composerMultiline = isMultiline.value
      composerHeightReady = true
      composerSnapNext = false
    })
    const el = composerEl.value
    if (el && typeof ResizeObserver !== 'undefined') {
      composerSizeObserver = new ResizeObserver(() => {
        // The fit check keys off width only, so the height swing of a pill↔multiline
        // morph (same width) can't feed back and re-toggle it.
        recomputeComposerFit()
        // Skip while we drive the height ourselves; only capture layout-driven
        // resizes so the next morph starts from the real current height. The
        // keystroke path sets composerHeightAnim before this fires, so normal
        // morphs are untouched.
        if (!composerHeightAnim) {
          composerHeight = el.offsetHeight
          composerBoxHeight.value = el.offsetHeight
        }
      })
      composerSizeObserver.observe(el)
    }
  })

  onBeforeUnmount(() => {
    composerSizeObserver?.disconnect()
    composerSizeObserver = null
    composerHeightAnim?.cancel()
    composerHeightAnim = null
  })

  onDeactivated(() => {
    composerHeightAnim?.cancel()
    composerHeightAnim = null
    if (composerEl.value) clearComposerMorphStyles(composerEl.value)
    composerSnapNext = true
  })

  return {
    textareaEl,
    composerEl,
    modelLabelEl,
    isMultiline,
    composerRadiusMs,
    composerRadiusEase,
    focusTextarea,
    modelTriggerMaxWidth,
    snapComposerNext,
  }
}
