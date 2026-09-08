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
import { animateTurnEntrance } from './turn-entrance'
import { nativeScrollTo } from './native-scroll'

const TURN_ENTRANCE_MAX_DISTANCE_PX = 80

// "At the bottom" is a threshold, not a pixel-perfect landing: sub-pixel
// rounding, the last line growing mid-stream, and fractional zoom all leave a
// few px of slack that must still count as "following". 30px stays under one
// line of body text, so a deliberate scroll-up still unlocks. Measured against
// the CONTENT END (last message's bottom edge), never scrollHeight — see the
// content-end geometry section for the business semantic.
const NEAR_BOTTOM_THRESHOLD_PX = 30

// Keep the pinned prompt below the viewport top so the previous turn
// remains visible above it. The offset is independent of pane width.
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
 * Owns send placement, user-controlled follow, history restoration and jumps.
 * A send retires the previous reserve, parks the viewport at the new prompt,
 * then translates only the latest turn's contents upward. The outer reserve
 * stays untransformed so its geometry cannot compete with the animation.
 * History/reply/rail jumps use browser-owned smooth scrolling.
 * Reserves survive completion and KeepAlive; only a subsequent send or a real
 * session switch clears them. Follow is re-armed by physical downward scrolling
 * at the bottom, never by a stream update alone.
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
  // turn is pinned). Flipped only by explicit actions: a physical downward
  // scroll reaching the bottom or the jump button arms it; an upward scroll,
  // a jump-to-message, a prepend, or a send parks it.
  let followEnabled = true
  // One-shot pin: armed by pinAfterSend, applied by the content heartbeat on
  // the first DOM/geometry change where the target prompt is actually present.
  let pinPending = false
  // Last user message id at arm time — the pin waits for a NEWER one, so a
  // stray mutation (a late token of the previous reply) can't size the pin
  // against the previous turn's prompt.
  let pinAnchorId: string | null = null
  // Freeze the jump-button mirror during latest-turn translation. Completion
  // and user interruption both release it through the same cleanup.
  let pinScrollActive = false
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

  // Retire only the previous send's unused reply room. Compensate the portion
  // above the viewport so the handover does not move the reader before placing
  // the new turn. Ordinary history reflow remains browser-anchor owned.
  //
  // Two alternatives were tried and rejected (both moved content the user was
  // looking at):
  //   • Always subtract the full delta — anchors content BELOW the blank, so
  //     anyone parked on previous-turn content or history above it gets yanked
  //     before the entrance starts.
  //   • Defer the clear until the scroll settles — the flight then runs
  //     through the empty band, and the previous turn looks unmounted until
  //     the settle removes the spacing and it reappears.
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
  let smoothScrollActive = false
  let cancelSmoothScroll: (() => void) | null = null
  let cancelTurnEntrance: (() => void) | null = null
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
    if (!el) return root.scrollTop
    // Measure layout, not the latest turn's temporary visual translation.
    const motion = el.closest<HTMLElement>('[data-turn-motion]')
    const transform = motion && getComputedStyle(motion).transform
    const entranceY = transform && transform !== 'none' ? new DOMMatrixReadOnly(transform).m42 : 0
    return getElementAbsoluteTop(el, root) - entranceY - PIN_TOP_OFFSET_PX
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
  function pinAfterSend(): () => void {
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
    pinAnchorId = lastUserMessage()?.id ?? null
    // Arm only — do NOT clear / set reserves here.
    //
    // sendMessage pushes the optimistic user turn only after several awaits
    // (command parse, session setup, …). Anything we mutate now paints one
    // Vue flush BEFORE that turn exists: a positional or "clear previous
    // blank now" edit either hits the wrong container or shrinks scrollHeight
    // under a bottom-parked viewport (zero-frame jerk). The full handover
    // (collapseReserveKeepingView → new min-height → latest-turn translation) runs in
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
        cancelSmoothScroll?.()
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

  function startSmoothScroll(root: HTMLElement, getTarget: () => number) {
    cancelSmoothScroll?.()
    let cancelled = false
    smoothScrollActive = true
    isProgrammaticScroll = true
    const release = () => {
      smoothScrollActive = false
      pinScrollActive = false
      isProgrammaticScroll = false
      root.removeEventListener('wheel', interrupt)
      root.removeEventListener('touchstart', interrupt)
      root.removeEventListener('pointerdown', interrupt)
      window.removeEventListener('keydown', cancelOnKey)
      lastScrollTop = root.scrollTop
      // The stream may have grown while the browser was scrolling to its
      // initial target. Catch up only after natural completion, never on cancel.
      if (!cancelled && followEnabled && isActive.value && !lockScroll.value
        && scrollEl.value === root && !isNearBottom(root)) {
        stickToBottomNow()
      }
      scheduleAtBottomRefresh()
    }
    const cancel = () => {
      cancelled = true
      stop()
      cancelTurnEntrance?.()
      cancelSmoothScroll = null
    }
    const interrupt = () => {
      markEscaped()
      cancel()
    }
    const cancelOnKey = (event: KeyboardEvent) => {
      if (KEY_NAV.has(event.key) && !(event.target instanceof HTMLElement && (
        event.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(event.target.tagName)
      ))) interrupt()
    }
    root.addEventListener('wheel', interrupt, { passive: true })
    root.addEventListener('touchstart', interrupt, { passive: true })
    root.addEventListener('pointerdown', interrupt, { passive: true })
    window.addEventListener('keydown', cancelOnKey)
    const stop = nativeScrollTo(root, getTarget, release)
    // Retain cancellation after scrolling ends: the turn's separate animation
    // may still be running when a new send or navigation arrives.
    cancelSmoothScroll = cancel
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

  // Resolve the final layout before starting the latest-turn transform.
  // R = viewport height - column bottom padding - pin offset + prompt offset.
  // Long prompts retain at least a third of a viewport below their tail.
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
    pinPending = false
    // A pinned turn is a parked view — follow re-engages only when the user
    // scrolls back into the content-end band. (pinAfterSend already parked;
    // re-assert against anything that re-armed follow in between.)
    followEnabled = false

    // Clear the previous reserve before measuring the new one; never animate
    // through two turns' unused reply room or clear spacing at animation end.
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
    // scrollHeight has a viewport-sized minimum, so it includes empty viewport
    // space on the first turn. The last turn's column owns the composer padding.
    const below = Number.parseFloat(getComputedStyle(container.parentElement ?? el).paddingBottom) || 0
    const ideal = el.clientHeight - below - PIN_TOP_OFFSET_PX + promptOffsetInTurn
    const floor = promptOffsetInTurn + promptEl.offsetHeight + Math.round(el.clientHeight / 3)
    const reservePx = Math.max(0, Math.round(Math.max(ideal, floor)))
    turnReserves.value = new Map(turnReserves.value).set(prompt.id, reservePx)
    appliedPinAttemptId = pinAttemptId
    // Immediate projection of the same value: the reactive binding lands on
    // Vue's next flush, but the scroll below needs this frame's geometry.
    // The binding renders the identical value and owns it from the next
    // patch on — including across every future remount.
    container.style.minHeight = `${reservePx}px`
    lastScrollTop = el.scrollTop

    // Resolve the live prompt, including its offset inside a replacement turn.
    // pinnedTurnId follows optimistic → persisted identity migration.
    const pinTarget = () => messageJumpTarget(el, pinnedTurnId ?? prompt.id)
    const target = Math.min(Math.max(pinTarget(), 0), Math.max(0, bottomTarget(el)))
    const promptTop = containerTop + promptOffsetInTurn - target
    // The viewport already covers the travel distance; keep the turn entrance local.
    const fromY = Math.max(0, Math.min(
      TURN_ENTRANCE_MAX_DISTANCE_PX,
      el.clientHeight - below - promptEl.offsetHeight - promptTop,
      container.offsetHeight - promptEl.offsetHeight,
    ))
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
      cancelSmoothScroll?.()
      el.scrollTop = target
      isProgrammaticScroll = false
      scheduleAtBottomRefresh()
    } else {
      startSmoothScroll(el, pinTarget)
      pinScrollActive = true
      startTurnEntrance(el, container, fromY)
    }
    return true
  }

  function startTurnEntrance(root: HTMLElement, container: HTMLElement, fromY: number) {
    const finish = () => {
      root.removeEventListener('pointerdown', cancel)
      root.removeEventListener('wheel', cancel)
      root.removeEventListener('touchstart', cancel)
      window.removeEventListener('keydown', cancelOnKey)
      cancelTurnEntrance = null
    }
    const cancel = () => { cancelSmoothScroll?.() }
    const cancelOnKey = (event: KeyboardEvent) => {
      if (KEY_NAV.has(event.key) && !(event.target instanceof HTMLElement && (
        event.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(event.target.tagName)
      ))) cancel()
    }
    root.addEventListener('pointerdown', cancel, { passive: true })
    window.addEventListener('keydown', cancelOnKey)
    root.addEventListener('wheel', cancel, { passive: true })
    root.addEventListener('touchstart', cancel, { passive: true })
    // Entrance completion must not clear an ongoing native scroll's guard.
    let finished = false
    const stop = animateTurnEntrance(container, fromY, () => {
      finished = true
      finish()
    })
    if (!finished) cancelTurnEntrance = stop
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
    // A content update must not replace an in-flight native smooth scroll.
    if (!el || smoothScrollActive) return
    isProgrammaticScroll = true
    el.scrollTo({ top: bottomTarget(el), behavior: 'auto' })
    requestAnimationFrame(() => {
      if (smoothScrollActive) return
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
    // Streaming moves the bottom continuously. Let this flight finish before
    // release catches up and resumes follow; message jumps use live targets.
    const target = bottomTarget(root)
    startSmoothScroll(root, () => target)
  }

  // The persistent per-turn container that holds a message — the element the
  // reserve :style binds to. chat-pane renders one div per turn keyed by the
  // opening message id; the message row lives inside it.
  function turnContainerOf(turnId: string): HTMLElement | null {
    const msgEl = findMessageElement(turnId)
    return msgEl?.closest<HTMLElement>('[data-chat-turn]') ?? msgEl?.parentElement ?? null
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
    startSmoothScroll(root, () => messageJumpTarget(root, messageId))
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
  watch(sessionId, (next, prev) => {
    // The panel survives draft promotion. Preserve both an armed send and an
    // already-running entrance if its session parameter catches up after render.
    const wasDraft = !prev || prev.startsWith('draft:')
    const isSession = !!next && !next.startsWith('draft:')
    if (wasDraft && isSession && (pinPending || pinnedTurnId)) return
    cancelSmoothScroll?.()
    elId.clear()
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
    cancelSmoothScroll?.()
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
      // "relock shortly after a downward pause" timer: it would re-arm follow
      // on any small downward nudge while a turn is parked, and the next
      // streamed token would yank the parked view to the bottom.
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
    if (old) cancelSmoothScroll?.()
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
  // layout settles that follow it. No manual scrollTop compensation: a one-shot
  // adjustment cannot track the async reflow (Shiki, KaTeX, images, fonts) that
  // keeps resizing rows after the DOM lands — it was tried, and each prepend
  // batch twitched while the pin drifted off its offset. Never set
  // `overflow-anchor: none` on the viewport either.
  function suppressAutoScrollForPrepend() {
    markEscaped()
  }

  onBeforeUnmount(() => {
    cancelSmoothScroll?.()
    if (atBottomRefreshRaf) cancelAnimationFrame(atBottomRefreshRaf)
    if (highlightTimer) clearTimeout(highlightTimer)
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

    // lifecycle hooks — call sites live in chat-pane.vue's own onActivated/onDeactivated
    onActivatedRestoreScroll,
    onDeactivatedResetScroll,

    // message-item @active contract
    onMessageActive,

    // per-turn reserve projection — chat-pane binds :style="turnReserveStyle(turn.id)"
    turnReserveStyle,

    // low-level primitives kept public for the scroll rail (the rail's own
    // trigger logic still calls these directly)
    startSmoothScroll,
    findMessageElement,
    getElementAbsoluteTop,
    messageJumpTarget,
  }
}
