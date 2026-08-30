import type { Ref } from 'vue'
import {
  computed,
  nextTick,
  onBeforeUnmount,
  ref,
  watch,
} from 'vue'
import { useScroll } from '@vueuse/core'
import type { ChatMessage } from '@/store/chat-list'

export interface ScrollTweenOptions {
  duration?: number
  now?: () => number
  raf?: (cb: FrameRequestCallback) => number
  caf?: (handle: number) => void
}

// The tween re-reads its target every frame, so positions shifted by
// late layout settles (markdown re-render, code highlighting, image
// loads, KaTeX/Mermaid resolves) still land exactly.
export function animateScrollTo(
  el: { scrollTop: number },
  getTarget: () => number,
  options: ScrollTweenOptions = {},
): () => void {
  const duration = options.duration ?? 450
  const now = options.now ?? (() => performance.now())
  const raf = options.raf ?? (cb => requestAnimationFrame(cb))
  const caf = options.caf ?? (handle => cancelAnimationFrame(handle))
  const start = el.scrollTop
  const startedAt = now()
  let cancelled = false
  let handle = 0
  const frame = () => {
    if (cancelled) return
    const progress = duration > 0 ? Math.min(1, (now() - startedAt) / duration) : 1
    const eased = 1 - (1 - progress) ** 5
    el.scrollTop = start + (getTarget() - start) * eased
    if (progress < 1) handle = raf(frame)
  }
  handle = raf(frame)
  return () => {
    if (cancelled) return
    cancelled = true
    caf(handle)
  }
}

const TWEEN_DURATION_MS = 450

// The pin entrance runs LONGER than utility jumps: it is the "turn the page"
// moment of sending, and at 450ms the ease-out's deceleration tail is barely
// perceptible over a near-viewport-height distance. TUNE ME with the user.
const PIN_TWEEN_DURATION_MS = 700

// "At the bottom" is a threshold, not a pixel-perfect landing: sub-pixel
// rounding, the last line growing mid-stream, and fractional zoom all leave a
// few px of slack that must still count as "following". 30px stays under one
// line of body text, so a deliberate scroll-up still unlocks. Measured against
// the CONTENT END (last message's bottom edge), never scrollHeight — see the
// content-end geometry section for the business semantic.
const NEAR_BOTTOM_THRESHOLD_PX = 30

// When a turn is pinned, the user prompt lands this far below the viewport
// top. Sized to leave a visible sliver of the previous turn above the prompt —
// context that the page "turned", not teleported: the top 40px sit under the
// fade overlay (h-10), so roughly the remainder is readable tail. TUNE ME with
// the user against the real layout. Measured, so it is width-agnostic — no
// narrow-screen special case needed.
const PIN_TOP_OFFSET_PX = 140

export interface UseChatScrollOptions {
  scrollEl: Ref<HTMLElement | null>
  /** Content-height probe — the scroll viewport's first child. */
  contentEl: Ref<HTMLElement | null>
  /**
   * Container of the LAST turn (chat-pane renders one persistent container
   * per turn and binds this ref to the last one). Used for measurement only
   * — the reserve itself is DECLARATIVE state (turnReserves) projected by
   * chat-pane as a :style binding per turn; see the reserve block below.
   */
  lastTurnEl: Ref<HTMLElement | null>
  messages: Ref<ChatMessage[]>
  isActive: Ref<boolean>
  sessionId: Ref<string | null | undefined>
}

