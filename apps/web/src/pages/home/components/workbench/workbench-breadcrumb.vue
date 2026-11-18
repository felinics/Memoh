<template>
  <div class="flex h-7 shrink-0 items-center gap-0.5 bg-surface-editor px-3 text-label text-muted-foreground">
    <template
      v-for="(segment, idx) in pathSegments"
      :key="idx"
    >
      <ChevronRight
        v-if="idx > 0"
        class="size-3 shrink-0 text-muted-foreground"
      />
      <span
        v-if="idx === pathSegments.length - 1"
        class="inline-flex min-w-0 items-center gap-1.5 text-foreground"
      >
        <span
          class="seti-icon shrink-0"
          :style="{ color: fileIcon.color }"
        >{{ fileIcon.char }}</span>
        <span class="truncate">{{ segment }}</span>
      </span>
      <span
        v-else
        class="shrink-0"
      >{{ segment }}</span>
    </template>
    <div
      v-if="$slots.actions"
      class="ml-auto flex shrink-0 items-center gap-1"
    >
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { storeToRefs } from 'pinia'
import { ChevronRight } from 'lucide-vue-next'
import { resolveFileIcon } from '@/components/file-manager/file-icon'
import { useSettingsStore } from '@/store/settings'

const props = defineProps<{
  path: string
}>()

const { isDark } = storeToRefs(useSettingsStore())

const pathSegments = computed(() => {
  const path = props.path || ''
  const display = path.startsWith('/data/') ? path.slice('/data/'.length) : path.replace(/^\//, '')
  return display.split('/').filter(Boolean)
})

const fileName = computed(() => pathSegments.value.at(-1) ?? '')
const fileIcon = computed(() => resolveFileIcon(fileName.value, isDark.value))
</script>

<style scoped>
.seti-icon {
  font-family: 'seti';
  font-size: 0.875rem;
  line-height: 1;
  font-style: normal;
  font-weight: normal;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}
</style>
