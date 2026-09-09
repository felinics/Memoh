<template>
  <div>
    <div class="flex items-start gap-2 px-1.5 py-1">
      <CircleAlert
        v-if="isError"
        class="mt-0.5 size-4 shrink-0 text-destructive"
      />
      <div class="min-w-0 flex-1">
        <p
          class="truncate text-label font-medium"
          :class="isError ? 'text-destructive' : 'text-foreground'"
        >
          {{ title }}
        </p>
        <p
          v-if="text"
          class="mt-0.5 whitespace-pre-wrap break-words text-body text-muted-foreground"
        >
          {{ text }}
        </p>
      </div>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        :aria-label="$t('chat.slash.dismiss')"
        @click="emit('dismiss')"
      >
        <X class="size-3.5" />
      </Button>
    </div>
    <!-- The Command menu shell is stripped to bare list chrome when embedded:
         the capsule already owns the surface/edge, a second one would read as
         card-in-card. -->
    <Command
      v-if="items.length"
      class="h-auto w-auto rounded-none border-0 bg-transparent shadow-none"
    >
      <CommandKeyBridge ref="bridge">
        <CommandList class="max-h-[min(20rem,45dvh)] p-1 overscroll-contain [scrollbar-gutter:stable]">
          <CommandGroup>
            <CommandItem
              v-for="item in items"
              :key="`${item.kind || 'item'}:${item.id || item.title}`"
              :value="`${item.kind || 'item'}:${item.id || item.title}`"
              :disabled="isCommandResultItemDisplayOnly(item)"
              @select="emit('select', item)"
            >
              <Sparkles
                v-if="item.kind === 'skill'"
                class="size-3.5 shrink-0 text-muted-foreground"
              />
              <Check
                v-else-if="isCommandResultItemDisplayOnly(item)"
                class="size-3.5 shrink-0 text-muted-foreground"
              />
              <List
                v-else
                class="size-3.5 shrink-0 text-muted-foreground"
              />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-body text-foreground">{{ item.title }}</span>
                <span
                  v-if="item.description"
                  class="block truncate text-caption text-muted-foreground"
                >{{ item.description }}</span>
              </span>
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </CommandKeyBridge>
    </Command>
  </div>
</template>

<script setup lang="ts">
// A slash-command result (/help, /skill list, errors) rendered as one section
// of the composer panel. Pure view: the pane owns the event data and what a
// selection means (quick actions edit the draft, skills arm chips), this
// component owns only the layout and the keyboard bridge, exposed so the
// pane's composer keydown can arbitrate arrows/Enter between this list and
// the slash picker.
import { ref } from 'vue'
import { Button, Command, CommandGroup, CommandItem, CommandKeyBridge, CommandList } from '@felinic/ui'
import { Check, CircleAlert, List, Sparkles, X } from 'lucide-vue-next'
import type { CommandActionListItem } from '@/composables/api/useChat'
import { isCommandResultItemDisplayOnly } from './slash-command-result'

defineProps<{
  isError: boolean
  title: string
  text: string
  items: CommandActionListItem[]
}>()

const emit = defineEmits<{
  (e: 'select', item: CommandActionListItem): void
  (e: 'dismiss'): void
}>()

const bridge = ref<InstanceType<typeof CommandKeyBridge> | null>(null)
defineExpose({ bridge })
</script>