/**
 * useChatScroll — every scroll behavior for the chat message list.
 *
 * ─── What it does ─────────────────────────────────────────────────────────
 *   • pin a just-sent prompt near the viewport top and let the reply stream
 *     into reserved blank below (send = "turn the page")
 *   • stick-to-bottom follow while a reply streams in — but only after the
 *     user opts in by scrolling to the bottom; pin and follow never overlap
 *   • let the user scroll away ("escape") and STAY there
 *   • jump-to-message + transient highlight (reply refs, the scroll rail)
 *   • keep the viewport still while older history is prepended at the top
 *   • restore scroll position across KeepAlive tab switches; a freshly
 *     opened session lands at the bottom (entry pin was a feature cut)
 *
 * ─── The one idea the whole file rests on ─────────────────────────────────
 * Follow and escape pull the scroll position in opposite directions, so
 * everything hinges on telling *the code's own scrolling* apart from *the
 * user's*. They are indistinguishable at the `scroll`-event layer — both just
 * fire `scroll`. The naive design (which this file was rewritten to kill) reads
 * `scroll` direction to decide "did the user leave?" while also snapping to the
 * bottom on every content growth; the follow's own scroll events then read as
 * user activity, the two fight every frame, and the user physically cannot
 * scroll away from a streaming reply.
 *
 * The fix is two independent guards:
 *   1. `isProgrammaticScroll` brackets every scroll the code performs; the
 *      `scroll` handler ignores events while it is set, so a follow scroll is
 *      never misread as the user leaving.
 *   2. Escape is latched only while a physical wheel, touch, pointer, or
 *      keyboard gesture surrounds the resulting `scrollTop` delta.
 *
 * ─── State model ──────────────────────────────────────────────────────────
 * The hot-path latches are plain closure vars, NOT refs on purpose: they move
 * on every frame of a scroll and must never trigger a re-render. Exactly ONE
 * reactive mirror, `isAtBottom`, is exposed to the UI (the jump-to-bottom
 * button); update it wherever scrollTop changes, never read the latches from a
 * template.
 *
 *   isProgrammaticScroll  code is mid-scroll → treat scroll events as "ours"
 *   followEnabled         content growth may pull the viewport to the bottom;
 *                         false = parked (user escaped, or a turn is pinned)
 *   pinPending/pinAnchorId one-shot pin, applied when the target prompt renders
 *   pinScrollActive       pin's entrance tween in flight → freeze isAtBottom
 *   lastScrollTop         previous frame's scrollTop, for the up/down test
 *   lockScroll (ref)      init / cross-tab restore running → freeze BOTH follow
 *                         and escape so their setup scrolls latch nothing
 *
 * ─── Event flow ───────────────────────────────────────────────────────────
 *   MutationObserver(content subtree) ─ streaming mutates the DOM ─▶ apply an
 *       armed pin (once, when the fresh prompt is in the DOM), then follow to
 *       the bottom IF followEnabled.
 *   ResizeObserver(content element) ─ image/font/layout-only reflow ─▶ run the
 *       same gated heartbeat. It never writes scrollTop merely because height
 *       changed: parked views stay parked, while an explicitly-following view
 *       is healed to the new bottom.
 *   wheel  ─▶ physical intent; deltaY<0 (up) parks the view immediately.
 *   scroll ─▶ refresh isAtBottom; if it is not our own scroll, run the latch.
 *
 * ─── Follow on / off ──────────────────────────────────────────────────────
 * Pin and follow are mutually exclusive phases, switched only by explicit
 * actions:
 *   • user scrolls down to the bottom                    → follow ON
 *     (parked at the pin already IS the bottom; arming there is a no-op
 *     until the reply outgrows the reserve)
 *   • jump-to-bottom button / session switch             → follow ON
 *   • any upward wheel / scroll                          → follow OFF
 *   • jump-to-message / rail / prepend (markEscaped)     → follow OFF
 *   • send (pinAfterSend)                                → follow OFF (parked)
 * There is deliberately NO "relock shortly after a downward pause" timer: it
 * would re-arm follow on any small downward nudge while a turn is parked, and
 * the next streamed token would yank the parked view to the bottom.
 *
 * ─── Prepend (older history) ──────────────────────────────────────────────
 * Loading older messages is treated as an escape (suppressAutoScrollForPrepend
 * → markEscaped): follow stays off, and the browser's native `overflow-anchor`
 * keeps the visible content stationary across the insert. There is NO manual
 * scrollTop compensation — and you must NOT set `overflow-anchor: none` on the
 * viewport. That was tried (to protect a browser-native smooth entrance
 * scroll, which anchoring can cancel) and it broke two things at once: each
 * prepend batch twitched, and the pin drifted off its offset — a
 * one-shot manual compensation / one-shot landing cannot track the ASYNC
 * layout settles (code highlighting, images, fonts) that keep resizing rows
 * after the DOM lands. Native anchoring corrects continuously; the pin's JS
 * tween is immune to it (it rewrites scrollTop every frame), so they coexist.
 *
 * ─── Browser vs hand-written scroll (read before changing pin/reserve) ───
 * We do NOT replace the browser's scroller. Most of the time the browser
 * owns scrollTop:
 *   • everyday wheel/touch scrolling
 *   • native overflow-anchor during history prepend and async reflow
 *     (Shiki / KaTeX / images / fonts) — see Prepend above
 *   • clamp when content shrinks past the max scroll
 *
 * What the browser does NOT know is chat product geometry:
 *   • residual pin blank is deliberate "reply room", not ordinary content
 *   • send = pin the new prompt at PIN_TOP_OFFSET_PX with previous-turn peek
 *   • that blank SURVIVES stream completion and stays until the next
 *     send's handover (legal layout — never trimmed while the view lives)
 *   • entrance needs a quintic ease-out tween, not native smooth
 *
 * So we hand-write scrollTop only on a NARROW boundary: the frame that
 * hands a pin from one turn to the next, plus the entrance tween itself.
 * That is intentional policy, not a temporary hack — but the surface must
 * stay narrow. If a new height-change path (Thought expand, tool group,
 * split pane, tab restore, …) seems to need the same compensation, do NOT
 * call collapseReserveKeepingView from there and do NOT add a second
 * settle-time / delayed "restore" layer. Either the product geometry
 * changed (rethink reserve structure) or the bug is elsewhere (follow
 * arming, remount, id identity). Three rewrite rounds died on "mechanism
 * got clever"; the surviving rule is: one named handover, one formula,
 * everything else stays dumb and browser-owned.
 *
 * ─── Pin (send only) ──────────────────────────────────────────────────────
 * On send (chat-pane → pinAfterSend) the newest prompt is pinned near the top
 * and the reply streams into reserved blank below. Mechanism: chat-pane
 * renders every turn in its own PERSISTENT container (keyed by the turn's
 * opening message — a send appends a container, it never re-parents previous
 * turns' DOM; re-parenting remounts the subtree and its transient height
 * collapse read as a scroll jump).
 *
 * tryApplyPin order is load-bearing (single coordinate system):
 *   1. Retire the PREVIOUS turn's residual blank via
 *      collapseReserveKeepingView (position-aware — see that function).
 *   2. Measure and set the NEW turn's min-height once.
 *   3. Tween to prompt top − PIN_TOP_OFFSET_PX.
 *
 * Two rejected alternatives (do not resurrect without a new product reason):
 *   A. Keep old blank for the whole flight, clear at settle.
 *      Document is [prev content | EMPTY SPACING | new prompt | new blank].
 *      The tween pins the new prompt by scrolling through that empty band,
 *      so the previous turn leaves the viewport and looks unmounted; settle
 *      then removes the spacing and the previous turn "reappears". User-
 *      confirmed wrong.
 *   B. Clear old blank before the tween with flat `scrollTop -= fullDelta`.
 *      Keeps content BELOW the blank fixed — but when the user is parked on
 *      the previous pin (reading content ABOVE the blank) or reading history
 *      far above, that yanks the whole view before the entrance starts.
 *      User-confirmed wrong.
 *
 * Position-aware collapse (A/B midpoint) only compensates the removed band
 * that sat above the viewport top. Parked on previous-turn content, the
 * blank is below the viewport top → scrollTop stays put → blank vanishes →
 * the down animation then carries REAL previous-turn content into the peek.
 * From there CSS layout does everything: the reply consumes the new reserve,
 * tool/Thought toggles use more or less of it, content past min-height makes
 * it inert. The reserve is LEGAL LAYOUT for the rest of this view's life —
 * NEVER recomputed, never trimmed on completion or scroll (Grok-parity
 * rule: spacing must not jump). It dies only at the next send's handover,
 * a session switch, or the view unmounting (KeepAlive tab switches keep
 * the DOM, so they keep the spacing). While pinned, follow is OFF; it re-arms on
 * any downward scroll at the bottom, which parked-at-pin trivially is —
 * harmless, since follow-to-scrollHeight is a no-op there.
 *
 * Opening a session does NOT pin — it lands at the bottom via the follow
 * heartbeat (entry pin was a deliberate feature cut; see the sessionId
 * watch). Only sending pins.
 *
 * ─── chat-pane contract ───────────────────────────────────────────────────
 * chat-pane owns the DOM refs and drives this composable through:
 *   scrollToBottom (jump button) · scrollToMessage (reply refs) · pinAfterSend /
 *   pinAfterSteer (live queue boundary)
 *   (on send) · suppressAutoScrollForPrepend (top sentinel) · markEscaped +
 *   startScrollTween + findMessageElement + getElementAbsoluteTop (scroll rail)
 *   · onMessageActive (per message-item) · onActivatedRestoreScroll /
 *   onDeactivatedResetScroll (its own KeepAlive hooks).
 */
