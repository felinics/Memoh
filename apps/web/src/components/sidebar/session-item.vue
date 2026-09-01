<template>
  <ContextMenu>
    <ContextMenuTrigger as-child>
      <!-- active: mirrors the hover fill so a touch press (incl. the hold that
           opens the context menu) gives visible feedback — touch has no hover,
           and without this the long-press felt dead until the menu appeared.

           data-menu-open is the ROW-EMPHASIZED contract: while the dropdown is
           open the cursor is over the portaled menu, so :hover is gone from
           the row — everything that keyed off hover (row fill, actions slot,
           marquee scroll) must also key off this attribute, or the row
           visually collapses the moment the pointer moves onto the menu. -->
      <div
        ref="rowEl"
        role="button"
        tabindex="0"
        class="group relative flex items-center min-h-[2.125rem] w-full rounded-[9px] px-[11px] text-left transition-colors cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        :class="rowClass"
        :data-ui-selected="isActive ? '' : undefined"
        :data-menu-open="menuOpen || undefined"
        :title="hoverTitle"
        @mouseenter="syncHovered"
        @mouseleave="syncHovered"
        @click="$emit('select', session)"
        @keydown.enter.prevent="$emit('select', session)"
        @keydown.space.prevent="$emit('select', session)"
      >
        <!-- Native session rows stay text-only. ACP rows carry the agent icon
             and schedule runs a yellow clock, because the unified Recents list
             mixes model chats, external-agent chats, and schedule runs. -->
        <span
          v-if="isACPSession"
          class="mr-2 flex size-4 shrink-0 items-center justify-center text-muted-foreground"
          role="img"
          :aria-label="acpAgentLabel"
        >
          <component
            :is="acpAgentIcon(acpAgentId, true)"
            class="size-4"
            aria-hidden="true"
          />
        </span>
        <span
          v-else-if="isScheduleSession"
          class="mr-2 flex size-4 shrink-0 items-center justify-center"
          role="img"
          :aria-label="t('chat.activityBar.schedule')"
        >
          <!-- Status icons stay state-constant on the accent ramp. -->
          <Clock
            class="size-4 text-[color:var(--accent-yellow)]"
            aria-hidden="true"
          />
        </span>

        <!-- Title runs are split CJK vs Latin so the title renders with the SAME
             per-script treatment as the chat body (.sidebar-cjk / .sidebar-latin reuse
             the --chat-*-body weight + Latin size/tracking). The only thing dropped vs
             the body is streaming — a title is a static one-line label. -->
        <MarqueeText class="flex-1 min-w-0 text-control text-foreground dark:text-[color:oklch(0.92_0_0)]">
          <span
            v-for="(run, i) in titleRuns"
            :key="i"
            :class="run.script === 'cjk' ? 'sidebar-cjk' : 'sidebar-latin'"
          >{{ run.text }}</span>
        </MarqueeText>

        <!-- Trailing slot: spinner and the actions button share one 24px right
             slot so they sit at the same center and never both show at once. The
             slot reserves its width IN FLOW whenever anything can be visible in
             it — streaming or row emphasis (hover, or the dropdown being open —
             keyed off data-menu-open on the row so the slot stays reserved while
             the cursor is over the portaled menu) — so the title's display area
             always ends before the icon zone instead of letting the icon float
             over the text. Keyboard focus alone does NOT widen it (deliberate):
             closing the dropdown refocuses its trigger inside the row, and a
             focus-keyed rule would pin the slot open after an Esc close,
             over-truncating the title. Idle rows keep w-0 (no dead gutter); the
             width transition lets the title re-truncate smoothly as the slot
             grows, and the actions button fades in over the same beat as the
             spinner fades out. The button is ABSOLUTELY anchored to the slot's
             right edge, never an in-flow flex child: a shrink-0 button beside
             the 24px spinner out-competes it for flex width inside the fixed
             w-6 slot and crushes the spinner's box to zero, off-centering the
             loader. Sole in-flow child while streaming, the spinner keeps its
             full box and center. -->
        <div
          class="relative ml-1.5 flex h-6 shrink-0 items-center justify-end transition-[width] duration-150"
          :class="streaming || menuOpen ? 'w-6' : 'w-0 group-hover:w-6 group-data-[menu-open=true]:w-6'"
        >
          <div
            v-if="streaming"
            class="flex h-6 w-6 items-center justify-center transition-opacity duration-150 group-hover:opacity-0"
            :class="menuOpen ? 'opacity-0' : 'opacity-100'"
          >
            <LoaderCircle
              class="size-3 animate-spin text-muted-foreground"
              :aria-label="t('chat.sessionStreaming')"
            />
          </div>

          <DropdownMenu v-model:open="menuOpen">
            <DropdownMenuTrigger as-child>
              <!-- Plain button (not <Button variant="ghost">): the ghost chip color
                   (--btn-ghost-hover) is the same gray as the row's own hover, so the
                   button's hover was invisible while sitting on a hovered row. A
                   translucent foreground mix darkens whatever is behind it, so the
                   chip reads clearly on top of both the hover and active row fills. -->
              <button
                type="button"
                class="absolute inset-y-0 right-0 my-auto inline-flex size-6 cursor-pointer items-center justify-center rounded-md text-muted-foreground outline-none transition-[opacity,background-color,color] duration-150 hover:bg-[color-mix(in_oklab,var(--foreground)_12%,transparent)] hover:text-foreground focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring data-[state=open]:bg-[color-mix(in_oklab,var(--foreground)_12%,transparent)] data-[state=open]:text-foreground"
                :class="menuOpen ? 'opacity-100' : 'opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto group-data-[menu-open=true]:opacity-100 group-data-[menu-open=true]:pointer-events-auto'"
                :aria-label="t('chat.sessionActions')"
                @click.stop
                @keydown.enter.stop
                @keydown.space.stop
              >
                <MoreHorizontal class="size-4" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              @click.stop
            >
              <DropdownMenuItem
                @select="$emit('rename', session)"
              >
                <Pencil class="mr-2 size-3.5" />
                {{ t('common.rename') }}
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                @select="$emit('delete', session)"
              >
                <Trash2 class="mr-2 size-3.5" />
                {{ t('common.delete') }}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </ContextMenuTrigger>
    <!-- Right-click menu: Open opens the session as its own pinned tab (a single
         click reuses the ephemeral preview slot; an explicit right-click means
         "give me a separate tab"). Rename/Delete reuse the same emits as the
         hover three-dot button so both affordances stay in sync. -->
    <ContextMenuContent>
      <ContextMenuItem
        :disabled="isActive"
        @select="$emit('openNewTab', session)"
      >
        <MessageSquare class="mr-2 size-3.5" />
        {{ t('common.open') }}
      </ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem @select="$emit('rename', session)">
        <Pencil class="mr-2 size-3.5" />
        {{ t('common.rename') }}
      </ContextMenuItem>
      <ContextMenuItem
        variant="destructive"
        @select="$emit('delete', session)"
      >
        <Trash2 class="mr-2 size-3.5" />
        {{ t('common.delete') }}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { Clock, LoaderCircle, MessageSquare, MoreHorizontal, Pencil, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { SessionSummary } from '@/composables/api/useChat'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@felinic/ui'
import { acpAgentDisplayName, acpAgentIcon, normalizeACPAgentID } from '@/utils/acp'
import { splitScriptRuns } from '@/utils/script-runs'
import { normalizedRuntimeType, normalizedSessionMode, routeConversationLabel } from '@/store/chat-list.utils'
import MarqueeText from './marquee-text.vue'

const props = defineProps<{
  session: SessionSummary
  isActive: boolean
  streaming?: boolean
}>()

defineEmits<{
  select: [session: SessionSummary]
  openNewTab: [session: SessionSummary]
  rename: [session: SessionSummary]
  delete: [session: SessionSummary]
}>()

const { t } = useI18n()

const menuOpen = ref(false)
const rowEl = ref<HTMLElement>()
// Pure-CSS :hover re-read as state, so the rowEmphasized computed can consume
// it (a computed can't match selectors directly). Note the dual channel: only
// the row FILL flows through this JS predicate; the slot width, actions button
// and marquee trigger key straight off CSS .group:hover. The channels diverge
// transiently when the row slides under a STATIONARY cursor (list reorder,
// folder expand/collapse): no mouseenter fires, so the fill lags one cursor
// move while the CSS effects apply immediately — cosmetic, self-heals.
const hovered = ref(false)
const syncHovered = () => { hovered.value = rowEl.value?.matches(':hover') ?? false }

// Row emphasis = hover OR open dropdown, as ONE predicate feeding the row fill
// class, the data-menu-open attribute, and (via it) the trailing slot width and
// the marquee's scroll trigger. One source of truth means the fill, the 24px
// slot, and the scrolling can never disagree about which state the row is in —
// previously three independent CSS :hover rules did, and each disagreement was
// a separate bug report ("slot didn't restore"; "row collapsed while the menu
// was open" — with the dropdown open the cursor sits over the PORTALED menu,
// so :hover is gone from the row and hover-keyed effects snapped off mid-read).
// Active rows are excluded: they already carry the stronger selected fill, and
// layering hover gray on it reads as a flicker.
const rowEmphasized = computed(() => !props.isActive && (hovered.value || menuOpen.value))

const rowClass = computed(() => {
  if (props.isActive) return ''
  // :active fill is a SEPARATE contract from rowEmphasized, kept from the
  // pre-marquee hover:/active: pair: it gives touch users press feedback
  // (incl. the long-press that opens the context menu) where :hover never
  // fires. It must not widen the slot or start the marquee — pressing is
  // not emphasis — so it stays out of the predicate on purpose.
  return [
    'active:bg-[color:var(--sidebar-hover)]',
    rowEmphasized.value ? 'bg-[color:var(--sidebar-hover)]' : '',
  ]
})

function routeMeta(): Record<string, unknown> {
  return props.session.route_metadata ?? {}
}

// Group title for group sessions, peer name for DMs — the default visible
// label for untitled channel sessions, and the IM handle for the hover
// tooltip. Empty title is expected: only the chat turn path generates titles
// server-side; discuss (group) sessions never get one (issue #1043).
const displayLabel = computed(() => routeConversationLabel(props.session))

const titleRuns = computed(() =>
  splitScriptRuns((props.session.title ?? '').trim() || displayLabel.value || t('chat.untitledSession')),
)

const WEB_CHANNELS = new Set(['local', ''])

const isIMSession = computed(() => {
  const ct = (props.session.channel_type ?? '').trim().toLowerCase()
  return ct !== '' && !WEB_CHANNELS.has(ct)
})

const acpAgentId = computed(() => normalizeACPAgentID(
  props.session.runtime_metadata?.acp_agent_id ?? props.session.metadata?.acp_agent_id,
))
const isACPSession = computed(() => normalizedRuntimeType(props.session) === 'acp_agent')
const isScheduleSession = computed(() => normalizedSessionMode(props.session) === 'schedule')
const acpAgentLabel = computed(() => acpAgentDisplayName(acpAgentId.value, t('chat.sessionTypeACPAgent')))

// The old two-line subLabel is folded into the native tooltip: channel handle
// for IM sessions, agent name for ACP sessions.
const hoverTitle = computed(() => {
  const title = (props.session.title ?? '').trim() || displayLabel.value || t('chat.untitledSession')
  if (isACPSession.value) {
    return `${title} — ${acpAgentLabel.value}`
  }
  if (!isIMSession.value) return title
  const meta = routeMeta()
  const handle = (meta.conversation_handle as string ?? '').trim()
    || (meta.sender_username as string ?? '').trim()
    || displayLabel.value
  return handle ? `${title} — @${handle.replace(/^@/, '')}` : title
})
</script>
