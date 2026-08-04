<template>
  <!-- Mobile settings chrome. LIST: ← exits to chat, plus the centered section
       title. CONTENT: ← pops one drill-in level or swaps back to the list —
       that decision lives in the parent shell; pages render their own
       PageShell titles, so this bar deliberately shows none there. -->
  <header class="flex h-11 shrink-0 items-center border-b border-border bg-background px-1.5">
    <Button
      variant="ghost"
      size="icon"
      shape="circle"
      :class="iconButtonClass"
      :title="backLabel"
      :aria-label="backLabel"
      @click="emit('back')"
    >
      <ChevronLeft :stroke-width="1.75" />
    </Button>
    <template v-if="mode === 'list'">
      <!-- Equal-width flanks (icon button / spacer) keep the title optically
           centered. -->
      <div class="min-w-0 flex-1 truncate text-center text-control font-medium">
        {{ t('sidebar.settings') }}
      </div>
      <div class="size-9 shrink-0" />
    </template>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronLeft } from 'lucide-vue-next'
import { Button } from '@felinic/ui'

defineProps<{ mode: 'list' | 'content' }>()
const emit = defineEmits<{ back: [] }>()

const { t } = useI18n()
const backLabel = computed(() => t('chat.topBar.goBack'))

// Same chrome as the chat mobile top bar's icon buttons (muted at rest →
// foreground on hover) so the two shells' bars read as one language.
const iconButtonClass = 'shrink-0 text-muted-foreground hover:text-foreground' /* ui-allow-style */
</script>
