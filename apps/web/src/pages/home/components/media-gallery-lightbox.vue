<template>
  <Teleport to="body">
    <Transition name="lightbox">
      <div
        v-if="isOpen"
        ref="dialogRef"
        class="fixed inset-0 z-(--z-top) flex items-center justify-center"
        :class="overlayClass"
        role="dialog"
        data-keyboard-scope="mediaLightbox"
        aria-modal="true"
        :aria-label="currentItem?.name ? `Media preview: ${currentItem.name}` : 'Media preview'"
        tabindex="-1"
        @click.self="close"
      >
        <!-- Close. 这三个圆形控制钮不走 Button shape="circle":它们是悬浮在
             媒体遮罩上的浮层按钮,墨色随 appearance(dark=白系/frost=前景系)
             切换,Button 的 variant 体系没有这一档;rounded-full 几何与
             circle 令牌一致,只是 chrome 关系不同,留在本地。 -->
        <button
          ref="closeButtonRef"
          type="button"
          class="absolute right-4 top-4 z-(--z-raised) rounded-full p-2 transition-colors"
          :class="controlClass"
          aria-label="Close"
          @click="close"
        >
          <X
            class="size-6"
          />
        </button>

        <!-- Prev -->
        <button
          v-if="items.length > 1"
          type="button"
          class="absolute left-4 top-1/2 -translate-y-1/2 z-(--z-raised) rounded-full p-3 transition-colors"
          :class="controlClass"
          aria-label="Previous"
          @click.stop="prev"
        >
          <ChevronLeft
            class="size-6"
          />
        </button>

        <!-- Next -->
        <button
          v-if="items.length > 1"
          type="button"
          class="absolute right-4 top-1/2 -translate-y-1/2 z-(--z-raised) rounded-full p-3 transition-colors"
          :class="controlClass"
          aria-label="Next"
          @click.stop="next"
        >
          <ChevronRight
            class="size-6"
          />
        </button>

        <!-- Media content: clicking the image (or the surrounding gap) closes;
             only the video keeps its own click so the controls stay usable. -->
        <div
          class="lightbox-media max-w-[90vw] max-h-[90vh] flex items-center justify-center"
          @click.self="close"
        >
          <img
            v-if="currentItem?.type === 'image'"
            :src="currentItem.src"
            :alt="currentItem.name || 'image'"
            class="max-w-full max-h-[90vh] object-contain select-none cursor-zoom-out"
            draggable="false"
            @click="close"
          >
          <video
            v-else-if="currentItem?.type === 'video'"
            :src="currentItem.src"
            controls
            class="max-w-full max-h-[90vh] object-contain"
            @click.stop
          />
        </div>

        <!-- Counter -->
        <div
          v-if="items.length > 1"
          class="absolute bottom-4 left-1/2 -translate-x-1/2 px-3 py-1 rounded-full text-xs"
          :class="counterClass"
        >
          {{ (openIndex ?? 0) + 1 }} / {{ items.length }}
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, watchEffect, nextTick, ref } from 'vue'
import { X, ChevronLeft, ChevronRight } from 'lucide-vue-next'
import { useKeyboardCommand } from '@/composables/useKeyboardCommand'
import { appKeyboardCommands } from '@/lib/keyboard-commands'

export interface MediaGalleryItem {
  src: string
  type: 'image' | 'video'
  name?: string
}

const props = withDefaults(defineProps<{
  items: MediaGalleryItem[]
  openIndex: number | null
  // 'dark' is the classic media viewer (sent images). 'frost' is a light,
  // blurred glass scrim for previewing attachments before they're sent.
  appearance?: 'dark' | 'frost'
}>(), {
  appearance: 'dark',
})

const overlayClass = computed(() =>
  props.appearance === 'frost' ? 'bg-background/80 backdrop-blur-xl' : 'bg-black/90',
)
const controlClass = computed(() =>
  props.appearance === 'frost'
    ? 'text-foreground/60 hover:bg-foreground/10 hover:text-foreground'
    : 'text-white/80 hover:bg-white/10 hover:text-white',
)
const counterClass = computed(() =>
  props.appearance === 'frost' ? 'bg-foreground/10 text-foreground' : 'bg-black/50 text-white/90',
)

const emit = defineEmits<{
  'update:openIndex': [value: number | null]
}>()

const isOpen = computed(() => props.openIndex !== null && props.items.length > 0)
const closeButtonRef = ref<HTMLButtonElement | null>(null)
const dialogRef = ref<HTMLElement | null>(null)
let previousFocusedElement: HTMLElement | null = null

const currentItem = computed(() => {
  const idx = props.openIndex
  if (idx === null || idx < 0 || idx >= props.items.length) return null
  return props.items[idx] ?? null
})

function close() {
  emit('update:openIndex', null)
}

function prev() {
  if (props.openIndex === null) return
  const idx = props.openIndex <= 0 ? props.items.length - 1 : props.openIndex - 1
  emit('update:openIndex', idx)
}

function next() {
  if (props.openIndex === null) return
  const idx = props.openIndex >= props.items.length - 1 ? 0 : props.openIndex + 1
  emit('update:openIndex', idx)
}

watchEffect(() => {
  if (isOpen.value) {
    previousFocusedElement = document.activeElement instanceof HTMLElement ? document.activeElement : null
    nextTick(() => {
      closeButtonRef.value?.focus()
    })
  } else {
    previousFocusedElement?.focus()
    previousFocusedElement = null
  }
})

// Scoped to "lightbox open": each handler returns false while closed so the
// dispatcher keeps iterating (e.g. lets Escape fall through to a global). When
// the lightbox is open we claim the key, fire the action, and preventDefault.
function claim(action: () => void): boolean {
  if (!isOpen.value) return false
  action()
  return true
}

useKeyboardCommand(appKeyboardCommands.closeMediaLightbox, () => claim(close))
useKeyboardCommand(appKeyboardCommands.mediaLightboxPrev, () => claim(prev))
useKeyboardCommand(appKeyboardCommands.mediaLightboxNext, () => claim(next))
</script>

<style scoped>
.lightbox-enter-active,
.lightbox-leave-active {
  transition: opacity 0.18s ease;
}
.lightbox-enter-from,
.lightbox-leave-to {
  opacity: 0;
}
/* The media scales in from slightly small for a quick, light pop — the scrim
   only fades, so the motion reads on the image rather than the whole screen. */
.lightbox-enter-active .lightbox-media,
.lightbox-leave-active .lightbox-media {
  transition: transform 0.18s cubic-bezier(0.16, 1, 0.3, 1);
}
.lightbox-enter-from .lightbox-media,
.lightbox-leave-to .lightbox-media {
  transform: scale(0.94);
}
@media (prefers-reduced-motion: reduce) {
  .lightbox-enter-active .lightbox-media,
  .lightbox-leave-active .lightbox-media {
    transition: none;
  }
  .lightbox-enter-from .lightbox-media,
  .lightbox-leave-to .lightbox-media {
    transform: none;
  }
}
</style>
