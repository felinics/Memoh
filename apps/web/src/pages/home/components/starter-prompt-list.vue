<script setup lang="ts">
import { Item, ItemGroup, Kbd, Skeleton } from '@felinic/ui'

export interface StarterPromptRow {
  key: string
  title: string
  prompt: string
}

/**
 * Suggestions as full-width rows: the title column and the prompt column line
 * up across rows (a subgrid sized by the longest title), so the set scans like
 * a table of contents while every prompt stays readable in full. Under 32rem
 * of section width the columns fold into a title-over-prompt stack.
 *
 * Rows are `Item` buttons, so hover, press and focus come from the library.
 * The rest (inset hairlines that step aside around the active row, the
 * revealed "Use" hint, the prompt brightening) lives in style.css under
 * [data-starter-prompt]. `rows === null` renders the skeleton of three rows.
 */
const props = defineProps<{
  rows: StarterPromptRow[] | null
  /** Show the "↵ Use" hint on the active row: only when Enter would pick it. */
  enterHint?: boolean
}>()

const emit = defineEmits<{
  select: [row: StarterPromptRow]
  preview: [prompt: string | null]
}>()

function previewRow(row: StarterPromptRow, active: boolean) {
  emit('preview', active ? row.prompt : null)
}

/** Arrow keys, Home and End move focus between rows, wrapping at the ends. */
function moveFocus(event: KeyboardEvent) {
  const rows = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[data-starter-prompt]')]
  const index = rows.indexOf(document.activeElement as HTMLElement)
  if (index < 0) return
  const target = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: rows.length - 1 }[event.key]
  if (target === undefined) return
  event.preventDefault()
  rows[(target + rows.length) % rows.length]?.focus()
}
</script>

<template>
  <ItemGroup
    role="group"
    class="grid grid-cols-[minmax(0,1fr)] gap-x-4 @lg:grid-cols-[fit-content(40%)_minmax(0,1fr)_auto]"
    :aria-busy="props.rows === null || undefined"
    @keydown="moveFocus"
  >
    <template v-if="props.rows">
      <Item
        v-for="row in props.rows"
        :key="row.key"
        as="button"
        type="button"
        size="sm"
        data-starter-prompt
        class="relative col-span-full grid grid-cols-subgrid items-start gap-x-4 gap-y-0.5 text-left"
        @click="emit('select', row)"
        @mouseenter="previewRow(row, true)"
        @mouseleave="previewRow(row, false)"
        @focus="previewRow(row, true)"
        @blur="previewRow(row, false)"
      >
        <span class="truncate text-label font-medium text-foreground">{{ row.title }}</span>
        <span
          data-starter-prompt-text
          class="text-body @lg:text-label"
        >{{ row.prompt }}</span>
        <span
          v-if="props.enterHint"
          data-starter-prompt-hint
          aria-hidden="true"
          class="hidden items-center gap-1.5 text-caption text-muted-foreground @lg:inline-flex"
        >
          <Kbd>↵</Kbd>{{ $t('chatExamples.fillIn') }}
        </span>
      </Item>
    </template>
    <template v-else>
      <!-- Same Item box and subgrid as the rows, so the swap keeps the height. -->
      <Item
        v-for="i in 3"
        :key="i"
        size="sm"
        data-starter-prompt
        class="relative col-span-full grid grid-cols-subgrid items-start gap-x-4 gap-y-0.5"
      >
        <!-- Bars sit in boxes as tall as the text lines they stand for. -->
        <span class="flex h-5 items-center">
          <Skeleton class="h-3.5 w-24" />
        </span>
        <span class="flex h-4 items-center @lg:h-5">
          <Skeleton class="h-3 w-full max-w-md" />
        </span>
      </Item>
    </template>
  </ItemGroup>
</template>
