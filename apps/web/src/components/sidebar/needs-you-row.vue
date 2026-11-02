<template>
  <button
    type="button"
    data-slot="needs-you-row"
    class="relative flex w-full cursor-pointer items-center gap-2.5 rounded-md py-1.5 pr-1.5 pl-1.5 text-left"
    :aria-label="label"
    :title="title"
    @click="emit('open')"
  >
    <span
      ref="flash"
      data-slot="needs-you-row-flash"
      :data-kind="kind"
      aria-hidden="true"
    />
    <Avatar class="size-5 shrink-0 ring-1 ring-border">
      <AvatarImage
        v-if="avatarUrl"
        :src="avatarUrl"
        :alt="botName"
      />
      <AvatarFallback class="text-caption">
        {{ firstAvatarCharacter(botName) }}
      </AvatarFallback>
    </Avatar>
    <span class="min-w-0 flex-1">
      <!-- Time rides the title line like a mail list, so the narrow
           meta line only has to fit bot and state. -->
      <span class="flex min-w-0 items-baseline gap-2">
        <span class="min-w-0 flex-1 truncate text-control font-medium text-foreground">
          {{ title }}
        </span>
        <time
          class="shrink-0 text-xs tabular-nums text-muted-foreground"
          :datetime="entry.attentionAt"
        >{{ ago }}</time>
      </span>
      <span class="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
        <span class="min-w-0 truncate">{{ botName }}</span>
        <span aria-hidden="true">·</span>
        <span
          class="shrink-0"
          :class="kindClass[kind]"
        >{{ t(`notifications.kind.${kind}`) }}</span>
      </span>
    </span>
    <span class="flex size-6 shrink-0 items-center justify-center">
      <!-- A schedule's unread runs share this row; their number takes the dot's place. -->
      <span
        v-if="count > 1"
        ref="badge"
        class="inline-flex"
      >
        <BadgeCount
          :count="count"
          :variant="attentionBadgeVariant([kind])"
          aria-hidden="true"
        />
      </span>
      <AttentionDot
        v-else
        :kind="kind"
        :session-id="entry.sessionId"
        :attention-at="entry.attentionAt"
      />
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed, onMounted, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback, AvatarImage, BadgeCount } from '@felinic/ui'
import type { AttentionKind } from '@/composables/api/attention-api'
import { firstAvatarCharacter } from '@/composables/useAvatarInitials'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import { bounce, flashRow } from '@/utils/attention-motion'
import { attentionBadgeVariant } from '@/utils/session-attention'
import AttentionDot from './attention-dot.vue'

/**
 * One "Needs you" row. The kind tint flashes once only when this attention just
 * arrived live; rows revealed later (expanding the list, reloading) never replay it.
 */
const props = withDefaults(defineProps<{
  /** The newest session of the row; one schedule's unread runs share a row. */
  entry: UnreadEntry
  botName: string
  avatarUrl?: string
  ago: string
  /** Unread sessions the row stands for. */
  count?: number
  /** The most urgent kind among them; the entry's own kind by default. */
  urgentKind?: AttentionKind
}>(), { count: 1, urgentKind: undefined })
const emit = defineEmits<{ open: [] }>()

const { t } = useI18n()
const unread = useUnreadStore()
const flash = useTemplateRef('flash')

/** The kind label carries the state in words; color only reinforces it. */
const kindClass: Record<AttentionKind, string> = {
  update: '',
  reply: '',
  failed: 'text-[color:var(--accent-red)]',
  needs_input: 'text-[color:var(--warning-foreground)]',
  marked: '',
}

const kind = computed(() => props.urgentKind ?? props.entry.kind)
const title = computed(() => props.entry.title || t('chat.untitledSession'))
const label = computed(() => [
  title.value,
  props.botName,
  t(`notifications.kind.${kind.value}`),
  ...(props.count > 1 ? [t('notifications.unreadRuns', { count: props.count })] : []),
].join(', '))
const badge = useTemplateRef('badge')

function flashIfFresh() {
  if (flash.value && unread.isFresh(props.entry.sessionId, props.entry.attentionAt)) flashRow(flash.value)
}

onMounted(flashIfFresh)
watch(() => props.entry.attentionAt, flashIfFresh)
/** A new run joining the row bumps its number, the way the inbox count does. */
watch(() => props.count, (count, previous) => {
  if (count > previous && badge.value) bounce(badge.value)
})
</script>
