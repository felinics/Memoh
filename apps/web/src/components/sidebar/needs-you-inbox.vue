<template>
  <!-- Geometry: the card sits on the panel's 8px gutter; header label and avatars
       land on the 19px title column of the session rows below, and the row dot on
       the same x as a Recents row dot, so the two lists read as one grid. The card
       and its rows grow and collapse their height (attention-motion.ts), so the
       folders and Recents below are pushed instead of jumping. -->
  <Transition
    :css="false"
    @enter="enterRow"
    @leave="leaveRow"
  >
    <div
      v-if="unread.total"
      class="overflow-hidden"
    >
      <div class="px-2 pt-2 pb-1">
        <section
          data-slot="needs-you-card"
          class="p-1"
          :aria-labelledby="headingId"
        >
          <!-- Header follows the sidebar section-header contract (group-header.vue):
               the whole row folds the list, the chevron is revealed on hover or
               keyboard focus. The trailing × reads everything, behind a confirm. -->
          <div class="group/needs-you-header flex h-7 items-center">
            <div
              data-slot="needs-you-header"
              role="button"
              tabindex="0"
              class="flex h-full min-w-0 flex-1 cursor-pointer select-none items-center gap-1.5 rounded-md pl-1.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              :aria-expanded="!collapsed"
              :aria-controls="listId"
              @click="collapsed = !collapsed"
              @keydown.enter.exact.prevent="collapsed = !collapsed"
              @keydown.space.prevent="collapsed = !collapsed"
            >
              <h2
                :id="headingId"
                class="text-xs font-[550] tracking-[-0.02em] text-muted-foreground transition-colors group-hover/needs-you-header:text-foreground"
              >
                {{ t('notifications.inbox') }}
              </h2>
              <span
                ref="count"
                class="inline-flex"
              >
                <BadgeCount
                  :count="unread.total"
                  :variant="countVariant"
                  aria-hidden="true"
                />
              </span>
              <ChevronRight
                class="size-3 shrink-0 text-muted-foreground transition-[opacity,rotate] duration-150 can-hover:opacity-0 group-hover/needs-you-header:opacity-100 group-focus-visible/needs-you-header:opacity-100"
                :class="collapsed ? 'rotate-0' : 'rotate-90'"
                aria-hidden="true"
              />
            </div>
            <ConfirmPopover
              :message="t('notifications.clearAllConfirm', { count: unread.total })"
              :confirm-text="t('notifications.clearAll')"
              :cancel-text="t('common.cancel')"
              @confirm="clearAll"
            >
              <template #trigger>
                <Button
                  variant="quiet"
                  size="icon-sm"
                  class="opacity-0 transition-opacity duration-150 group-hover/needs-you-header:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-100"
                  :aria-label="t('notifications.clearAll')"
                >
                  <X class="size-4" />
                </Button>
              </template>
            </ConfirmPopover>
          </div>

          <Transition
            :css="false"
            @enter="revealRow"
            @leave="leaveRow"
          >
            <div
              v-show="!collapsed"
              :id="listId"
              data-slot="needs-you-list"
              class="overflow-hidden"
            >
              <TransitionGroup
                tag="ul"
                name="needs-you-row"
                class="flex flex-col"
                @enter="enterListRow"
                @leave="leaveRow"
              >
                <li
                  v-for="entry in visibleEntries"
                  :key="entry.sessionId"
                  class="overflow-hidden"
                >
                  <NeedsYouRow
                    :entry="entry"
                    :bot-name="botName(entry.botId)"
                    :avatar-url="botFor(entry.botId)?.avatar_url"
                    :ago="ago(entry.attentionAt)"
                    @open="openSession(entry)"
                  />
                </li>
              </TransitionGroup>

              <TextButton
                v-if="unread.total > LIMIT"
                class="mt-0.5 w-full justify-start px-1.5"
                :aria-expanded="expanded"
                @click="toggleExpanded"
              >
                {{ expanded ? t('notifications.showLess') : t('notifications.showAll', { count: unread.total }) }}
              </TextButton>
            </div>
          </Transition>
        </section>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLocalStorage, useNow } from '@vueuse/core'
