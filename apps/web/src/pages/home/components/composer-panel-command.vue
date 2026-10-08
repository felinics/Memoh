<template>
  <div class="flex flex-col gap-2">
    <!-- The header IS a notice: the same page-level banner as the error line,
         so the two can't drift. Long results (/help) scroll inside the cap
         instead of pushing the composer off screen. -->
    <CalloutBanner
      :tone="isError ? 'destructive' : 'neutral'"
      :title="title"
      :description="text"
      :dismiss-label="$t('chat.slash.dismiss')"
      class="max-h-[min(20rem,45dvh)] overflow-y-auto overscroll-contain"
      @dismiss="emit('dismiss')"
    >
      <template
        v-if="data !== undefined"
        #details
      >
        <Collapsible v-model:open="detailsOpen">
          <CollapsibleTrigger as-child>
            <TextButton class="font-sans">
              {{ $t('runtime.result.details') }}
            </TextButton>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre class="whitespace-pre-wrap break-words">{{ JSON.stringify(data, null, 2) }}</pre>
          </CollapsibleContent>
        </Collapsible>
      </template>
    </CalloutBanner>
    <!-- The pickable list is a control, not part of the notice, so it sits
         in the dock's capsule under the banner. The Command menu shell is
         stripped to bare list chrome: the capsule owns the surface/edge. -->
    <ComposerCapsule
      v-if="items.length"
      compact
    >
      <Command class="h-auto w-auto rounded-none border-0 bg-transparent shadow-none">
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
    </ComposerCapsule>
  </div>
</template>

<script setup lang="ts">
// A slash-command result (/help, /skill list, errors) rendered in the composer
// panel: a notice banner, plus the pickable list in a capsule when present. Pure view: the pane owns the event data and what a
// selection means (quick actions edit the draft, skills arm chips), this
// component owns only the layout and the keyboard bridge, exposed so the
// pane's composer keydown can arbitrate arrows/Enter between this list and
// the slash picker.
import { ref, watch } from 'vue'
import { CalloutBanner, Collapsible, CollapsibleContent, CollapsibleTrigger, TextButton, Command, CommandGroup, CommandItem, CommandKeyBridge, CommandList } from '@felinic/ui'
import { Check, List, Sparkles } from 'lucide-vue-next'
import ComposerCapsule from './composer-capsule.vue'
import type { CommandActionListItem } from '@/composables/api/useChat'
import { isCommandResultItemDisplayOnly } from './slash-command-result'

const props = defineProps<{
  data?: unknown
  isError: boolean
  title: string
  text: string
  items: CommandActionListItem[]
}>()

const emit = defineEmits<{
  (e: 'select', item: CommandActionListItem): void
  (e: 'dismiss'): void
}>()

const detailsOpen = ref(false)
watch(() => props.data, () => { detailsOpen.value = false })

const bridge = ref<InstanceType<typeof CommandKeyBridge> | null>(null)
defineExpose({ bridge })
</script>
