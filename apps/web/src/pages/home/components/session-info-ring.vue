<template>
  <Popover v-model:open="open">
    <!-- Use a native element reference: nested tooltip/popover roots must not share their anchor. -->
    <span
      ref="anchorRef"
      v-bind="$attrs"
      class="inline-flex self-center"
    >
      <TooltipProvider :delay-duration="200">
        <Tooltip :disabled="open">
          <TooltipTrigger as-child>
            <PopoverTrigger as-child>
              <!-- TODO(ui): Review persistent open-state highlighting for ghost triggers
                   across the component library. Keep the shared behavior here until
                   that review; any change should cover all affected triggers. -->
              <Button
                variant="ghost"
                tone="muted"
                size="icon-sm"
                class="rounded-sm"
                :class="[
                  contextWindow == null ? 'size-6 max-md:size-11' : 'h-6 w-auto gap-1 pl-1 pr-0.75 max-md:h-11 max-md:px-2',
                  { 'text-foreground': open },
                ]"
                :disabled="!sessionId"
                :aria-label="ringLabel"
              >
                <svg
                  viewBox="0 0 24 24"
                  class="size-3.5 -rotate-90"
                  aria-hidden="true"
                >
                  <circle
                    cx="12"
                    cy="12"
                    :r="radius"
                    fill="none"
                    stroke="currentColor"
                    :stroke-width="strokeWidth"
                    class="opacity-40"
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
                    class="transition-[stroke-dashoffset] motion-reduce:transition-none"
                  />
                </svg>
                <!-- Glanceable number beside the ring; the ring alone only reads as "some". -->
                <span
                  v-if="contextWindow != null"
                  class="text-label leading-none tabular-nums"
                  :class="ringColorClass"
                >{{ Math.round(contextPercent) }}%</span>
              </Button>
            </PopoverTrigger>
          </TooltipTrigger>
          <TooltipContent
            side="top"
            align="end"
          >
            <div>{{ contextWindow == null ? t('chat.sessionInfoRingAria') : t('chat.contextUsedPercent', { percent: Math.round(contextPercent) }) }}</div>
            <div class="opacity-70">
              {{ contextWindow == null
                ? t('chat.infoContextTokensNoWindow', { used: formatTokenCount(contextTokens) })
                : t('chat.contextUsedTokens', { used: formatTokenCount(contextTokens), window: formatTokenCount(contextWindow) }) }}
            </div>
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </span>
    <PopoverContent
      :reference="anchorRef ?? undefined"
      class="flex w-80 flex-col p-0 max-h-(--reka-popover-content-available-height) overflow-hidden"
      align="end"
      side="top"
      :side-offset="8"
      :collision-padding="8"
    >
      <SessionInfoPanel
        :visible="open"
        :override-model-id="overrideModelId"
        :fallback-context-window="fallbackContextWindow"
        @open-lifecycle="openLifecycle"
      />
    </PopoverContent>
  </Popover>
  <!-- The lifecycle dialog outlives the popover that launched it. -->
  <ContextLifecycleDialog
    v-if="lifecycleEverOpened"
    v-model:open="lifecycleOpen"
    @close-auto-focus="restoreTriggerFocus"
  />
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Popover, PopoverContent, PopoverTrigger, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@felinic/ui'
import SessionInfoPanel from './session-info-panel.vue'
import { useSessionInfo } from '../composables/useSessionInfo'
import { contextPressureToneClass, formatTokenCount } from '../composables/context-categories'

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
const anchorRef = ref<HTMLElement | null>(null)

function openLifecycle() {
  open.value = false
  lifecycleEverOpened.value = true
  lifecycleOpen.value = true
}

// The previous focus target lived in the closed popover. Restore focus only
// after the dialog releases its focus trap, and override its default target.
function restoreTriggerFocus(event: Event) {
  event.preventDefault()
  anchorRef.value?.querySelector('button')?.focus()
}

const visibleRef = computed(() => props.visible ?? true)
const overrideModelIdRef = computed(() => props.overrideModelId ?? '')
const fallbackContextWindowRef = computed(() => props.fallbackContextWindow ?? null)
const { contextPercent, contextWindow, contextTokens, sessionId } = useSessionInfo({
  visible: visibleRef,
  overrideModelId: overrideModelIdRef,
  fallbackContextWindow: fallbackContextWindowRef,
})

const radius = 10
const strokeWidth = 3
const circumference = computed(() => 2 * Math.PI * radius)
const dashOffset = computed(() => {
  const pct = Math.max(0, Math.min(100, contextPercent.value))
  return circumference.value * (1 - pct / 100)
})

// Normal usage inherits the button's hover color; pressure keeps its warning tone.
const ringColorClass = computed(() => contextPercent.value >= 70 ? contextPressureToneClass(contextPercent.value, 'text') : '')
const ringLabel = computed(() => (contextWindow.value == null
  ? t('chat.sessionInfoRingAria')
  : t('chat.sessionInfoRingAriaUsage', { percent: Math.round(contextPercent.value) })))

</script>