import { BadgeCount, Button, ConfirmPopover, TextButton, toast } from '@felinic/ui'
import { ChevronRight, X } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { useChatStore } from '@/store/chat-list'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import { bounce, enterRow, leaveRow, revealRow } from '@/utils/attention-motion'
import { attentionBadgeVariant, sessionAttentionPath } from '@/utils/session-attention'
import NeedsYouRow from './needs-you-row.vue'

const LIMIT = 5

const { t, locale } = useI18n()
const unread = useUnreadStore()
const chat = useChatStore()
const router = useRouter()
const headingId = useId()
const listId = useId()
/** Folded or not is a per-device sidebar preference, like the Recents section. */
const collapsed = useLocalStorage('memoh-needs-you-collapsed', false)
const expanded = ref(false)
const count = useTemplateRef('count')
const now = useNow({ interval: 30_000 })
/** The count speaks for every unread session, folded or not, so a failure past the fifth row still turns it red. */
const countVariant = computed(() => attentionBadgeVariant(unread.inbox.map(entry => entry.kind)))
const visibleEntries = computed(() => expanded.value ? unread.inbox : unread.inbox.slice(0, LIMIT))
watch(() => unread.total, (total) => { if (total <= LIMIT) expanded.value = false })
/** The count bumps when it changes while the card is already showing, not when it appears. */
watch(() => unread.total, (total, previous) => {
  if (total && previous && count.value) bounce(count.value)
})

const botFor = (id: string) => chat.bots.find(bot => bot.id === id)
const botName = (id: string) => botFor(id)?.display_name || botFor(id)?.name || t('sidebar.bots')

/**
 * Elapsed time as a bare narrow unit ("15m", "15分钟") for a ~240px column. Under a minute reads as "just now"
 * rather than a seconds counter frozen between 30s ticks; past a week, a short date.
 */
function ago(at: string) {
  const ms = now.value.getTime() - Date.parse(at)
  if (ms < 60_000) return t('notifications.justNow')
  const unit = (value: number, name: 'minute' | 'hour' | 'day') =>
    new Intl.NumberFormat(locale.value, { style: 'unit', unit: name, unitDisplay: 'narrow' }).format(value)
  if (ms < 3_600_000) return unit(Math.floor(ms / 60_000), 'minute')
  if (ms < 86_400_000) return unit(Math.floor(ms / 3_600_000), 'hour')
  if (ms < 604_800_000) return unit(Math.floor(ms / 86_400_000), 'day')
  return new Date(at).toLocaleDateString(locale.value, { month: 'numeric', day: 'numeric' })
}

let revealing = false

/** Rows that appear because the user expanded the list open at collapse speed, not as arrivals. */
function toggleExpanded() {
  expanded.value = !expanded.value
  revealing = expanded.value
  void nextTick(() => { revealing = false })
}

function enterListRow(el: Element, done: () => void) {
  if (revealing) revealRow(el, done)
  else enterRow(el, done)
}

/**
 * Reads every unread session at once. The toast's undo marks the same sessions
 * unread again, which restores their kind and time, so nothing is lost by a misclick.
 */
async function clearAll() {
  let cleared: string[]
  try {
    cleared = await unread.markAllRead()
  }
  catch {
    toast.error(t('notifications.updateFailed'))
    return
  }
  if (!cleared.length) return
  toast(t('notifications.clearedAll', { count: cleared.length }), {
    action: {
      label: t('notifications.undo'),
      onClick: () => {
        collapsed.value = false
        unread.markUnread(cleared).catch(() => toast.error(t('notifications.updateFailed')))
      },
    },
  })
}

function openSession(entry: UnreadEntry) {
  void router.push(sessionAttentionPath(entry.botId, entry.sessionId))
}
</script>
