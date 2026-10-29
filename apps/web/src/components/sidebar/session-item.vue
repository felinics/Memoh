<template>
  <ContextMenu>
    <!-- While selecting, a row only toggles its checkbox; the menus are what
         entered the mode, so they stay out of the way until it ends. -->
    <ContextMenuTrigger
      as-child
      :disabled="selecting"
    >
      <!-- active: mirrors the hover fill so a touch press (incl. the hold that
           opens the context menu) gives visible feedback — touch has no hover,
           and without this the long-press felt dead until the menu appeared.

           data-menu-open is the ROW-EMPHASIZED contract: while the dropdown is
           open the cursor is over the portaled menu, so :hover is gone from
           the row — everything that keyed off hover (row fill, actions button,
           marquee scroll) must also key off this attribute, or the row
           visually collapses the moment the pointer moves onto the menu. -->
      <div
        data-slot="sidebar-session-row"
        :role="selecting ? 'checkbox' : 'button'"
        :aria-checked="selecting ? selected : undefined"
        tabindex="0"
        class="group relative flex items-center min-h-[2.125rem] w-full rounded-[9px] px-[11px] text-left cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        :data-ui-selected="isActive ? '' : undefined"
        :data-menu-open="menuOpen || undefined"
        :data-streaming="streaming || unread.isUnread(session.id) || undefined"
        :data-unread="unread.isUnread(session.id) || undefined"
        :data-selecting="selecting || undefined"
        :title="hoverTitle"
        @click="activate"
        @keydown.enter.exact.prevent="activate"
        @keydown.space.prevent="activate"
        @contextmenu="suppressNativeMenu"
      >
        <!-- The row itself is the checkbox while selecting (role/aria-checked
             above), so this box is display-only — the same non-interactive
             library Checkbox as md-checkbox.vue, not a second focusable
             control nested inside the row. -->
        <span
          v-if="selecting"
          class="mr-2 flex size-4 shrink-0 items-center justify-center"
        >
          <Checkbox
            :model-value="selected"
            tabindex="-1"
            aria-hidden="true"
            class="pointer-events-none"
          />
        </span>

        <!-- Native session rows stay text-only. Agent rows carry the agent icon
             and schedule runs a yellow clock, because the unified Recents list
             mixes model chats, external-agent chats, and schedule runs. -->
        <span
          v-if="isAgentSession"
          class="mr-2 flex size-4 shrink-0 items-center justify-center text-muted-foreground"
          role="img"
          :aria-label="agentLabel"
        >
          <component
            :is="externalAgentIcon(agentProvider, true)"
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
        <div class="relative min-w-0 flex-1">
          <MarqueeText
            :overlay="actionsEl"
            class="mr-1.5 min-w-0 text-control text-foreground dark:text-[color:oklch(0.92_0_0)]"
          >
            <span
              v-for="(run, i) in titleRuns"
              :key="i"
              :class="run.script === 'cjk' ? 'sidebar-cjk' : 'sidebar-latin'"
            >{{ run.text }}</span>
          </MarqueeText>

          <!-- Keep the overlay out of flow so revealing actions never resizes
               the title. The marquee masks this area while a control is visible.
               A selecting row has no actions button, so data-selecting keeps
               hover from masking the title or fading the spinner for nothing. -->
          <div
            ref="actionsEl"
            data-slot="sidebar-session-overlay"
            class="absolute right-0 top-1/2 flex h-6 w-6 -translate-y-1/2 items-center justify-end"
          >
            <div
              v-if="streaming"
              data-slot="sidebar-session-spinner"
              class="flex h-6 w-6 items-center justify-center transition-opacity duration-150"
            >
              <LoaderCircle
                class="size-3 animate-spin text-muted-foreground"
                :aria-label="t('chat.sessionStreaming')"
              />
            </div>
            <div
              v-else-if="unread.isUnread(session.id)"
              data-slot="sidebar-session-spinner"
              class="flex h-6 w-6 items-center justify-center transition-opacity duration-150"
              role="img"
              :aria-label="t(`notifications.kind.${unread.kindOf(session.id)}`)"
            >
              <AttentionDot
                :kind="unread.kindOf(session.id) ?? 'reply'"
                :session-id="session.id"
                :attention-at="unread.entries[session.id]?.attentionAt"
              />
            </div>

            <DropdownMenu
              v-if="!selecting"
              v-model:open="menuOpen"
            >
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
                  @keydown.enter.exact.stop
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
                  <Pencil />
                  {{ t('common.rename') }}
                </DropdownMenuItem>
                <DropdownMenuItem
                  v-if="canToggleRead"
                  @select="toggleRead"
                >
                  <component :is="isUnread ? MailOpen : Mail" />
                  {{ isUnread ? t('notifications.markRead') : t('notifications.markUnread') }}
                </DropdownMenuItem>
                <DropdownMenuItem @select="startSelection">
                  <ListChecks />
                  {{ t('chat.selectSessions') }}
                </DropdownMenuItem>
                <DropdownMenuItem
                  variant="destructive"
                  @select="$emit('delete', session)"
                >
                  <Trash2 />
                  {{ t('common.delete') }}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </div>
    </ContextMenuTrigger>
    <!-- Right-click menu: Open opens the session as its own pinned tab (a single
         click reuses the ephemeral preview slot; an explicit right-click means
         "give me a separate tab"). Rename/Select/Delete mirror the hover
         three-dot menu so both affordances stay in sync. -->
    <ContextMenuContent>
      <ContextMenuItem
        :disabled="isActive"
        @select="$emit('openNewTab', session)"
      >
        <OpenInTabIcon />
        {{ t('common.open') }}
      </ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem @select="$emit('rename', session)">
        <Pencil />
        {{ t('common.rename') }}
      </ContextMenuItem>
      <ContextMenuItem
        v-if="canToggleRead"
        @select="toggleRead"
      >
        <component :is="isUnread ? MailOpen : Mail" />
        {{ isUnread ? t('notifications.markRead') : t('notifications.markUnread') }}
      </ContextMenuItem>
      <ContextMenuItem @select="startSelection">
        <ListChecks />
        {{ t('chat.selectSessions') }}
      </ContextMenuItem>
      <ContextMenuItem
        variant="destructive"
        @select="$emit('delete', session)"
      >
        <Trash2 />
        {{ t('common.delete') }}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>
