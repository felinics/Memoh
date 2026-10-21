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
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useDark } from '@vueuse/core'
import { ChevronRight } from 'lucide-vue-next'
import { resolveFileIcon } from '@/components/file-manager/file-icon'

// Path breadcrumb for file / preview panels only. Sessions, terminals, browsers
// and the desktop panel carry no breadcrumb: the dockview tab already states
// what they are, so a single-segment crumb would only echo the tab. A real path
// is the one case with genuine hierarchy worth showing, since the tab shows just
// the file's base name. Sized to the editor body (13px) so the header reads as
// part of the code surface rather than looming above it.
const props = defineProps<{
  path: string
}>()

const isDark = useDark()

const pathSegments = computed(() => {
  const path = props.path || ''
  // /data/ is the workspace mount root — strip it for a cleaner path.
  const display = path.startsWith('/data/') ? path.slice('/data/'.length) : path.replace(/^\//, '')
  return display.split('/').filter(Boolean)
})

const fileName = computed(() => pathSegments.value.at(-1) ?? '')

const fileIcon = computed(() => resolveFileIcon(fileName.value, isDark.value))
</script>

<style scoped>
.seti-icon {
  font-family: 'seti';
  /* A hair over the 13px label text so the glyph reads as an icon, not a
   * character — trimmed from 16px so the row stays compact. */
  font-size: 14px;
  line-height: 1;
  font-style: normal;
  font-weight: normal;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}
</style>
