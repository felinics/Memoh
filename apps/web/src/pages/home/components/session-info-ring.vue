<template>
  <Popover v-model:open="open">
    <!-- 不走 Button shape="circle":这是承载 SVG 进度环 + hover 展开 popover
         的复合触发器,刻意无 hover 填充(安静的状态环,不是操作钮);
         rounded-full 几何与 circle 令牌一致,chrome 关系不同,留在本地。 -->
    <PopoverTrigger
      ref="triggerRef"
      as="button"
      type="button"
      :class="[
        'inline-flex items-center justify-center size-9 rounded-full text-foreground transition-[opacity,scale] duration-200 ease-out disabled:opacity-50 disabled:pointer-events-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring motion-reduce:transition-none',
        ($attrs.class as string | undefined) ?? '',
      ]"
      :disabled="!sessionId"
      :aria-label="ringLabel"
      @mouseenter="handleMouseEnter"
      @mouseleave="handleMouseLeave"
    >
      <svg
        viewBox="0 0 24 24"
        class="size-6 -rotate-90"
        aria-hidden="true"
      >
        <circle
          cx="12"
          cy="12"
          :r="radius"
          fill="none"
          stroke="currentColor"
          :stroke-width="strokeWidth"
          class="opacity-20"
        />
        <circle
          cx="12"
          cy="12"
          :r="radius"
          fill="none"
          :class="ringColorClass"
          stroke="currentColor"
          stroke-linecap="round"
          :stroke-width="strokeWidth"
          :stroke-dasharray="circumference"
          :stroke-dashoffset="dashOffset"
          class="transition-all"
        />
      </svg>
    </PopoverTrigger>
    <PopoverContent
      class="w-80 p-0 max-h-[60vh] overflow-hidden"
      align="end"
      side="top"
      :side-offset="8"
      @mouseenter="handleContentMouseEnter"
      @mouseleave="handleMouseLeave"
      @open-auto-focus="handleOpenAutoFocus"
    >
      <SessionInfoPanel
        :visible="open"
        :override-model-id="overrideModelId"
        :fallback-context-window="fallbackContextWindow"
        @open-lifecycle="openLifecycle"
      />
    </PopoverContent>
  </Popover>
  <!-- Sibling of the Popover: the modal's pointer-events lock closes the
       hover popover, which unmounts the panel — a dialog nested there would
       unmount with it. -->
  <ContextLifecycleDialog
    v-if="lifecycleEverOpened"
    v-model:open="lifecycleOpen"
  />
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Popover, PopoverContent, PopoverTrigger } from '@felinic/ui'
import SessionInfoPanel from './session-info-panel.vue'
import { useSessionInfo } from '../composables/useSessionInfo'
import { contextPressureToneClass } from '../composables/context-categories'

defineOptions({ inheritAttrs: false })

const ContextLifecycleDialog = defineAsyncComponent(() => import('./context-lifecycle-dialog.vue'))

const props = defineProps<{
  visible?: boolean
  overrideModelId?: string
  fallbackContextWindow?: number | null
}>()

const { t } = useI18n()
const open = ref(false)
const lifecycleOpen = ref(false)
const lifecycleEverOpened = ref(false)
const triggerRef = ref<{ $el?: HTMLElement } | null>(null)

function openLifecycle() {
  clearTimers()
  open.value = false
  lifecycleEverOpened.value = true
  lifecycleOpen.value = true
}

// The dialog's previous focus target lives in the closed popover, so hand
// focus back to the ring instead of letting it fall to the body.
watch(lifecycleOpen, (isOpen) => {
  if (!isOpen) triggerRef.value?.$el?.focus?.()
})

// Hover opens without moving focus; a click or Enter on the trigger lets the
// popover take focus so its actions are reachable from the keyboard.
let openedByHover = false
function handleOpenAutoFocus(event: Event) {
  if (openedByHover) event.preventDefault()
  openedByHover = false
}

const visibleRef = computed(() => props.visible ?? true)
const overrideModelIdRef = computed(() => props.overrideModelId ?? '')
const fallbackContextWindowRef = computed(() => props.fallbackContextWindow ?? null)
const { contextPercent, contextWindow, sessionId } = useSessionInfo({
  visible: visibleRef,
  overrideModelId: overrideModelIdRef,
  fallbackContextWindow: fallbackContextWindowRef,
})

const radius = 10
const strokeWidth = 2.5
const circumference = computed(() => 2 * Math.PI * radius)
const dashOffset = computed(() => {
  const pct = Math.max(0, Math.min(100, contextPercent.value))
  return circumference.value * (1 - pct / 100)
})

const ringColorClass = computed(() => contextPressureToneClass(contextPercent.value, 'text'))
const ringLabel = computed(() => (contextWindow.value == null
  ? t('chat.sessionInfoRingAria')
  : t('chat.sessionInfoRingAriaUsage', { percent: Math.round(contextPercent.value) })))

let openTimer: ReturnType<typeof setTimeout> | null = null
let closeTimer: ReturnType<typeof setTimeout> | null = null

function clearTimers() {
  if (openTimer) {
    clearTimeout(openTimer)
    openTimer = null
  }
  if (closeTimer) {
    clearTimeout(closeTimer)
    closeTimer = null
  }
}

function handleMouseEnter() {
  if (!sessionId.value) return
  if (closeTimer) {
    clearTimeout(closeTimer)
    closeTimer = null
  }
  if (open.value) return
  openTimer = setTimeout(() => {
    openedByHover = true
    open.value = true
    openTimer = null
  }, 150)
}

function handleContentMouseEnter() {
  if (closeTimer) {
    clearTimeout(closeTimer)
    closeTimer = null
  }
}

function handleMouseLeave() {
  if (openTimer) {
    clearTimeout(openTimer)
    openTimer = null
  }
  closeTimer = setTimeout(() => {
    open.value = false
    closeTimer = null
  }, 200)
}

defineExpose({ clearTimers })
</script>