export function useChatScroll(options: UseChatScrollOptions) {
  const { scrollEl, contentEl, lastTurnEl, messages, isActive, sessionId } = options

  const highlightedMessageId = ref('')
  // Reactive mirror of "is the viewport at the bottom". This is the ONLY
  // follow/escape state that feeds the UI (the jump-to-bottom button); the
  // hot-path latches below are deliberately non-reactive so a scroll storm
  // never triggers re-renders.
  const isAtBottom = ref(true)
  // Held true during session load / cross-tab restore so neither the follow
  // nor the escape latch reacts to the programmatic scrolls those flows make.
  const lockScroll = ref(true)

  const { isScrolling } = useScroll(scrollEl)

  // --- Follow / pin latches (non-reactive on purpose) ---
  // True around a scroll the code itself performs, so the resulting scroll
  // event is not misread as user intent.
  let isProgrammaticScroll = false
  let lastScrollTop = 0
  // THE mode switch: while true, content growth pulls the viewport to the
  // bottom; while false the view is parked (user scrolled up, or a just-sent
  // turn is pinned). Flipped only by explicit actions — see "Follow on / off"
  // in the header.
  let followEnabled = true
  // One-shot pin: armed by pinAfterSend, applied by the content heartbeat on
  // the first DOM/geometry change where the target prompt is actually present.
  let pinPending = false
  // Last user message id at arm time — the pin waits for a NEWER one, so a
  // stray mutation (a late token of the previous reply) can't size the pin
  // against the previous turn's prompt.
  let pinAnchorId: string | null = null
  // True while the pin's entrance tween is in flight; freezes the isAtBottom
  // mirror so the jump button doesn't flash mid-animation. Cleared by the
  // settle timer (the tween has no completion callback).
  let pinScrollActive = false
  let pinSettleTimer: ReturnType<typeof setTimeout> | null = null
  // --- The pin reserve: DECLARATIVE render state, keyed by TURN IDENTITY --
  // This map is the source of truth; chat-pane projects it per turn via
  // turnReserveStyle(turn.id) as a :style min-height binding. Two design
  // points, both learned the hard way:
  //   • Declarative, not an imperative inline style: the style is part of
  //     the render, so a container remount (stream completion re-keys the
  //     turn) re-renders WITH the reserve — a reserve-less frame (and the
  //     scrollTop clamp it caused: view silently shoved up, fake jump
  //     arrow) cannot be produced. The imperative design needed a restore
  //     helper plus a remount watcher to approximate this, and still lost
  //     the race whenever anything forced layout mid-remount.
  //   • Keyed by the turn's opening message id, NOT by position: positional
  //     bindings re-map onto different containers the instant the turn
  //     count changes mid-send (optimistic append lands several microtasks
  //     after pinAfterSend). Id keying is inert to turn-count changes; it
  //     relies on render ids being stable across the optimistic → server
  //     consolidation, which the store guarantees (adoptRenderIdentity).
  //
  // BUSINESS INVARIANT unchanged: the reserve must NOT change when the
  // stream finishes — it is legal layout until the next send's handover,
  // a session switch, or the view unmounting.
  const turnReserves = ref<Map<string, number>>(new Map())
  function turnReserveStyle(turnId: string): { minHeight: string } | undefined {
    const px = turnReserves.value.get(turnId)
    return px === undefined ? undefined : { minHeight: `${px}px` }
  }
  // Turn id holding the CURRENT pin's reserve (the entry the next send's
  // handover collapses). An id, not an element pointer — element pointers
  // die on remount, which was the imperative design's disease.
  let pinnedTurnId: string | null = null

  // ── collapseReserveKeepingView ─────────────────────────────────────────
  // THE only place that hand-adjusts scrollTop in response to a pin-reserve
  // height drop. Call site is intentionally singular: tryApplyPin, when the
  // previous pin container is not the container about to receive the new
  // reserve. Do not reuse for Thought/tool expands, stream completion,
  // remounts, or prepend — those stay browser-owned (overflow-anchor) or
  // follow/escape owned.
  //
  // Why we write scrollTop at all (vs "let the browser handle shrink"):
  //   Residual pin blank is product geometry, not content. Clearing
  //   min-height is a deliberate layout edit on the send handover. The
  //   browser's clamp / scroll anchoring do not know we want "parked on
  //   previous-turn content → keep that content still; blank below may
  //   vanish". Left alone, clamp/anchoring and a later tween fight; the
  //   user either sees a pre-tween yank or flies through empty spacing.
  //   This function is the narrow policy that makes the handover match
  //   that product intent. It is NOT a general scroll-anchoring reinstall.
  //
  // Geometry (residual blank always at the BOTTOM of its turn container,
  // above the next turn's prompt):
  //
  //   before clear:  container height = max(content, minHeight)
  //   after clear:   container height = content
  //   delta         = before − after  (≥ 0; the blank we remove)
  //   collapseTop   = absolute Y where the blank started
  //                 = containerTop + contentHeight
  //                 = containerTop + (before − delta)
  //   removed band  = [collapseTop, collapseTop + delta)
  //
  // Position-aware scrollTop (only when delta > 0):
  //
  //   • scrollTop ≤ collapseTop
  //       Viewport top is above the blank (reading previous-turn content,
  //       or history further up). Leave scrollTop alone. The blank below
  //       the user vanishes; what they were looking at stays put. This is
  //       the common "send while still parked on the previous pin" path
  //       and "scroll up into history then send".
  //
  //   • scrollTop > collapseTop
  //       Viewport top sits inside or past the blank. Subtract only the
  //       removed length that was above the viewport top:
  //         scrollTop -= min(delta, scrollTop − collapseTop)
  //       so content that lived BELOW the blank does not jump.
  //
  // Rejected formulas (do not bring back):
  //   • scrollTop -= delta always
  //       Anchors content below the blank. Yanks history readers toward
  //       the top; shifts a parked pin's visible content before the
  //       entrance tween — "spacing snaps off, whole view jumps, then
  //       the down animation starts".
  //   • defer clear until tween settle (+ prompt-as-anchor restore)
  //       Flight runs with [prev | EMPTY | new prompt]. Tween scrolls
  //       through the empty band; previous turn looks unmounted until
  //       settle removes spacing and it reappears.
  //   • prompt-as-anchor compensation on the PRE-tween clear
  //       Keeps the NEW prompt fixed; when the user is still looking at
  //       previous-turn content, THAT is what jumps. Wrong anchor for
  //       the send-from-parked-pin case.
  //
  // After this returns, tryApplyPin measures the new reserve and starts
  // the tween in a single-reserve coordinate system — previous-turn
  // content is what peeks above the new prompt, not empty spacing.
  function collapseReserveKeepingView(el: HTMLElement, turnId: string) {
    const container = turnContainerOf(turnId)
    // Drop the reserve from render state FIRST; if its container is gone the
    // blank is already gone with it and there is nothing to compensate.
    const nextMap = new Map(turnReserves.value)
    nextMap.delete(turnId)
    turnReserves.value = nextMap
    if (!container?.isConnected) return
    const rootRect = el.getBoundingClientRect()
    const before = container.offsetHeight
    // Absolute top of the container in scroll content coordinates (same
    // basis as scrollTop), measured BEFORE clearing min-height so
    // collapseTop refers to the pre-clear layout.
    const containerTop = el.scrollTop + container.getBoundingClientRect().top - rootRect.top

    // The reactive binding clears on Vue's next flush; the handover needs
    // this frame's geometry, so mirror the removal synchronously. The next
    // patch renders the same (absent) value and owns it from there.
    container.style.minHeight = ''
    const delta = before - container.offsetHeight
    if (delta <= 0) return

    // Removed band was [containerTop + after, containerTop + before] with
    // after = before - delta (= content height once min-height is gone).
    const collapseTop = containerTop + (before - delta)
    if (el.scrollTop > collapseTop) {
      el.scrollTop = Math.max(0, el.scrollTop - Math.min(delta, el.scrollTop - collapseTop))
    }
  }

  // Coalesced post-paint refresh of the isAtBottom mirror. An observer callback
  // can read geometry MID-layout: heavy markdown / tool rows can be
  // transiently taller for a frame before async reflow (Shiki, collapse)
  // settles, so a reading taken there can latch a stale "not at bottom" (fake
  // jump arrow). If the stream ends right then, nothing ever corrects it —
  // no further mutation, no scroll event. This rAF re-reads AFTER paint;
  // any later async reflow mutates again and schedules another pass.
  let atBottomRefreshRaf = 0
  function scheduleAtBottomRefresh() {
    if (atBottomRefreshRaf) return
    atBottomRefreshRaf = requestAnimationFrame(() => {
      atBottomRefreshRaf = 0
      const el = scrollEl.value
      if (!el || pinScrollActive) return
      isAtBottom.value = isNearBottom(el)
    })
  }

  let highlightTimer: ReturnType<typeof setTimeout> | null = null
  let cancelScrollTween: (() => void) | null = null
  let tweenFlagTimer: ReturnType<typeof setTimeout> | null = null
  let mutationObserver: MutationObserver | null = null
  let contentResizeObserver: ResizeObserver | null = null
  let pinAttemptId = 0
  let appliedPinAttemptId = 0

  // "At the bottom" = the plain scrollHeight test. The pin reserve is LEGAL
  // LAYOUT (Grok-parity business rule): parked at the pin IS the physical
  // bottom, so no separate "content end" notion exists — one predicate serves
  // the jump button, follow arming, and the jump target alike.
  function isNearBottom(el: HTMLElement): boolean {
    return el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_THRESHOLD_PX
  }

  // Scroll target for "go to the bottom": the physical bottom.
  function bottomTarget(el: HTMLElement): number {
    return el.scrollHeight - el.clientHeight
  }

  // THE one landing rule for every "bring this message into view" path —
  // reply-ref jumps, the scroll rail, and the pin's own entrance all resolve
  // through this: target top sits PIN_TOP_OFFSET_PX below the viewport top,
  // exactly where a pinned prompt lands. Jumping back to an old turn therefore
  // reproduces the same geometry the user saw right after sending it.
  function messageJumpTarget(root: HTMLElement, messageId: string): number {
    const el = findMessageElement(messageId)
    return el ? getElementAbsoluteTop(el, root) - PIN_TOP_OFFSET_PX : root.scrollTop
  }

  // Stop following, immediately. Called for any deliberate move away from the
  // bottom (jump-to-message, rail navigation, prepend of older history).
  function markEscaped() {
    followEnabled = false
  }

  // Re-arm following. The content heartbeat picks it back up on the next
  // growth; used by the jump-to-bottom button and session switches.
  function followBottom() {
    followEnabled = true
  }

  // Called when the user sends. Pin and follow are MUTUALLY EXCLUSIVE phases:
  // sending parks the view and arms a one-shot pin; the content heartbeat
  // applies it when the new prompt renders (tryApplyPin). Streaming then grows
  // below the fold without moving the view — follow re-engages only when the
  // user scrolls back down to the bottom.
  function armPin(anchorId: string | null): () => void {
    const attemptId = ++pinAttemptId
    const previousFollowEnabled = followEnabled
    const previousPinPending = pinPending
    const previousPinAnchorId = pinAnchorId
    const previousPinnedTurnId = pinnedTurnId
    const previousTurnReserves = new Map(turnReserves.value)
    const previousScrollTop = scrollEl.value?.scrollTop ?? 0
    let active = true

    followEnabled = false
    pinPending = true
    pinAnchorId = anchorId ?? lastUserMessage()?.id ?? null
    // Arm only — do NOT clear / set reserves here.
    //
    // sendMessage pushes the optimistic user turn only after several awaits
    // (command parse, session setup, …). Anything we mutate now paints one
    // Vue flush BEFORE that turn exists: a positional or "clear previous
    // blank now" edit either hits the wrong container or shrinks scrollHeight
    // under a bottom-parked viewport (zero-frame jerk). The full handover
    // (collapseReserveKeepingView → new min-height → tween) runs in
    // tryApplyPin on the first mutation where the NEW prompt is in the DOM.

    // The store calls this rollback only when it had started a real turn but
    // failed before any visible response was established. Command-only paths
    // never arm a pin in the first place. Restoring the full snapshot also
    // covers the rare case where the optimistic prompt rendered before a
    // startup transport failure arrived.
    return () => {
      if (!active || attemptId !== pinAttemptId) return
      active = false
      const pinWasApplied = appliedPinAttemptId === attemptId
      if (pinWasApplied) {
        cancelScrollTween?.()
        if (pinSettleTimer) {
          clearTimeout(pinSettleTimer)
          pinSettleTimer = null
        }
        pinScrollActive = false
        appliedPinAttemptId = 0
      }
      pinPending = previousPinPending
      pinAnchorId = previousPinAnchorId
      pinnedTurnId = previousPinnedTurnId
      turnReserves.value = previousTurnReserves
      followEnabled = previousFollowEnabled

      void nextTick(() => {
        const el = scrollEl.value
        if (!el) return
        if (previousFollowEnabled) {
          stickToBottomNow()
          return
        }
        isProgrammaticScroll = true
        const max = Math.max(el.scrollHeight - el.clientHeight, 0)
        el.scrollTop = Math.min(Math.max(previousScrollTop, 0), max)
        lastScrollTop = el.scrollTop
        requestAnimationFrame(() => {
          isProgrammaticScroll = false
          const current = scrollEl.value
          if (current) isAtBottom.value = isNearBottom(current)
        })
      })
    }
  }

  function pinAfterSend(): () => void {
    return armPin(lastUserMessage()?.id ?? null)
  }

  // A steer is projected into the transcript before this method is called.
  // Its prompt is therefore already the newest user message; the anchor must
  // be supplied explicitly so tryApplyPin waits for that prompt rather than
  // treating the steer itself as the old turn.
  function pinAfterSteer(anchorId: string): () => void {
    const rollback = armPin(anchorId.trim() || null)
    void nextTick(() => {
      if (!pinPending) return
      onContentChanged()
    })
    return rollback
  }

  // A follow-up continuation is a new user turn created by the server after
  // the previous run reaches its final boundary. It must use the same
  // position-aware reserve handover as a normal send; otherwise the previous
  // turn's pin reserve remains between the two turns and the continuation
  // appears far below its predecessor. A pending send pin owns this boundary
  // already, so this is intentionally a no-op in that case.
  function pinAfterFollowUp(anchorId: string): () => void {
    if (pinPending) return () => {}
    const rollback = armPin(anchorId.trim() || null)
    void nextTick(() => {
      if (!pinPending) return
      onContentChanged()
    })
    return rollback
  }

  function startScrollTween(
    root: HTMLElement,
    getTarget: () => number,
    duration: number = TWEEN_DURATION_MS,
  ) {
    cancelScrollTween?.()
    // A tween is a programmatic scroll; flag it for its whole run so its
    // per-frame scrollTop moves are never latched as a user escape.
    isProgrammaticScroll = true
    if (tweenFlagTimer) {
      clearTimeout(tweenFlagTimer)
      tweenFlagTimer = null
    }
    const stop = animateScrollTo(root, () => {
      const max = Math.max(root.scrollHeight - root.clientHeight, 0)
      return Math.min(Math.max(getTarget(), 0), max)
    }, { duration })
    const cancel = () => {
      stop()
      isProgrammaticScroll = false
      if (tweenFlagTimer) {
        clearTimeout(tweenFlagTimer)
        tweenFlagTimer = null
      }
      root.removeEventListener('wheel', cancel)
      root.removeEventListener('touchstart', cancel)
      cancelScrollTween = null
    }
    root.addEventListener('wheel', cancel, { passive: true })
    root.addEventListener('touchstart', cancel, { passive: true })
    cancelScrollTween = cancel
    // animateScrollTo has no completion callback; drop the flag once the tween
    // can no longer be running.
    tweenFlagTimer = setTimeout(() => {
      isProgrammaticScroll = false
      tweenFlagTimer = null
      // A zero-distance tween (already at the target) fires no scroll event;
      // refresh the mirror here or a stale arrow survives the click.
      scheduleAtBottomRefresh()
    }, duration + 100)
  }

  function getElementAbsoluteTop(target: HTMLElement, root: HTMLElement) {
    return root.scrollTop + target.getBoundingClientRect().top - root.getBoundingClientRect().top
  }

  // The most recent user turn — the message a pin anchors to the top. Scans
  // back through the flat message list (user/assistant/system interleaved) for
  // the last `user` entry.
  function lastUserMessage(): ChatMessage | null {
    const list = messages.value
    for (let i = list.length - 1; i >= 0; i--) {
      if (list[i]?.role === 'user') return list[i]!
    }
    return null
  }

  // Apply an armed pin: size the last-turn container's reserve ONCE, then
  // tween the viewport to the shared pin target (prompt top − offset).
  // The reserve is min-height, so all
  // later geometry is absorbed by CSS layout with zero JS involvement; see
  // the header's Pin section for why it must be set exactly once and left
  // alone (including after the stream completes).
  //
  // Pipeline (order is the product; do not reorder "for simplicity"):
  //   arm (pinAfterSend / session watch) → heartbeat sees prompt → tryApplyPin
  //     → collapse previous residual blank (if any)
  //     → measure + set new min-height
  //     → pinTarget tween/jump
  //   settle timer only clears pinScrollActive / refreshes isAtBottom —
  //   it must NOT touch reserves (handover already finished before flight).
  //
  // Geometry for the NEW reserve R (measured only after old blank is gone):
  //   with `below` = everything under the new container (column bottom pad),
  //     scrollTop@bottom = containerTop + R + below − clientHeight
  //   want that equal to promptTop − PIN_TOP_OFFSET_PX:
  //     R = clientHeight − below − PIN_TOP_OFFSET_PX + (promptTop − containerTop)
  //   Floor: prompt taller than the ideal reserve can't land its top at the
  //   pin point — keep breathing room below its tail instead.
  function tryApplyPin(el: HTMLElement): boolean {
    const container = lastTurnEl.value
    if (!container) return false
    const prompt = lastUserMessage()
    if (!prompt) return false
    // Wait until a user message NEWER than the one present at arm time is
    // actually rendered — an unrelated mutation must not size the pin
    // against the previous turn's prompt.
    if (prompt.id === pinAnchorId) return false
    const promptEl = findMessageElement(prompt.id)
    if (!promptEl) return false
    // The prompt must live INSIDE the last-turn container — mid-patch the ref
    // and the rendered rows can briefly disagree; sizing against a mismatched
    // pair would reserve garbage.
    if (!container.contains(promptEl)) return false
    // BUSINESS RULE: the session's FIRST turn never pins — there is no
    // content above the prompt to peek at, so a reserve would only add dead
    // scroll range. Disarm and hand over to the follow heartbeat, whose follow
    // branch lands the view at the bottom on this same change. Covers draft
    // sends and the first send in an empty session alike.
    if (messages.value[0]?.id === prompt.id) {
      pinPending = false
      followBottom()
      return false
    }
    pinPending = false
    // A pinned turn is a parked view — follow re-engages only when the user
    // scrolls back into the content-end band. (pinAfterSend already parked;
    // re-assert against anything that re-armed follow in between.)
    followEnabled = false

    // ── Reserve HANDOVER (single coordinate system) ────────────────────
    // Step 1: retire previous residual blank with position-aware
    // compensation (collapseReserveKeepingView). MUST run before measuring
    // the new reserve and before the entrance tween:
    //   • If the old blank stays during flight, pinTarget is measured with
    //     empty spacing above the new prompt — the tween scrolls through
    //     that spacing; previous turn looks unmounted until something later
    //     clears the blank.
    //   • If we clear with flat scrollTop -= delta, parked/history views
    //     jump before the tween starts.
    // Bracket as programmatic so any scrollTop tweak cannot latch as a
    // user escape before startScrollTween takes over the flag.
    isProgrammaticScroll = true
    if (pinnedTurnId && pinnedTurnId !== prompt.id) {
      collapseReserveKeepingView(el, pinnedTurnId)
    }
    pinnedTurnId = prompt.id

    // Step 2: measure + set NEW reserve once. `below` / prompt offsets are
    // only honest after step 1 — dual-reserve layout would inflate
    // containerTop and lie about how much blank the new turn still needs.
    const containerTop = getElementAbsoluteTop(container, el)
    const promptOffsetInTurn = getElementAbsoluteTop(promptEl, el) - containerTop
    const below = el.scrollHeight - containerTop - container.offsetHeight
    const ideal = el.clientHeight - below - PIN_TOP_OFFSET_PX + promptOffsetInTurn
    const floor = promptOffsetInTurn + promptEl.offsetHeight + Math.round(el.clientHeight / 3)
    const reservePx = Math.max(0, Math.round(Math.max(ideal, floor)))
    turnReserves.value = new Map(turnReserves.value).set(prompt.id, reservePx)
    appliedPinAttemptId = pinAttemptId
    // Immediate projection of the same value: the reactive binding lands on
    // Vue's next flush, but the tween below needs this frame's geometry.
    // The binding renders the identical value and owns it from the next
    // patch on — including across every future remount.
    container.style.minHeight = `${reservePx}px`
    lastScrollTop = el.scrollTop

    // Step 3: landing target — resolves through messageJumpTarget, THE one
    // formula shared with reply jumps and the scroll rail, so sending and
    // jumping back to a message cannot drift apart. Anchored on the PROMPT,
    // never on scrollHeight (a bottom-anchored target moves mid-flight when
    // the reply outgrows the reserve). Re-resolved every tween frame because
    // optimistic → server id swap can remount the prompt row mid-flight.
    // Measured only after step 1 so distance and peek are real previous-turn
    // content.
    const pinTarget = () => {
      const cur = lastUserMessage()
      return cur ? messageJumpTarget(el, cur.id) : el.scrollTop
    }

    // Entrance: shared JS tween (same as jump-to-bottom / reply jumps).
    // Native smooth was rejected: Chromium's profile reads as constant-
    // speed, and content mutations cancel the flight mid-stream. The
    // tween re-reads pinTarget each frame; wheel/touch cancels it inside
    // startScrollTween. Settle timer only ends pinScrollActive — no
    // reserve work (that finished in steps 1–2).
    pinScrollActive = true
    if (pinSettleTimer) clearTimeout(pinSettleTimer)
    pinSettleTimer = setTimeout(() => {
      pinSettleTimer = null
      pinScrollActive = false
      const cur = scrollEl.value
      if (!cur) return
      isAtBottom.value = isNearBottom(cur)
      lastScrollTop = cur.scrollTop
    }, PIN_TWEEN_DURATION_MS + 100)
    startScrollTween(el, pinTarget, PIN_TWEEN_DURATION_MS)
    return true
  }

  // Instant follow used by the content heartbeat while follow is engaged. Marks
  // itself programmatic so the scroll it triggers is not read as user intent.
  //
  // Timing: `scrollTo` dispatches its `scroll` event before the next rAF fires,
  // so the scroll handler runs while `isProgrammaticScroll` is still true and
  // correctly ignores it. The rAF then clears the flag and refreshes the
  // at-bottom mirror. Do not "simplify" by clearing the flag synchronously —
  // the scroll event would then be read as a user gesture.
  function stickToBottomNow() {
    const el = scrollEl.value
    if (!el) return
    isProgrammaticScroll = true
    el.scrollTo({ top: bottomTarget(el), behavior: 'auto' })
    requestAnimationFrame(() => {
      isProgrammaticScroll = false
      const cur = scrollEl.value
      if (!cur) return
      isAtBottom.value = isNearBottom(cur)
      lastScrollTop = cur.scrollTop
    })
  }

  // Deliberate "go to the latest" — the jump-to-bottom button. Re-arms follow
  // and eases down; the content heartbeat keeps it pinned once there.
  function scrollToBottom() {
    const root = scrollEl.value
    if (!root) return
    followBottom()
    startScrollTween(root, () => bottomTarget(root))
  }

  // The persistent per-turn container that holds a message — the element the
  // reserve :style binds to. chat-pane renders one div per turn keyed by the
  // opening message id; the message row lives inside it.
  function turnContainerOf(turnId: string): HTMLElement | null {
    const msgEl = findMessageElement(turnId)
    return (msgEl?.parentElement as HTMLElement | null) ?? null
  }

  function findMessageElement(messageId: string): HTMLElement | null {
    const root = scrollEl.value
    if (!root) return null
    return root.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(messageId)}"]`)
  }

  async function scrollToMessage(messageId: string): Promise<boolean> {
    await nextTick()
    const root = scrollEl.value
    const target = findMessageElement(messageId)
    if (!root || !target) return false
    // Landing on a specific message parks the reader there — stop following.
    markEscaped()
    startScrollTween(root, () => messageJumpTarget(root, messageId))
    highlightedMessageId.value = messageId
    if (highlightTimer) clearTimeout(highlightTimer)
    highlightTimer = setTimeout(() => {
      if (highlightedMessageId.value === messageId) {
        highlightedMessageId.value = ''
      }
    }, 1800)
    return true
  }

  const showJumpToBottom = computed(() =>
    isActive.value
    && messages.value.length > 0
    && !isAtBottom.value,
  )

  // Tracks the viewport-relative top offset of every "active" message element so
  // onActivated can restore scroll to the same anchor. Keyed by message id for
  // O(1) update/remove on every active/inactive transition; long conversations
  // would otherwise pay a linear scan + splice on each transition.
  const elId = new Map<string, number>()

  function onMessageActive(active: boolean, item: { id: string, top: number }) {
    if (lockScroll.value) return
    if (active) {
      elId.set(item.id, item.top)
    } else {
      elId.delete(item.id)
    }
  }

  // Drop accumulated anchors when the active session changes, and land the new
  // session at the bottom via the ordinary follow heartbeat (the
  // content heartbeat sticks to the content end as the messages render).
  // Entry-pin (landing on the last prompt like just-after-send) was a
  // deliberate FEATURE CUT — the user chose plain bottom landing for history
  // sessions; only sending pins.
  watch(sessionId, (_next, prev) => {
    elId.clear()
    // Draft materialization is NOT a navigation: sending from a draft creates
    // the session mid-send (sessionId null → id) while the send pin is still
    // armed. The pin belongs to this very send — killing it here (and arming
    // follow) made the reply stream yank the view and drop the reserve.
    // Only a change AWAY FROM an existing session is a real switch.
    if (pinPending && !prev) {
      pinnedTurnId = null
      turnReserves.value = new Map()
      return
    }
    followBottom()
    // A send pin armed in the previous session must not fire against the new
    // session's rows.
    pinPending = false
    pinAnchorId = null
    // The old session's reserves are meaningless against the new session's
    // turns — clear the map so nothing re-projects onto foreign ids.
    pinnedTurnId = null
    turnReserves.value = new Map()
  })

  watch(isScrolling, (scrolling) => {
    if (scrolling || lockScroll.value || !isActive.value) return
    for (const [id] of elId) {
      const el = findMessageElement(id)
      if (el) elId.set(id, el.getBoundingClientRect().top - 48)
    }
  })

  function onActivatedRestoreScroll(loadingMessages: Ref<boolean>) {
    if (!isActive.value) return
    let done = false
    const unwatch = watch(loadingMessages, async (newValue) => {
      if (done) return
      try {
        // Pick the anchor closest to the top edge of the viewport so the
        // restore lands on the message the user was reading rather than an
        // arbitrary entry from earlier hover state.
        let anchorId: string | undefined
        let anchorTop = Number.POSITIVE_INFINITY
        for (const [id, top] of elId) {
          if (Math.abs(top) < Math.abs(anchorTop)) {
            anchorId = id
            anchorTop = top
          }
        }

        if (anchorId && !newValue) {
          const el: HTMLElement | null = document.querySelector(`[data-message-id="${anchorId}"]`)
          if (el) {
            const cachePos = anchorTop
            el.scrollIntoView()
            requestAnimationFrame(() => {
              requestAnimationFrame(() => {
                scrollEl.value?.scrollBy({
                  top: -cachePos,
                })
              })
            })
          }
          setTimeout(() => {
            lockScroll.value = false
            done = true
            unwatch()
            // Restored to a remembered position: follow only if it is at the
            // bottom.
            const root = scrollEl.value
            followEnabled = root ? isNearBottom(root) : true
            if (root) isAtBottom.value = isNearBottom(root)
          })
        } else {
          if (!newValue) {
            setTimeout(() => {
              lockScroll.value = false
              done = true
              unwatch()
              // No remembered anchor (fresh open / previously at bottom):
              // land at the bottom and let the follow heartbeat keep it
              // there (entry pin was cut — see the sessionId watch).
              followBottom()
              stickToBottomNow()
            })
          }
        }
      } catch (error) {
        done = true
        unwatch()
        throw error
      }
    }, {
      immediate: true,
      flush: 'post',
    })
  }

  function onDeactivatedResetScroll() {
    lockScroll.value = true
    followBottom()
    // The pin reserve (last-turn min-height) intentionally SURVIVES tab
    // switches: KeepAlive preserves the DOM, and clearing it here would make
    // the conversation land in a different spot than the user left it.
    const el = scrollEl.value
    if (el && isNearBottom(el)) {
      elId.clear()
    }
  }

  // --- User-intent detection: the escape latch ---
  // `wheel` is a user-only signal — a programmatic scroll never fires it — so
  // it is the trustworthy source for "the user is moving the view".
  const PHYSICAL_SCROLL_GRACE_MS = 250
  const KEY_NAV_GRACE_MS = 500
  let lastWheelAt = Number.NEGATIVE_INFINITY
  let touchActive = false
  let lastTouchScrollAt = Number.NEGATIVE_INFINITY

  function onWheel(ev: WheelEvent) {
    isProgrammaticScroll = false
    lastWheelAt = performance.now()
    // Upward intent must park immediately, before the browser applies the
    // wheel delta. A downward wheel already at the bottom can re-lock here
    // even when the browser has no remaining distance and emits no scroll;
    // otherwise the subsequent scroll event decides against post-delta
    // geometry.
    handleUserScroll(ev.deltaY < 0)
  }

  function onTouchStart() {
    isProgrammaticScroll = false
    touchActive = true
    lastTouchScrollAt = performance.now()
  }

  function onTouchMove() {
    lastTouchScrollAt = performance.now()
  }

  function onTouchEnd() {
    touchActive = false
    lastTouchScrollAt = performance.now()
  }

  // --- Physical-gesture latches for the scroll paths that bypass `wheel` ---
  // Scrollbar dragging and text-selection auto-scroll arrive as bare scroll
  // events; so does keyboard paging. Track the gestures themselves so
  // onScrollEvent can tell them apart from LAYOUT-induced scrolls (see below).
  let pointerActive = false
  let lastKeyNavAt = Number.NEGATIVE_INFINITY
  function onPointerDown() {
    pointerActive = true
  }
  function onPointerUp() {
    pointerActive = false
  }
  const KEY_NAV = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])
  function onKeyNav(ev: KeyboardEvent) {
    if (!KEY_NAV.has(ev.key)) return
    // Typing in the composer must not count as scroll intent (space/arrows
    // are ordinary editing keys there).
    const t = ev.target as HTMLElement | null
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
    lastKeyNavAt = performance.now()
  }

  function onScrollEvent() {
    const el = scrollEl.value
    if (!el) return
    const top = el.scrollTop
    const isScrollingUp = top < lastScrollTop
    lastScrollTop = top
    // Frozen while the pin's entrance tween animates, or the jump button would
    // flash for its whole flight; the settle timer refreshes it on landing.
    if (!pinScrollActive) isAtBottom.value = isNearBottom(el)
    // A scroll we triggered ourselves only updates the at-bottom mirror; it
    // must never move the follow latch.
    if (isProgrammaticScroll) return
    // Escape may ONLY be latched from a physical gesture. Everything else
    // reaching here is a LAYOUT-induced scroll the browser performed on its
    // own — clamp when content transiently shrinks (completion flips
    // streaming off → the markdown subtree re-renders and is shorter for a
    // beat), or scroll anchoring re-seating the viewport when its anchor
    // node is destroyed by that re-render. Reading direction off such events
    // killed follow exactly at completion and left the view shoved up at the
    // reply's top; enumerating their signatures (a previous clamp-only guard)
    // lost the race whenever content had already regrown by the time the
    // event was delivered. Wheel/touch scroll events do reach here, so their
    // short-lived gesture latches must survive the browser's post-input event
    // ordering. Pointer drag and keyboard paging are latched alongside them.
    const now = performance.now()
    const touchPhysical = touchActive || now - lastTouchScrollAt < PHYSICAL_SCROLL_GRACE_MS
    // Momentum scroll emits no more touch events. Extend the grace window on
    // every scroll frame that still belongs to that gesture so pointercancel
    // and touchend cannot turn momentum into a fake layout scroll.
    if (touchPhysical) lastTouchScrollAt = now
    const physical = pointerActive
      || touchPhysical
      || now - lastWheelAt < PHYSICAL_SCROLL_GRACE_MS
      || now - lastKeyNavAt < KEY_NAV_GRACE_MS
    if (physical) {
      handleUserScroll(isScrollingUp)
      return
    }
    // Layout displacement while following: heal it. Completion re-renders can
    // shove the viewport with no further DOM mutation ever coming to pull it
    // back — the follow heartbeat may see neither a mutation nor a resize, so
    // this scroll event is the only wake-up we get.
    if (followEnabled && !isNearBottom(el)) stickToBottomNow()
  }

  function handleUserScroll(isScrollingUp: boolean) {
    const el = scrollEl.value
    if (!el) return
    if (lockScroll.value) return
    if (isScrollingUp) {
      // Any upward move parks the view immediately (and a pinned turn stays
      // parked).
      followEnabled = false
    } else if (isNearBottom(el)) {
      // Reaching the physical bottom is the only scroll gesture that
      // (re-)arms follow. Parked at the pin the viewport already IS the
      // bottom, and follow-to-bottom there is a no-op until content outgrows
      // the reserve — so arming is harmless by construction. Deliberately NO
      // optimistic "relock shortly after a downward pause" timer — see the
      // header's "Follow on / off" section.
      followEnabled = true
    }
  }

  // ── Pinned-turn identity migration ─────────────────────────────────────
  // Stream completion swaps the opening user message's render id (temp →
  // server) when the store cannot adopt the on-screen id; the v-for key
  // changes and the turn container REMOUNTS under a NEW id. The reserve map
  // is keyed by the OLD id — without migration the fresh container's :style
  // lookup misses and Vue strips the min-height (spacing vanished exactly at
  // completion; the imperative design survived this because its restore
  // helper stamped px onto whatever container was last, id-blind).
  // flush: 'pre' is load-bearing: the migration must land BEFORE the render
  // that re-keys, so the remounted container binds the reserve in the very
  // same patch — a reserve-less frame never exists.
  watch(() => messages.value.map(message => message.id), () => {
    if (!pinnedTurnId) return
    const px = turnReserves.value.get(pinnedTurnId)
    if (px === undefined) return
    const list = messages.value
    if (list.some(m => m.id === pinnedTurnId)) return
    // The pinned turn's opening prompt is still the newest user message —
    // only its id changed. No user message at all means the turn was
    // removed (retry/fork surgery): drop the reserve with it.
    const successor = lastUserMessage()?.id ?? null
    const next = new Map(turnReserves.value)
    next.delete(pinnedTurnId)
    if (successor) next.set(successor, px)
    turnReserves.value = next
    pinnedTurnId = successor
  }, { flush: 'pre' })

  // Follow / pin heartbeat. Streaming (and any other subtree mutation) lands
  // here. Order matters:
  //   1. tryApplyPin if armed — owns the one real handover + entrance; return
  //      so this mutation never ALSO follow-snaps (would cancel the pin).
  //   2. else if followEnabled → stick to the bottom.
  //   3. else refresh jump-button mirror only.
  // Height changes that are NOT a pin handover (Thought expand, tool body,
  // markdown reflow) intentionally do nothing special here when follow is
  // off — the browser keeps the parked view; do not invent collapse/scroll
  // compensation on those paths.
  function onContentChanged() {
    const el = scrollEl.value
    if (!el) return
    if (!isActive.value || lockScroll.value) return
    if (pinPending && tryApplyPin(el)) return
    if (followEnabled) stickToBottomNow()
    else if (!pinScrollActive) {
      isAtBottom.value = isNearBottom(el)
      scheduleAtBottomRefresh()
    }
  }

  function attach(el: HTMLElement) {
    lastScrollTop = el.scrollTop
    el.addEventListener('wheel', onWheel, { passive: true })
    el.addEventListener('scroll', onScrollEvent, { passive: true })
    // Physical-gesture latches: pointerdown on the element covers scrollbar
    // drags and selection auto-scroll; pointerup goes on window because a
    // drag often releases outside the element. Keydown on window because the
    // focused node during keyboard paging may sit outside this subtree.
    el.addEventListener('pointerdown', onPointerDown, { passive: true })
    el.addEventListener('touchstart', onTouchStart, { passive: true })
    el.addEventListener('touchmove', onTouchMove, { passive: true })
    window.addEventListener('pointerup', onPointerUp, { passive: true })
    window.addEventListener('pointercancel', onPointerUp, { passive: true })
    window.addEventListener('touchend', onTouchEnd, { passive: true })
    window.addEventListener('touchcancel', onTouchEnd, { passive: true })
    window.addEventListener('keydown', onKeyNav, { passive: true })
    mutationObserver = new MutationObserver(onContentChanged)
    // childList catches new bubbles/token spans; characterData catches text
    // that streams into an existing node — either can be the stream's growth.
    mutationObserver.observe(el, { childList: true, subtree: true, characterData: true })
  }

  function detach(el: HTMLElement | null) {
    if (el) {
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('scroll', onScrollEvent)
      el.removeEventListener('pointerdown', onPointerDown)
      el.removeEventListener('touchstart', onTouchStart)
      el.removeEventListener('touchmove', onTouchMove)
    }
    window.removeEventListener('pointerup', onPointerUp)
    window.removeEventListener('pointercancel', onPointerUp)
    window.removeEventListener('touchend', onTouchEnd)
    window.removeEventListener('touchcancel', onTouchEnd)
    window.removeEventListener('keydown', onKeyNav)
    pointerActive = false
    touchActive = false
    lastTouchScrollAt = Number.NEGATIVE_INFINITY
    lastWheelAt = Number.NEGATIVE_INFINITY
    lastKeyNavAt = Number.NEGATIVE_INFINITY
    mutationObserver?.disconnect()
    mutationObserver = null
  }

  watch(scrollEl, (el, old) => {
    detach(old ?? null)
    if (el) attach(el)
  }, { immediate: true })

  watch(contentEl, (el) => {
    contentResizeObserver?.disconnect()
    contentResizeObserver = null
    if (!el || typeof ResizeObserver === 'undefined') return
    contentResizeObserver = new ResizeObserver(onContentChanged)
    contentResizeObserver.observe(el)
  }, { immediate: true })

  // Prepend of older history is a deliberate move away from the bottom, so it
  // escapes: the browser's native `overflow-anchor` keeps the visible content
  // stationary across the insert — continuously, including through the async
  // layout settles that follow it. No manual scrollTop compensation (see the
  // header's Prepend section for the failed attempt).
  function suppressAutoScrollForPrepend() {
    markEscaped()
  }

  onBeforeUnmount(() => {
    if (atBottomRefreshRaf) cancelAnimationFrame(atBottomRefreshRaf)
    if (highlightTimer) clearTimeout(highlightTimer)
    if (tweenFlagTimer) clearTimeout(tweenFlagTimer)
    if (pinSettleTimer) clearTimeout(pinSettleTimer)
    cancelScrollTween?.()
    contentResizeObserver?.disconnect()
    contentResizeObserver = null
    detach(scrollEl.value)
  })

  return {
    // state
    isScrolling,
    lockScroll,
    highlightedMessageId,
    showJumpToBottom,

    // primary actions
    scrollToBottom,
    scrollToMessage,
    suppressAutoScrollForPrepend,
    markEscaped,
    followBottom,
    pinAfterSend,
    pinAfterSteer,
    pinAfterFollowUp,

    // lifecycle hooks — call sites live in chat-pane.vue's own onActivated/onDeactivated
    onActivatedRestoreScroll,
    onDeactivatedResetScroll,

    // message-item @active contract
    onMessageActive,

    // per-turn reserve projection — chat-pane binds :style="turnReserveStyle(turn.id)"
    turnReserveStyle,

    // low-level primitives kept public for the scroll rail (the rail's own
    // trigger logic still calls these directly)
    startScrollTween,
    findMessageElement,
    getElementAbsoluteTop,
    messageJumpTarget,
  }
}
