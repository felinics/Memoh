<template>
  <p
    v-if="!sessionId"
    class="px-4 py-6 text-center text-body text-muted-foreground"
  >
    {{ t('chat.infoNoSession') }}
  </p>

  <div
    v-else
    class="relative flex flex-col gap-3 p-4"
  >
    <!-- Header actions follow the message action bar: icon-sm ghost buttons
         2px apart, tooltips instant and owned by their trigger. The group sits
         at the dialog close-button inset (half the panel padding), which lands
         the last glyph on the content edge. -->
    <TooltipProvider
      :delay-duration="0"
      :disable-hoverable-content="true"
    >
      <div class="absolute top-2 right-2 flex items-center gap-0.5">
        <!-- Compaction rewrites earlier history into a summary, so it asks
             first; the confirm opens under its button, right-aligned to it and growing
             down-left, since the button sits at the card's right edge. -->
        <Tooltip
          v-if="compactionAvailable"
          ignore-non-keyboard-focus
        >
          <ConfirmPopover
            :title="t('chat.compactConfirmTitle')"
            :message="t('chat.compactConfirmMessage')"
            :confirm-text="t('chat.compactConfirmAction')"
            :cancel-text="t('common.cancel')"
            :loading="isCompacting"
            align="end"
            @confirm="compact"
          >
            <template #trigger>
              <TooltipTrigger as-child>
                <Button
                  variant="ghost"
                  tone="muted"
                  size="icon-sm"
                  :loading="isCompacting"
                  :aria-label="t('chat.compactHint')"
                >
                  <CheckDrawIcon v-if="compacted" />
                  <FoldVertical v-else />
                </Button>
              </TooltipTrigger>
            </template>
          </ConfirmPopover>
          <TooltipContent
            side="top"
            class="max-w-64"
          >
            {{ isCompacting ? t('chat.compactingContext') : compacted ? t('chat.compactSuccess') : t('chat.compactHint') }}
          </TooltipContent>
        </Tooltip>
        <Tooltip ignore-non-keyboard-focus>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              tone="muted"
              size="icon-sm"
              :aria-label="t('chat.lifecycle.title')"
              @click="emit('openLifecycle')"
            >
              <ScanSearch />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="top">
            {{ t('chat.lifecycle.title') }}
          </TooltipContent>
        </Tooltip>
      </div>
    </TooltipProvider>
    <div
      class="flex items-center gap-2"
      :class="compactionAvailable ? 'pr-15' : 'pr-6'"
    >
      <span
        v-if="contextWindow != null"
        class="text-control font-medium tabular-nums"
        :class="toneClass"
      >{{ percent }}%</span>
      <span class="min-w-0 flex-1 truncate text-body text-muted-foreground tabular-nums">{{ tokensLabel }}</span>
    </div>

    <ContextWaffle
      v-if="contextWindow != null"
      :percent="contextPercent"
      :groups="groups"
      :columns="20"
    />

    <div
      v-if="groups.length"
      class="flex flex-wrap gap-x-4 gap-y-1 text-body"
    >
      <span
        v-for="group in groups"
        :key="group.id"
        class="inline-flex items-center gap-1.5"
      >
        <span
          class="size-2 shrink-0 rounded-2xs"
          :class="group.colorClass"
        />
        <span class="text-muted-foreground">{{ t(`chat.contextGroup.${group.id}`) }}</span>
        <span class="text-foreground tabular-nums">{{ formatTokenCount(group.tokens) }}</span>
      </span>
    </div>

    <p
      v-if="nearlyFull"
      class="text-body"
      :class="toneClass"
    >
      {{ t('chat.infoNearlyFull') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, ConfirmPopover, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@felinic/ui'
import { FoldVertical, ScanSearch } from 'lucide-vue-next'
import CheckDrawIcon from '@/components/check-draw-icon/index.vue'
import { useSessionInfo } from '../composables/useSessionInfo'
import { contextPressureToneClass, formatTokenCount } from '../composables/context-categories'
import { groupContextCategories } from '../composables/context-groups'
import ContextWaffle from './context-waffle.vue'

const emit = defineEmits<{ openLifecycle: [] }>()

const props = defineProps<{
  visible: boolean
  overrideModelId?: string
  fallbackContextWindow?: number | null
}>()

const { t } = useI18n()

const { composition, contextWindow, contextTokens, contextPercent, autoCompactTokens, compactionAvailable, isCompacting, triggerCompact, sessionId } = useSessionInfo({
  visible: toRef(props, 'visible'),
  overrideModelId: computed(() => props.overrideModelId ?? ''),
  fallbackContextWindow: computed(() => props.fallbackContextWindow ?? null),
})

// Success shows on the button itself, like copy's check, while the card is
// open; the toast is only for a compaction that finishes after it closed.
const compacted = ref(false)
let alive = true
let compactedTimer: ReturnType<typeof setTimeout> | null = null
onBeforeUnmount(() => {
  alive = false
  if (compactedTimer) clearTimeout(compactedTimer)
})

async function compact() {
  const ok = await triggerCompact({ quietSuccess: () => alive && props.visible })
  if (!ok || !alive) return
  compacted.value = true
  if (compactedTimer) clearTimeout(compactedTimer)
  compactedTimer = setTimeout(() => { compacted.value = false }, 1500)
}

const percent = computed(() => Math.round(contextPercent.value))
const toneClass = computed(() => contextPercent.value >= 70 ? contextPressureToneClass(contextPercent.value, 'text') : '')
const nearlyFull = computed(() => contextWindow.value != null && contextPercent.value >= 70 && autoCompactTokens.value != null)

const tokensLabel = computed(() => {
  const used = formatTokenCount(contextTokens.value)
  if (composition.value) {
    return contextWindow.value != null
      ? t('chat.infoContextTokensEstimate', { used, window: formatTokenCount(contextWindow.value) })
      : t('chat.infoContextTokensEstimateNoWindow', { used })
  }
  return contextWindow.value != null
    ? t('chat.infoContextTokens', { used, window: formatTokenCount(contextWindow.value) })
    : t('chat.infoContextTokensNoWindow', { used })
})

const groups = computed(() => groupContextCategories(composition.value?.categories))

</script>
