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
          <header class="flex h-7 items-center gap-1.5 px-1.5">
            <h2
              :id="headingId"
              class="text-xs font-[550] tracking-[-0.02em] text-muted-foreground"
            >
              {{ t('notifications.inbox') }}
            </h2>
            <span
              ref="count"
              class="inline-flex"
            >
              <BadgeCount
                :count="unread.total"
                variant="destructive"
                aria-hidden="true"
              />
            </span>
          </header>

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
        </section>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useNow } from '@vueuse/core'
import { BadgeCount, TextButton } from '@felinic/ui'
import { useRouter } from 'vue-router'
import { useChatStore } from '@/store/chat-list'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import { bounce, enterRow, leaveRow, revealRow } from '@/utils/attention-motion'
import { sessionAttentionPath } from '@/utils/session-attention'
import NeedsYouRow from './needs-you-row.vue'

const LIMIT = 5

const { t, locale } = useI18n()
const unread = useUnreadStore()
const chat = useChatStore()
const router = useRouter()
const headingId = useId()
const expanded = ref(false)
const count = useTemplateRef('count')
const now = useNow({ interval: 30_000 })
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

function openSession(entry: UnreadEntry) {
  void router.push(sessionAttentionPath(entry.botId, entry.sessionId))
}
</script>
