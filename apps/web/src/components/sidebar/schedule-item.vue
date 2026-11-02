<template>
  <!-- The sidebar's schedule card. Deliberately NOT a SettingsRow: the panel is
       its own dense surface (compact card, two tight lines, hover-revealed
       actions), a different spatial relationship from the settings list this
       used to share code with.
       The card is the frame; its header row is the control. Below the header
       the default slot hosts what the card expands into (the sessions its runs
       created), so the expanded list reads as part of the same card. -->
  <div :class="cardClass">
    <div
      :class="headerClass"
      role="button"
      tabindex="0"
      :aria-expanded="expandable ? expanded : undefined"
      :title="pinnedSession ? t('bots.schedule.sessions.openPinned') : undefined"
      @click="$emit('open')"
      @keydown.enter.exact="$emit('open')"
      @keydown.space.prevent="$emit('open')"
    >
      <div class="min-w-0 flex-1 px-3 py-2.5">
        <div class="flex min-w-0 items-center gap-2">
          <p class="truncate text-control font-normal leading-snug text-foreground">
            {{ item.name }}
          </p>
          <!-- Unread runs show on the card, so a folded card still says it has news. -->
          <BadgeCount
            v-if="unreadCount"
            :count="unreadCount"
            :variant="attentionBadgeVariant(unreadKinds)"
            class="shrink-0"
          />
          <span
            v-if="timeLabel || running"
            class="flex shrink-0 items-center gap-1 text-caption tabular-nums text-muted-foreground"
          >
            <Spinner
              v-if="running"
              class="size-3"
              :aria-label="t('chat.sessionStreaming')"
            />
            {{ timeLabel }}
          </span>
        </div>
        <p class="mt-0.5 flex min-w-0 items-center gap-1 text-caption leading-snug text-muted-foreground">
          <!-- Marks a schedule that always runs in one existing conversation,
               the card that opens that conversation instead of expanding. -->
          <MessageSquare
            v-if="pinnedSession"
            class="size-3 shrink-0"
            role="img"
            :aria-label="t('bots.schedule.sessions.pinned')"
          />
          <span class="truncate">{{ description }}</span>
        </p>
      </div>

      <div class="flex shrink-0 items-center gap-2 pr-1.5">
        <DropdownMenu
          @update:open="(open: boolean) => { menuOpen = open }"
        >
          <DropdownMenuTrigger as-child>
            <Button
              variant="ghost"
              size="icon-sm"
              class="size-6 transition-opacity"
              :class="menuOpen ? 'opacity-100' : 'opacity-0 group-hover/card:opacity-100'"
              :aria-label="t('common.actions')"
              @click.stop
            >
              <MoreHorizontal class="size-3.5" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem @select="$emit('edit')">
              <Pencil />
              {{ t('bots.schedule.edit') }}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              @select="$emit('delete')"
            >
              <Trash2 />
              {{ t('bots.schedule.delete') }}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <Switch
          :model-value="!!item.enabled"
          :disabled="busy"
          :aria-label="t('bots.schedule.form.enabled')"
          @click.stop
          @update:model-value="(value: boolean) => $emit('toggle', !!value)"
        />
      </div>
    </div>

    <AutoHeight v-if="expandable">
      <slot v-if="expanded" />
    </AutoHeight>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessageSquare, MoreHorizontal, Pencil, Trash2 } from 'lucide-vue-next'
import {
  AutoHeight,
  BadgeCount,
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Spinner,
  Switch,
} from '@felinic/ui'
import type { ScheduleSchedule } from '@memohai/sdk'
import type { AttentionKind } from '@/composables/api/attention-api'
import { attentionBadgeVariant } from '@/utils/session-attention'

withDefaults(defineProps<{
  item: ScheduleSchedule
  description: string
  timeLabel?: string
  busy?: boolean
  /** The card expands into its run sessions; the header announces aria-expanded. */
  expandable?: boolean
  expanded?: boolean
  /** The schedule runs inside one existing conversation; the card opens it. */
  pinnedSession?: boolean
  /** A run of this schedule is executing right now. */
  running?: boolean
  /** Unread sessions of this schedule's runs (or its one conversation). */
  unreadCount?: number
  unreadKinds?: AttentionKind[]
}>(), {
  timeLabel: '',
  busy: false,
  expandable: false,
  expanded: false,
  pinnedSession: false,
  running: false,
  unreadCount: 0,
  unreadKinds: () => [],
})

defineEmits<{
  open: []
  edit: []
  delete: []
  toggle: [enabled: boolean]
}>()

// The sidebar is a deliberately local row system (see ui-owners), so this card
// carries its own hover — on the sidebar's own rung of the overlay ladder, the
// same token session-item.vue and folders-section.vue use, never a baked tint.
// The hairline stays in BOTH modes. The surface rule drops dark-mode borders
// because a card's fill separates it from the page (card 0.21 on background
// 0.122, Δ≈0.088 lightness); this row sits on the sidebar surface instead
// (sidebar 0.185, Δ≈0.025), where the fill alone does not read as a card —
// the `bordered` case of surfaceEdgeClass (dialogs keep their dark border for
// the same reason). Do not "fix" this back to dark:border-0.
// The hover belongs to the header only, so pointing at an expanded run row
// highlights that row alone instead of the whole card; the card clips it to
// its own corners, so the header carries no radius of its own.
const cardClass = 'group/card relative overflow-hidden rounded-[var(--radius-menu-shell)] border border-border bg-card'
const headerClass = 'flex cursor-pointer items-center gap-3 transition-colors hover:bg-[color:var(--sidebar-hover)] focus-visible:outline-none' /* ui-allow-style: sidebar rows are a local row system (see ui-owners) — same hover token as session-item.vue */

const { t } = useI18n()
const menuOpen = ref(false)
</script>
