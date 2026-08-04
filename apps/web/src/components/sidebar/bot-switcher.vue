<template>
  <DropdownMenu v-model:open="menuOpen">
    <DropdownMenuTrigger as-child>
      <!-- Workspace switcher. Two shapes by platform:
           · mac desktop (chip): hugs avatar + name, never full-width — leaves the
             traffic-light gutter to its left uncluttered; chevron always shown.
           · web / Windows (full-width): a stadium hover bar whose right edge lines
             up with the search button's hover below. Avatar + name stay LEFT; the
             chevron is pinned far-right and only fades in on hover/open (it's an
             affordance, not chrome at rest). Web/Win have no traffic lights, so the
             switcher reads as a normal full-width nav control.
           The avatar carries a hairline ring (only this one) to stay crisp on the
           hover fill. No color transition: the fill snaps on hover/open. -->
      <button
        type="button"
        class="group flex h-8 items-center translate-y-[-2px] text-xs text-foreground outline-none hover:bg-[color:var(--sidebar-hover)] focus-visible:ring-2 focus-visible:ring-ring data-[state=open]:bg-[color:var(--sidebar-hover)] [-webkit-app-region:no-drag]"
        :class="fullWidth
          ? 'w-[calc(100%+3px)] -ml-[3px] gap-1.5 rounded-full pl-2 pr-2.5'
          : 'max-w-full gap-2 rounded-md px-1.5'"
        :aria-label="t('sidebar.switchBot')"
      >
        <Avatar
          class="shrink-0 ring-1 ring-[color-mix(in_oklab,var(--foreground)_10%,transparent)]"
          :class="fullWidth ? 'size-[22px]' : 'size-5'"
        >
          <AvatarImage
            v-if="currentBot?.avatar_url"
            :src="currentBot.avatar_url"
            :alt="currentLabel"
          />
          <AvatarFallback class="text-[9px]">
            {{ avatarFallback }}
          </AvatarFallback>
        </Avatar>
        <span
          class="min-w-0 truncate text-[13px] font-[550] tracking-[0.015em] text-foreground dark:text-[color:oklch(0.97_0_0)]"
          :class="fullWidth && 'flex-1 text-left'"
        >
          {{ currentLabel }}
        </span>
        <ChevronsUpDown
          class="size-3 shrink-0 text-muted-foreground"
          :class="fullWidth && 'ml-auto opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-data-[state=open]:opacity-100'"
        />
      </button>
    </DropdownMenuTrigger>
    <!-- chip mode pulls the panel left (align-offset) so the dropdown bot avatars
         line up under the trigger avatar (trigger px-1.5 = 6px vs menu p-1.5 + item
         px-2.5 = 16px → -10px). Full-width mode is a plain dropdown: left-aligned,
         growing rightward — no left pull, which would shove it against the window
         edge with too little left margin. -->
    <DropdownMenuContent
      align="start"
      side="bottom"
      :align-offset="fullWidth ? 0 : -10"
      class="w-60"
    >
      <DropdownMenuLabel class="text-xs font-medium text-muted-foreground">
        {{ t('sidebar.bots') }}
      </DropdownMenuLabel>

      <!-- Bot rows: drag-to-reorder via sortablejs with forceFallback, so there's
           no native OS drag ghost. Sortable floats its OWN clone, which keeps the
           hover fill at full content opacity (see .bot-row-drag) and tracks the
           cursor 1:1 — continuous/finger-following, not snapping slot-by-slot — while
           the live rows slide to make room (animation) and the source slot shows
           as an empty gap (.bot-row-ghost). The WHOLE row is the drag target (no
           `handle`), so you can grab it anywhere the cursor reads as interactive,
           not just the avatar; a move past the 5px `fallbackTolerance` starts a
           drag, a plain click (even with slight hand jitter) still selects.
           The grip that swaps in on hover is a pure affordance (no
           separate cursor, so moving across the row never flickers grab↔pointer).
           While a drag runs we (a) freeze the rendered list off `displayBots` so
           an unrelated query refetch can't re-render mid-drag and yank the row,
           and (b) lock the cursor to grabbing globally (body.bot-dragging). Order
           persists to localStorage. -->
      <div ref="listRef">
        <div
          v-for="bot in displayBots"
          :key="bot.id"
          role="menuitem"
          class="bot-row group relative flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-control transition-colors"
          :class="bot.status === 'error'
            ? 'bot-row-disabled opacity-40'
            : 'cursor-pointer hover:bg-[color:var(--bot-row-tint)]'"
          @click="handleSelect(bot)"
        >
          <span class="relative size-5 shrink-0">
            <Avatar class="bot-row-avatar size-5 transition-opacity group-hover:opacity-0">
              <AvatarImage
                v-if="bot.avatar_url"
                :src="bot.avatar_url"
                :alt="bot.display_name || bot.id"
              />
              <AvatarFallback class="text-[9px]">
                {{ initialsOf(bot) }}
              </AvatarFallback>
            </Avatar>
            <span class="bot-drag-handle absolute inset-0 flex items-center justify-center text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100">
              <GripVertical class="size-4" />
            </span>
          </span>
          <span class="min-w-0 flex-1 truncate">
            {{ bot.display_name || bot.id }}
          </span>
          <Check
            v-if="bot.id === currentBotId"
            class="size-3.5 shrink-0 text-muted-foreground"
          />
        </div>
      </div>

      <div
        v-if="isLoading"
        class="flex justify-center py-3"
      >
        <Spinner class="size-3.5" />
      </div>
      <div
        v-if="!isLoading && bots.length === 0"
        class="px-2 py-3 text-center text-xs text-muted-foreground"
      >
        {{ t('bots.emptyTitle') }}
      </div>

      <DropdownMenuSeparator />
      <DropdownMenuItem
        class="gap-2"
        @select="router.push({ name: 'bot-new' })"
      >
        <Plus class="size-4 text-muted-foreground" />
        <span class="text-control">{{ t('bots.createBot') }}</span>
      </DropdownMenuItem>
      <DropdownMenuItem
        class="gap-2"
        @select="router.push({ name: 'bots' })"
      >
        <Settings2 class="size-4 text-muted-foreground" />
        <span class="text-control">{{ t('sidebar.manageBots') }}</span>
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useLocalStorage } from '@vueuse/core'
import Sortable from 'sortablejs'
import { useQuery } from '@pinia/colada'
import { getBotsQuery } from '@memohai/sdk/colada'
import type { BotsBot } from '@memohai/sdk'
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  Spinner,
} from '@felinic/ui'
import { Check, ChevronsUpDown, GripVertical, Plus, Settings2 } from 'lucide-vue-next'
import { useChatStore } from '@/store/chat-list'
import { usePinnedBots } from '@/composables/usePinnedBots'

