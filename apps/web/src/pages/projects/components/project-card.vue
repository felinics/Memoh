<template>
  <!-- A grid card that is an ENTRY POINT into the project. ActionCard is the
       library's entry-point shape but it is a slim single-line row with no slot
       for the issue tallies, and its own docs forbid stretching it two ways at
       once — so this is a page-local card, composed from the same card language
       (bg-card + one hairline + card radius + flat, no hover-rise). If a second
       caller ever needs this shape, lift it instead of copying it. -->
  <button
    type="button"
    :class="cardClass"
    @click="emit('open')"
  >
    <!-- Hover fill as a neutral overlay UNDER the content: --card is not
         --background, so a bg-* swap on the body would replace the white fill
         and bleed the page surface through. Negative z-index inside the
         isolated stacking context paints above the background, below content —
         the same mechanism ActionCard uses. -->
    <span :class="overlayClass" />

    <div class="flex min-w-0 items-center gap-2">
      <FolderKanban class="size-4 shrink-0 text-foreground" />
      <span class="min-w-0 flex-1 truncate text-control font-medium text-foreground">
        {{ project.name }}
      </span>
      <ChevronRight class="size-4 shrink-0 text-muted-foreground" />
    </div>

    <!-- Reserve the description line even when empty so cards in a row keep
         one baseline instead of stepping. -->
    <p class="mt-1 line-clamp-2 min-h-8 text-body text-muted-foreground">
      {{ project.description || t('projects.noDescription') }}
    </p>

    <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1">
      <span class="inline-flex items-center gap-1.5 text-body text-muted-foreground">
        <CircleDot class="size-3.5 text-accent-green" />
        {{ t('projects.openIssues', { count: project.open_issue_count ?? 0 }) }}
      </span>
      <span class="inline-flex items-center gap-1.5 text-body text-muted-foreground">
        <CircleCheck class="size-3.5 text-accent-purple" />
        {{ t('projects.closedIssues', { count: project.closed_issue_count ?? 0 }) }}
      </span>
    </div>
  </button>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ChevronRight, CircleCheck, CircleDot, FolderKanban } from 'lucide-vue-next'
import type { ProjectProject } from '@memohai/sdk'

defineProps<{
  project: ProjectProject
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()

const cardClass = 'group/card relative isolate flex cursor-pointer flex-col rounded-xl border border-border bg-card p-4 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50' /* ui-allow-style */
const overlayClass = 'pointer-events-none absolute inset-0 -z-10 rounded-xl bg-[color:var(--ui-hover)] opacity-0 transition-opacity duration-150 group-hover/card:opacity-100' /* ui-allow-style */
</script>
