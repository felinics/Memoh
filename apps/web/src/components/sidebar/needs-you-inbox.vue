<template>
  <!-- Geometry: the card sits on the panel's 8px gutter; header label and avatars
       land on the 19px title column of the session rows below, and the row dot on
       the same x as a Recents row dot, so the two lists read as one grid. -->
  <Transition name="needs-you-card">
    <div
      v-if="unread.total"
      class="needs-you-reveal"
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
            <BadgeCount
              :count="unread.total"
              variant="destructive"
              aria-hidden="true"
            />
          </header>

          <TransitionGroup
            tag="ul"
            name="needs-you-row"
            class="flex flex-col"
          >
            <li
              v-for="entry in visibleEntries"
              :key="entry.sessionId"
              class="needs-you-reveal"
            >
              <div>
                <button
                  type="button"
                  data-slot="needs-you-row"
                  class="flex w-full cursor-pointer items-center gap-2.5 rounded-md py-1.5 pr-1.5 pl-1.5 text-left"
                  :aria-label="rowLabel(entry)"
                  :title="entry.title || t('chat.untitledSession')"
                  @click="openSession(entry)"
                >
                  <Avatar class="size-5 shrink-0 ring-1 ring-border">
                    <AvatarImage
                      v-if="botFor(entry.botId)?.avatar_url"
                      :src="botFor(entry.botId)?.avatar_url ?? ''"
                      :alt="botName(entry.botId)"
                    />
                    <AvatarFallback class="text-caption">
                      {{ firstAvatarCharacter(botName(entry.botId)) }}
                    </AvatarFallback>
                  </Avatar>
                  <span class="min-w-0 flex-1">
                    <!-- Time rides the title line like a mail list, so the narrow
                         meta line only has to fit bot and state. -->
                    <span class="flex min-w-0 items-baseline gap-2">
                      <span class="min-w-0 flex-1 truncate text-control font-medium text-foreground">
                        {{ entry.title || t('chat.untitledSession') }}
                      </span>
                      <time
                        class="shrink-0 text-xs tabular-nums text-muted-foreground"
                        :datetime="entry.attentionAt"
                      >{{ ago(entry.attentionAt) }}</time>
                    </span>
                    <span class="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
                      <span class="min-w-0 truncate">{{ botName(entry.botId) }}</span>
                      <span aria-hidden="true">·</span>
                      <span
                        class="shrink-0"
                        :class="kindClass[entry.kind]"
                      >{{ t(`notifications.kind.${entry.kind}`) }}</span>
                    </span>
                  </span>
                  <span class="flex size-6 shrink-0 items-center justify-center">
                    <AttentionDot
                      :kind="entry.kind"
                      :pulse-key="entry.attentionAt"
                    />
                  </span>
                </button>
              </div>
            </li>
          </TransitionGroup>

          <TextButton
            v-if="unread.total > LIMIT"
            class="mt-0.5 w-full justify-start px-1.5"
            :aria-expanded="expanded"
            @click="expanded = !expanded"
          >
            {{ expanded ? t('notifications.showLess') : t('notifications.showAll', { count: unread.total }) }}
          </TextButton>
        </section>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useNow } from '@vueuse/core'
import { Avatar, AvatarFallback, AvatarImage, BadgeCount, TextButton } from '@felinic/ui'
import { useRouter } from 'vue-router'
import { useChatStore } from '@/store/chat-list'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import type { AttentionKind } from '@/composables/api/attention-api'
import { firstAvatarCharacter } from '@/composables/useAvatarInitials'
import { sessionAttentionPath } from '@/utils/session-attention'
import AttentionDot from './attention-dot.vue'

const LIMIT = 5

const { t, locale } = useI18n()
const unread = useUnreadStore()
const chat = useChatStore()
const router = useRouter()
const headingId = useId()
const expanded = ref(false)
const now = useNow({ interval: 30_000 })
const visibleEntries = computed(() => expanded.value ? unread.inbox : unread.inbox.slice(0, LIMIT))
watch(() => unread.total, (total) => { if (total <= LIMIT) expanded.value = false })

/** The kind label carries the state in words; color only reinforces it. */
const kindClass: Record<AttentionKind, string> = {
  reply: '',
  failed: 'text-[color:var(--accent-red)]',
  needs_input: 'text-[color:var(--warning-foreground)]',
}

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

function rowLabel(entry: UnreadEntry) {
  return `${entry.title || t('chat.untitledSession')}, ${botName(entry.botId)}, ${t(`notifications.kind.${entry.kind}`)}`
}

function openSession(entry: UnreadEntry) {
  void router.push(sessionAttentionPath(entry.botId, entry.sessionId))
}
</script>
