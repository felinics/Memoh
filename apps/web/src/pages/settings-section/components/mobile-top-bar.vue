<template>
  <!-- Mobile settings chrome. LIST: ← exits to chat, plus the centered section
       title. CONTENT: ← pops one drill-in level or goes back to the list —
       that decision lives in the parent shell; pages render their own
       PageShell titles, so this bar deliberately shows none there. -->
  <MobileBar>
    <template #left>
      <MobileBarIconButton
        :icon="ChevronLeft"
        :label="t('common.back')"
        @click="emit('back')"
      />
    </template>
    <!-- Equal-width flanks (icon button / spacer) keep the list title
         optically centered; content mode renders no title at all (pages own
         their PageShell headers). -->
    <template
      v-if="mode === 'list'"
      #default
    >
      <div class="min-w-0 flex-1 truncate text-center text-control font-medium">
        {{ t('sidebar.settings') }}
      </div>
    </template>
    <template
      v-if="mode === 'list'"
      #right
    >
      <div class="size-9 shrink-0" />
    </template>
  </MobileBar>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ChevronLeft } from 'lucide-vue-next'
import MobileBar from '@/components/mobile-bar/index.vue'
import MobileBarIconButton from '@/components/mobile-bar/icon-button.vue'

defineProps<{ mode: 'list' | 'content' }>()
const emit = defineEmits<{ back: [] }>()

const { t } = useI18n()
</script>
