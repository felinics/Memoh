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
      :data-kind="entry.kind"
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
          :class="kindClass[entry.kind]"
        >{{ t(`notifications.kind.${entry.kind}`) }}</span>
      </span>
    </span>
    <span class="flex size-6 shrink-0 items-center justify-center">
      <AttentionDot
        :kind="entry.kind"
        :session-id="entry.sessionId"
        :attention-at="entry.attentionAt"
      />
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed, onMounted, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback, AvatarImage } from '@felinic/ui'
import type { AttentionKind } from '@/composables/api/attention-api'
import { firstAvatarCharacter } from '@/composables/useAvatarInitials'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import { flashRow } from '@/utils/attention-motion'
import AttentionDot from './attention-dot.vue'

/**
 * One "Needs you" row. The kind tint flashes once only when this attention just
 * arrived live; rows revealed later (expanding the list, reloading) never replay it.
 */
const props = defineProps<{
  entry: UnreadEntry
  botName: string
  avatarUrl?: string
  ago: string
}>()
const emit = defineEmits<{ open: [] }>()

const { t } = useI18n()
const unread = useUnreadStore()
const flash = useTemplateRef('flash')

/** The kind label carries the state in words; color only reinforces it. */
const kindClass: Record<AttentionKind, string> = {
  reply: '',
  failed: 'text-[color:var(--accent-red)]',
  needs_input: 'text-[color:var(--warning-foreground)]',
  marked: '',
}

const title = computed(() => props.entry.title || t('chat.untitledSession'))
const label = computed(() => `${title.value}, ${props.botName}, ${t(`notifications.kind.${props.entry.kind}`)}`)

function flashIfFresh() {
  if (flash.value && unread.isFresh(props.entry.sessionId, props.entry.attentionAt)) flashRow(flash.value)
}

onMounted(flashIfFresh)
watch(() => props.entry.attentionAt, flashIfFresh)
</script>
