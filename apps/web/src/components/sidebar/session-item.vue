<template>
  <ContextMenu>
    <ContextMenuTrigger as-child>
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
        role="button"
        tabindex="0"
        class="group relative flex items-center min-h-[2.125rem] w-full rounded-[9px] px-[11px] text-left cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        :data-ui-selected="isActive ? '' : undefined"
        :data-menu-open="menuOpen || undefined"
        :data-streaming="streaming || undefined"
        :title="hoverTitle"
        @click="$emit('select', session)"
        @keydown.enter.exact.prevent="$emit('select', session)"
        @keydown.space.prevent="$emit('select', session)"
      >
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
               the title. The marquee masks this area while a control is visible. -->
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
         "give me a separate tab"). Rename/Delete reuse the same emits as the
         hover three-dot button so both affordances stay in sync. -->
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
import { computed, ref } from 'vue'
import { Clock, LoaderCircle, MoreHorizontal, Pencil, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { OpenInTabIcon } from '@memohai/icon/ui'
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
import { sessionAgentProvider } from '@/utils/bot-agent'
import { splitScriptRuns } from '@/utils/script-runs'
import { isAgentRuntimeType, normalizedRuntimeType, normalizedSessionMode, routeConversationLabel } from '@/store/chat-list.utils'
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