</template>

<script setup lang="ts">
import { externalAgentDisplayName, externalAgentIcon } from '@/utils/external-agent'
import { computed, inject, ref } from 'vue'
import { Clock, ListChecks, LoaderCircle, Mail, MailOpen, MoreHorizontal, Pencil, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { OpenInTabIcon } from '@memohai/icon/ui'
import type { SessionSummary } from '@/composables/api/useChat'
import {
  Checkbox,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  toast,
} from '@felinic/ui'
import { sessionAgentProvider } from '@/utils/bot-agent'
import { splitScriptRuns } from '@/utils/script-runs'
import { isAgentRuntimeType, normalizedRuntimeType, normalizedSessionMode, routeConversationLabel } from '@/store/chat-list.utils'
import MarqueeText from './marquee-text.vue'
import { SessionSelectionKey } from './session-selection'
import { useUnreadStore } from '@/store/unread'
import { useUserStore } from '@/store/user'
import { canHoldUnread } from '@/utils/session-attention'
import AttentionDot from './attention-dot.vue'

const props = defineProps<{
  session: SessionSummary
  isActive: boolean
  streaming?: boolean
}>()

const emit = defineEmits<{
  select: [session: SessionSummary]
  openNewTab: [session: SessionSummary]
  rename: [session: SessionSummary]
  delete: [session: SessionSummary]
}>()

const { t } = useI18n()
const unread = useUnreadStore()
const user = useUserStore()
const isUnread = computed(() => unread.isUnread(props.session.id))
const canToggleRead = computed(() => canHoldUnread(props.session, user.userInfo.id))

/** Two sides of one switch: reading clears the dot, marking unread brings it back for later. */
async function toggleRead() {
  if (isUnread.value) return unread.markRead(props.session.id)
  try {
    await unread.markUnread([props.session.id])
  }
  catch {
    toast.error(t('notifications.updateFailed'))
  }
}

const injectedSelection = inject(SessionSelectionKey)
if (!injectedSelection) throw new Error('SessionItem must be used within the sessions panel')
const selection = injectedSelection
const selecting = computed(() => selection.active.value)
const selected = computed(() => selection.isSelected(props.session.id))

function activate() {
  if (selecting.value) selection.toggle(props.session.id)
  else emit('select', props.session)
}

function startSelection() {
  // Selecting unmounts the dropdown before reka emits its close, which would
  // leave menuOpen stuck true and the row emphasized for good.
  menuOpen.value = false
  selection.select(props.session.id)
}

// A disabled reka ContextMenuTrigger lets the event through, which would pop
// the browser's own menu over a selecting row.
function suppressNativeMenu(event: MouseEvent) {
  if (selecting.value) event.preventDefault()
}

const menuOpen = ref(false)
const actionsEl = ref<HTMLElement>()

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

const agentProvider = computed(() => sessionAgentProvider(
  normalizedRuntimeType(props.session),
  props.session.runtime_metadata,
  props.session.metadata,
))
const isAgentSession = computed(() => isAgentRuntimeType(normalizedRuntimeType(props.session)))
const isScheduleSession = computed(() => normalizedSessionMode(props.session) === 'schedule')
const agentLabel = computed(() => externalAgentDisplayName(agentProvider.value, t('chat.sessionTypeACPAgent')))

// The old two-line subLabel is folded into the native tooltip: channel handle
// for IM sessions, agent name for ACP sessions.
const hoverTitle = computed(() => {
  const title = (props.session.title ?? '').trim() || displayLabel.value || t('chat.untitledSession')
  if (isAgentSession.value) {
    return `${title} — ${agentLabel.value}`
  }
  if (!isIMSession.value) return title
  const meta = routeMeta()
  const handle = (meta.conversation_handle as string ?? '').trim()
    || (meta.sender_username as string ?? '').trim()
    || displayLabel.value
  return handle ? `${title} — @${handle.replace(/^@/, '')}` : title
})
</script>

<style scoped>
[data-unread] :is(.sidebar-cjk, .sidebar-latin) {
  font-weight: 600;
}
</style>