const router = useRouter()
const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)
const { sortBots } = usePinnedBots()

// Full-width shape for web / Windows (no traffic lights); mac desktop passes
// false to keep the compact floating chip beside the traffic-light gutter.
withDefaults(defineProps<{ fullWidth?: boolean }>(), { fullWidth: false })

const menuOpen = ref(false)

const { data: botData, isLoading } = useQuery(getBotsQuery())

// Manual drag order persisted to localStorage; bots not present in the saved
// order keep their default (pinned) order at the end.
const botOrder = useLocalStorage<string[]>('bot-order', [])
const bots = computed<BotsBot[]>(() => {
  const base = sortBots(botData.value?.items ?? [])
  if (botOrder.value.length === 0) return base
  const rank = new Map(botOrder.value.map((id, index) => [id, index]))
  return [...base].sort((a, b) =>
    (rank.get(a.id ?? '') ?? Number.MAX_SAFE_INTEGER)
    - (rank.get(b.id ?? '') ?? Number.MAX_SAFE_INTEGER),
  )
})

const currentBot = computed(() =>
  bots.value.find((bot) => bot.id === currentBotId.value) ?? null,
)
const currentLabel = computed(() =>
  currentBot.value
    ? currentBot.value.display_name || currentBot.value.id || ''
    : t('chat.selectBot'),
)

