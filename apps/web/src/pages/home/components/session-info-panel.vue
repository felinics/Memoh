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
    <!-- The Inspector button follows the dialog close-button precedent: its
         hover background is inset equally from the top and right edges
         (half the panel padding), which also lands the glyph on the content
         edge and leaves the same gap down to the grid. -->
    <TooltipProvider :delay-duration="200">
      <Tooltip ignore-non-keyboard-focus>
        <TooltipTrigger as-child>
          <Button
            variant="ghost"
            tone="muted"
            size="icon-sm"
            class="absolute top-2 right-2"
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
    </TooltipProvider>
    <div class="flex items-center gap-2 pr-6">
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
import { computed, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@felinic/ui'
import { ScanSearch } from 'lucide-vue-next'
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

const { composition, contextWindow, contextTokens, contextPercent, autoCompactTokens, sessionId } = useSessionInfo({
  visible: toRef(props, 'visible'),
  overrideModelId: computed(() => props.overrideModelId ?? ''),
  fallbackContextWindow: computed(() => props.fallbackContextWindow ?? null),
})

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
