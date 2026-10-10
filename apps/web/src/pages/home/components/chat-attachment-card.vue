<template>
  <div class="group/att relative shrink-0">
    <div
      class="relative size-30 overflow-hidden rounded-lg border border-border bg-surface-composer text-left"
      :class="[
        (interactive || clickable) ? 'transition-colors group-hover/att:border-foreground/20' : '',
        clickable ? 'cursor-pointer' : '',
      ]"
      :role="clickable ? 'button' : undefined"
      :tabindex="clickable ? 0 : undefined"
      :aria-label="clickable ? previewLabel : undefined"
      @click="clickable ? emit('preview') : undefined"
      @keydown.enter.exact.prevent="clickable ? emit('preview') : undefined"
      @keydown.space.prevent="clickable ? emit('preview') : undefined"
    >
      <!-- Image / video cover -->
      <template v-if="kind === 'media'">
        <Skeleton
          v-if="src && !mediaLoaded"
          class="absolute inset-0 size-full rounded-none"
        />
        <video
          v-if="src && video"
          :src="src"
          class="size-full object-cover transition-opacity"
          :class="mediaLoaded ? 'opacity-100' : 'opacity-0'"
          preload="metadata"
          muted
          playsinline
          @loadeddata="mediaLoaded = true"
        />
        <img
          v-else-if="src"
          :src="src"
          :alt="name || ''"
          class="size-full object-cover transition-opacity"
          :class="mediaLoaded ? 'opacity-100' : 'opacity-0'"
          loading="eager"
          decoding="async"
          @load="mediaLoaded = true"
          @error="mediaLoaded = true"
        >
        <div
          v-else
          class="flex size-full items-center justify-center text-muted-foreground"
        >
          <ImageIcon class="size-5" />
        </div>
      </template>

      <!-- Pasted content: a large text paste captured as a card. The body
           previews the raw text and a badge marks its origin; clicking opens
           the full-text viewer. -->
      <template v-else-if="kind === 'pasted'">
        <div class="flex size-full flex-col gap-1 p-2.5">
          <p class="line-clamp-4 flex-1 whitespace-pre-wrap break-all text-caption leading-snug text-muted-foreground">
            {{ text }}
          </p>
          <div class="flex items-center justify-between gap-1">
            <span
              v-if="size != null"
              class="text-caption text-muted-foreground"
            >
              {{ sizeLabel }}
            </span>
            <span class="rounded-sm border border-border px-1.5 py-0.5 text-caption font-medium uppercase tracking-wide text-muted-foreground">
              {{ t('chat.pastedBadge') }}
            </span>
          </div>
        </div>
      </template>

      <!-- File card: a shimmer holds the space while the composer reads the file,
           then the name / line count / badge fade in — so a freshly added file
           "loads then appears" rather than popping in. -->
      <template v-else>
        <div
          v-if="!fileRevealed"
          class="absolute inset-0 flex flex-col gap-2 p-2.5"
        >
          <Skeleton class="h-2.5 w-4/5 rounded-sm" />
          <Skeleton class="h-2.5 w-3/5 rounded-sm" />
          <div class="flex-1" />
          <Skeleton class="h-2.5 w-1/3 rounded-sm" />
        </div>
        <div
          class="flex size-full flex-col gap-0.5 p-2.5 transition-opacity duration-200"
          :class="fileRevealed ? 'opacity-100' : 'opacity-0'"
        >
          <span class="line-clamp-3 break-all text-caption leading-snug text-foreground">
            {{ name }}
          </span>
          <span
            v-if="lines != null"
            class="text-caption text-muted-foreground"
          >
            {{ t('chat.attachmentLines', { count: lines }) }}
          </span>
          <div class="flex-1" />
          <span
            v-if="ext"
            class="self-start rounded-sm border border-border px-1.5 py-0.5 text-caption font-medium uppercase tracking-wide text-muted-foreground"
          >
            {{ ext }}
          </span>
          <FileIcon
            v-else
            class="size-4 text-muted-foreground"
          />
        </div>
      </template>
    </div>

    <!-- Remove (composer only). 不走 Button shape="circle":size-5 角标钮,
         hover 才浮现(opacity-0 group-hover),贴在卡片角上,和普通图标操作钮
         关系不同;rounded-full 几何与 circle 令牌一致,留在本地。 -->
    <button
      v-if="removable"
      type="button"
      :aria-label="resolvedRemoveLabel"
      class="absolute right-1 top-1 flex size-5 items-center justify-center rounded-full border border-border bg-card text-muted-foreground opacity-0 transition-[opacity,color] hover:text-foreground focus-visible:opacity-100 focus-visible:outline-none group-hover/att:opacity-100"
      @click.stop="emit('remove')"
    >
      <X class="size-3" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { X, Image as ImageIcon, File as FileIcon } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { Skeleton } from '@felinic/ui'

const props = defineProps<{
  kind: 'media' | 'file' | 'pasted'
  src?: string
  video?: boolean
  name?: string
  ext?: string
  lines?: number | null
  text?: string
  size?: number
  loading?: boolean
  removable?: boolean
  interactive?: boolean
  clickable?: boolean
  removeLabel?: string
}>()

const emit = defineEmits<{ remove: []; preview: [] }>()
const { t } = useI18n()

const resolvedRemoveLabel = computed(() =>
  props.removeLabel
  || (props.name ? `${t('common.delete')}: ${props.name}` : t('common.delete')),
)

const previewLabel = computed(() =>
  props.name ? `${t('common.preview')}: ${props.name}` : t('common.preview'),
)

const sizeLabel = computed(() => {
  const bytes = props.size ?? 0
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
})

const mediaLoaded = ref(false)
watch(() => props.src, () => {
  // Keep an already-painted frame on screen across an in-place src swap — e.g.
  // an optimistic base64 preview being reconciled to its persisted asset URL
  // (same <img>, same message). The browser holds the old frame until the new
  // src decodes, so skipping the skeleton reset avoids a blank flash. Only the
  // first load (nothing painted yet) falls through to the skeleton.
  if (mediaLoaded.value) return
  mediaLoaded.value = false
})

// File cards hold a shimmer until the composer has finished reading the file
// (its line count), then fade their content in. Two animation frames guarantee
// the shimmer paints once before the swap, so the fade actually plays instead
// of snapping — even when the read resolves in the same tick.
const fileRevealed = ref(false)
watch(
  () => props.loading,
  (loading) => {
    if (loading) {
      fileRevealed.value = false
      return
    }
    requestAnimationFrame(() => requestAnimationFrame(() => { fileRevealed.value = true }))
  },
  { immediate: true },
)
</script>
