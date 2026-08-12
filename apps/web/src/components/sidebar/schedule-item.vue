<template>
  <!-- The sidebar's schedule card. Deliberately NOT a SettingsRow: the panel is
       its own dense surface (compact card, two tight lines, hover-revealed
       actions), a different spatial relationship from the settings list this
       used to share code with. -->
  <div
    :class="cardClass"
    role="button"
    tabindex="0"
    @click="$emit('open')"
    @keydown.enter="$emit('open')"
    @keydown.space.prevent="$emit('open')"
  >
    <div class="min-w-0 flex-1 px-3 py-2.5">
      <div class="flex min-w-0 items-center gap-2">
        <p class="truncate text-control font-normal leading-snug text-foreground">
          {{ item.name }}
        </p>
        <span
          v-if="timeLabel"
          class="shrink-0 text-caption tabular-nums text-muted-foreground"
        >
          {{ timeLabel }}
        </span>
      </div>
      <p class="mt-0.5 truncate text-caption leading-snug text-muted-foreground">
        {{ description }}
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
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MoreHorizontal, Pencil, Trash2 } from 'lucide-vue-next'
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Switch,
} from '@felinic/ui'
import type { ScheduleSchedule } from '@memohai/sdk'

withDefaults(defineProps<{
  item: ScheduleSchedule
  description: string
  timeLabel?: string
  busy?: boolean
}>(), {
  timeLabel: '',
  busy: false,
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
const cardClass = 'group/card relative flex cursor-pointer items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card transition-colors hover:bg-[color:var(--sidebar-hover)] focus-visible:outline-none' /* ui-allow-style: sidebar rows are a local row system (see ui-owners) — same hover token as session-item.vue */

const { t } = useI18n()
const menuOpen = ref(false)
</script>