function initialsOf(bot: BotsBot): string {
  const name = (bot.display_name || bot.id || '').trim()
  const initials = name
    .split(/[\s_-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map(word => word[0])
    .join('')
    .toUpperCase()
  return initials || 'B'
}

const avatarFallback = computed(() =>
  currentBot.value ? initialsOf(currentBot.value) : 'B',
)

function handleSelect(bot: BotsBot) {
  if (bot.status === 'error') return
  const id = bot.id ?? ''
  if (!id) return
  menuOpen.value = false
  if (id === currentBotId.value) return
  void chatStore.selectBot(id)
  void router.push({ name: 'bot', params: { botName: bot.name ?? id } })
}

// ---- drag-to-reorder (sortablejs) ----
// The teleported menu only mounts while open, so the Sortable instance is created
// on open and torn down on close.
//
// The list renders off `displayBots`, a snapshot of `bots`, instead of `bots`
// directly. While a drag is in flight we stop syncing it, so a background query
// refetch (which mints a NEW `bots` array) can't re-render the v-for under
// Sortable's feet — that mid-drag re-render was the "starts sliding then snaps
// across" twitch. On drop we revert Sortable's physical DOM move, then write the
// new order to BOTH `displayBots` (one clean keyed move) and `botOrder`
// (persistence), so Vue stays the single source of truth and never double-moves.
const listRef = ref<HTMLElement | null>(null)
let sortable: Sortable | null = null
let isDragging = false
const displayBots = ref<BotsBot[]>([])
watch(bots, (next) => {
  if (!isDragging) displayBots.value = next
}, { immediate: true })

// Lock the drag to the VERTICAL axis. Sortable's fallback clone follows the
// cursor on both axes (it writes its position as an inline `matrix(a,b,c,d,e,f)`
// transform, where e = horizontal translate and f = vertical), but a bot list is
// a single column, so horizontal drift just looks wrong. SortableJS has no
// built-in axis lock for the visual ghost, so each frame we read that matrix and
// force e to 0 while keeping f. rAF runs after Sortable's pointer handler and
// before paint, so the user only ever sees the vertical-only position; Sortable
// keeps deriving the drop slot from the pointer, so reordering is unaffected.
let axisLockRaf = 0
function lockGhostAxis() {
  axisLockRaf = requestAnimationFrame(lockGhostAxis)
  const ghost = document.querySelector<HTMLElement>('.bot-row-drag')
  if (!ghost) return
  const t = ghost.style.transform
  if (!t || t === 'none') return
  let m: DOMMatrixReadOnly
  try {
    m = new DOMMatrixReadOnly(t)
  } catch {
    return
  }
  if (m.e === 0) return
  ghost.style.transform = `matrix(${m.a}, ${m.b}, ${m.c}, ${m.d}, 0, ${m.f})`
}
function stopAxisLock() {
  if (axisLockRaf) {
    cancelAnimationFrame(axisLockRaf)
    axisLockRaf = 0
  }
}

// Post-drop continuity: hover is KEPT, not suppressed. The pointer lands on the
// dropped row, which must come back ALREADY hovered — the clone was showing the
// fill + grip, so the row has to snap into that exact look (one continuous
// object settling in place). bot-drag-settle disables the rows' transitions for
// a beat so the :hover state applies instantly instead of replaying its fade-in
// (which read as a flash); normal animated hover resumes once the window ends —
// re-enabling transitions on an already-applied state animates nothing.
let settleTimer = 0
function startSettle() {
  document.body.classList.add('bot-drag-settle')
  window.clearTimeout(settleTimer)
  settleTimer = window.setTimeout(() => {
    settleTimer = 0
    document.body.classList.remove('bot-drag-settle')
  }, 200)
}
function clearSettle() {
  if (settleTimer) {
    window.clearTimeout(settleTimer)
    settleTimer = 0
  }
  document.body.classList.remove('bot-drag-settle')
}

watch(menuOpen, async (open) => {
  if (open) {
    // Re-sync in case bots changed while the menu was closed (the watcher above
    // keeps it current, but be explicit so the first paint is never stale).
    if (!isDragging) displayBots.value = bots.value
    await nextTick()
    if (listRef.value && !sortable) {
      sortable = Sortable.create(listRef.value, {
        animation: 180,
        easing: 'cubic-bezier(0.32, 0.72, 0, 1)',
        // Whole row is draggable (no `handle`); .bot-row-disabled can't be moved.
        draggable: '.bot-row',
        filter: '.bot-row-disabled',
        // No native HTML5 DnD (and its OS drag ghost): Sortable floats its own
        // clone on <body> and reorders the live rows with the animation above.
        forceFallback: true,
        fallbackOnBody: true,
        fallbackClass: 'bot-row-drag',
        // A drag only arms after the pointer travels 5px from pointerdown
        // (default 0 = the FIRST pointermove of any distance starts one).
        // Sortable swallows the next click in capture phase once a drag has
        // started (its #1184 fix), so with zero tolerance a sub-pixel jitter
        // during a click — constant on a Magic Mouse — silently eats the
        // select click: the menu stays open and nothing happens. 5px matches
        // the native OS drag threshold; the clone tracks the cursor 1:1, so a
        // real drag still feels instant, and the post-drop click stays
        // suppressed (drop ≠ select) — aside from a rare pre-existing upstream
        // race where a >50ms drag whose emulation tick lands on a sibling row
        // releases the suppression early (sortablejs #1184 handling in
        // _onDragOver); this tolerance changes nothing about that either way.
        fallbackTolerance: 5,
        ghostClass: 'bot-row-ghost',
        onStart: () => {
          isDragging = true
          clearSettle()
          document.body.classList.add('bot-dragging')
          lockGhostAxis()
        },
        onEnd: (evt) => {
          isDragging = false
          stopAxisLock()
          document.body.classList.remove('bot-dragging')
          startSettle()
          const { oldIndex, newIndex, item, from } = evt
          if (oldIndex == null || newIndex == null || oldIndex === newIndex) {
            // Re-sync to absorb anything skipped during the drag.
            displayBots.value = bots.value
            return
          }
          // Undo Sortable's physical move so the live DOM matches `displayBots`
          // again, then let Vue apply the single keyed move from the data change.
          item.remove()
          from.insertBefore(item, from.children[oldIndex] ?? null)
          const next = [...displayBots.value]
          const [moved] = next.splice(oldIndex, 1)
          next.splice(newIndex, 0, moved)
          displayBots.value = next
          botOrder.value = next.map(bot => bot.id ?? '').filter(Boolean)
        },
      })
    }
  }
  else {
    sortable?.destroy()
    sortable = null
  }
})

onBeforeUnmount(() => {
  sortable?.destroy()
  sortable = null
  stopAxisLock()
  clearSettle()
  document.body.classList.remove('bot-dragging')
})
</script>

<style>
/* sortablejs drag states. The fallback clone (.bot-row-drag) is appended to
 * <body>, outside this component, so these rules are intentionally global.
 *
 * The layering model: the fill is the BOTTOM layer, every row's content (text,
 * icons, checkmark) is the TOP layer. Hover and drag share ONE fill color so it
 * never changes from hover → grab → drop. That fill is a translucent ink
 * (foreground at 8% alpha) instead of the opaque --ui-selected: over the bare
 * popover it composites to the same ≈ --ui-selected gray in both themes, and
 * while the clone floats over other rows their content stays readable through
 * it — fill below, content above. The clone itself is the row DOM, so it
 * carries .bot-row and resolves the same var. */
.bot-row {
  --bot-row-tint: color-mix(in oklab, var(--foreground) 8%, transparent);
}
.bot-row-drag {
  /* Sortable inlines opacity: 0.8 on the clone; the grabbed row's text and
   * icons must NOT dim, so out-important it. */
  opacity: 1 !important;
  background-color: var(--bot-row-tint) !important;
  box-shadow: none !important;
}
/* The clone is built from the row DOM, but it isn't :hovered (pointer-events are
 * off), so its group-hover crossfade would default back to the avatar. Force the
 * grip / hide the avatar so the grabbed row matches the hover state the cursor
 * was just in — no avatar↔grip flip on pickup. */
.bot-row-drag .bot-row-avatar {
  opacity: 0 !important;
}
.bot-row-drag .bot-drag-handle {
  opacity: 1 !important;
}
/* The slot the dragged row will land in: an empty gap. Hidden so there's only
 * ONE visible item (the clone under the finger) — never a second copy. The other
 * rows sliding to make room (Sortable's `animation`) is what shows the target. */
.bot-row-ghost {
  opacity: 0;
}
/* Lock the cursor to grabbing for the whole drag so it doesn't flicker as the
 * pointer passes over rows (pointer). Toggled on <body> by onStart/onEnd. */
body.bot-dragging,
body.bot-dragging * {
  cursor: grabbing !important;
}

/* During a drag the pointer sweeps across every row, and each row it passes
 * would otherwise (a) flip its avatar→grip crossfade and (b) flash its hover
 * fill. Both read as the row "twitching". Freeze the resting rows: avatar stays,
 * grip hidden, and no hover fill (the empty source gap is excluded since it's
 * already invisible). */
body.bot-dragging .bot-row:not(.bot-row-drag) .bot-row-avatar {
  opacity: 1 !important;
}
body.bot-dragging .bot-row:not(.bot-row-drag) .bot-drag-handle {
  opacity: 0 !important;
}
body.bot-dragging .bot-row:not(.bot-row-drag):not(.bot-row-ghost):hover {
  background-color: transparent !important;
}

/* Post-drop: hover is KEPT, not suppressed. The pointer lands on the dropped
 * row, which must come back already hovered — the clone was showing fill +
 * grip, so the row snapping into that same look reads as one continuous object
 * settling in place. Transitions are disabled for a beat so the :hover state
 * applies instantly instead of replaying its fade-ins (the flash); normal
 * animated hover resumes when the class drops. */
body.bot-drag-settle .bot-row,
body.bot-drag-settle .bot-row * {
  transition: none !important;
}
</style>
