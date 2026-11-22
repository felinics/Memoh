<template>
  <div
    v-if="showScrollRail"
    class="group/rail hidden md:flex absolute inset-y-0 right-4 z-(--z-raised) w-96 flex-col items-end justify-center pointer-events-none"
    @mouseenter="scheduleRailOpen"
    @mouseleave="scheduleRailClose"
  >
    <!-- Collapsed: uniform tick marks -->
    <div
      class="flex max-h-[60vh] flex-col items-end justify-center gap-2 py-2 pointer-events-auto transition-opacity duration-150"
      :class="railOpen ? 'opacity-0 pointer-events-none' : 'opacity-100'"
    >
      <span
        v-for="seg in railSegments"
        :key="seg.id"
        class="h-0.5 w-4 shrink-0 rounded-full transition-colors duration-150"
        :class="seg.id === activeRailId
          ? 'bg-foreground/70'
          : 'bg-muted-foreground/30 group-hover/rail:bg-muted-foreground/55'"
      />
    </div>

    <!-- Expanded: user-prompt select panel -->
    <div
      v-if="railOpen"
      class="absolute right-0 top-1/2 w-80 -translate-y-1/2 overflow-hidden rounded-xl border bg-popover text-popover-foreground shadow-lg pointer-events-auto"
      @mouseenter="scheduleRailOpen"
      @mouseleave="scheduleRailClose"
    >
      <div
        class="max-h-[min(60vh,480px)] overflow-y-auto overscroll-contain p-1.5 outline-none [mask-image:linear-gradient(to_bottom,transparent,black_10px,black_calc(100%-10px),transparent)] scrollbar-none"
      >
        <button
          v-for="seg in railSegments"
          :key="seg.id"
          type="button"
          class="flex h-8 w-full items-center rounded-md px-3 text-left text-[13px] text-foreground hover:bg-[var(--overlay-hover)]"
          @click="selectSegment(seg)"
        >
          <span class="truncate">{{ seg.preview }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { useScroll } from '@vueuse/core'
import type { ChatMessage } from '@/store/chat-list'

// Right-edge scroll rail: collapsed tick marks per user turn, expanding on
// hover into a jump list of message previews. This component only OWNS the
// rail chrome and the active-tick sync; the actual jump (escape-follow +
// scroll tween, see useChatScroll) stays with the parent, which holds the
// scroll machinery — the rail just emits which segment was chosen.

export interface ScrollRailSegment {
  id: string
  label: string
  preview: string
  index: number
}

const props = defineProps<{
  messages: ChatMessage[]
  // The chat scroll container; also observed here to track the active tick.
  scrollEl: Ref<HTMLElement | null> | HTMLElement | null
  // Parent-level visibility gate (pane visible + not booting). Split groups
  // remain rendered together, even though the workbench focuses only one of them.
  enabled: boolean
}>()

const emit = defineEmits<{
  jump: [segment: ScrollRailSegment]
}>()

const scrollElRef = computed<HTMLElement | null>(() => {
  const el = props.scrollEl
  return el && 'value' in el ? el.value : el
})

const railSegments = ref<ScrollRailSegment[]>([])
const activeRailId = ref('')
const railOpen = ref(false)
let railRaf = 0
let railOpenTimer: ReturnType<typeof setTimeout> | null = null
let railCloseTimer: ReturnType<typeof setTimeout> | null = null

function getRailSegmentText(msg: ChatMessage): string {
  if (msg.role === 'user') return msg.text?.trim().replace(/\s+/g, ' ') || ''
  return ''
}

function rebuildRailSegments() {
  const segments: ScrollRailSegment[] = []
  props.messages.forEach((msg) => {
    if (msg.role !== 'user') return
    const preview = getRailSegmentText(msg)
    if (!preview) return
    segments.push({
      id: msg.id,
      label: `Message ${segments.length + 1}`,
      preview,
      index: segments.length,
    })
  })
  railSegments.value = segments
}

function syncActiveRailFromScroll() {
  const root = scrollElRef.value
  if (!root || !railSegments.value.length) return
  const viewAnchor = root.scrollTop + 8
  let best = railSegments.value[0]!.id
  let bestDist = Number.POSITIVE_INFINITY
  for (const seg of railSegments.value) {
    const el = root.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(seg.id)}"]`)
    if (!el) continue
    const top = root.scrollTop + el.getBoundingClientRect().top - root.getBoundingClientRect().top
    const dist = Math.abs(top - viewAnchor)
    if (dist < bestDist) { bestDist = dist; best = seg.id }
  }
  activeRailId.value = best
}

watch(() => props.messages.map(m => `${m.id}:${m.role}`).join('|'), () => {
  rebuildRailSegments()
}, { flush: 'post', immediate: true })

useScroll(scrollElRef, {
  onScroll() {
    if (railRaf) return
    railRaf = requestAnimationFrame(() => {
      railRaf = 0
      syncActiveRailFromScroll()
    })
  },
})

function scheduleRailOpen() {
  if (railCloseTimer) { clearTimeout(railCloseTimer); railCloseTimer = null }
  if (railOpen.value || railOpenTimer) return
  railOpenTimer = setTimeout(() => { railOpen.value = true; railOpenTimer = null }, 80)
}

function scheduleRailClose() {
  if (railOpenTimer) { clearTimeout(railOpenTimer); railOpenTimer = null }
  if (!railOpen.value || railCloseTimer) return
  railCloseTimer = setTimeout(() => { railOpen.value = false; railCloseTimer = null }, 150)
}

const showScrollRail = computed(() =>
  props.enabled && railSegments.value.length > 1,
)

function selectSegment(seg: ScrollRailSegment) {
  activeRailId.value = seg.id
  railOpen.value = false
  emit('jump', seg)
}

onBeforeUnmount(() => {
  if (railRaf) cancelAnimationFrame(railRaf)
  if (railOpenTimer) clearTimeout(railOpenTimer)
  if (railCloseTimer) clearTimeout(railCloseTimer)
})
</script>
