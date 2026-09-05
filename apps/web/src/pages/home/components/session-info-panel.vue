<template>
  <ScrollArea class="h-full">
    <div class="px-4 py-3">
      <!-- No session -->
      <div
        v-if="!sessionId"
        class="flex h-40 items-center justify-center"
      >
        <p class="text-body text-muted-foreground">
          {{ $t('chat.infoNoData') }}
        </p>
      </div>

      <template v-else>
        <!-- Key-value rows -->
        <div class="divide-y divide-border text-body">
          <!-- Messages -->
          <div class="flex items-center justify-between py-2">
            <span class="text-muted-foreground">{{ $t('chat.infoMessages') }}</span>
            <span class="font-medium text-foreground tabular-nums">{{ info?.message_count ?? '--' }}</span>
          </div>

          <!-- Context Usage -->
          <div class="py-2 space-y-1.5">
            <div class="flex items-center justify-between">
              <span class="text-muted-foreground">{{ $t('chat.infoContextUsage') }}</span>
              <span class="font-medium text-foreground tabular-nums">
                <template v-if="composition">
                  <template v-if="contextWindow != null">
                    {{ $t('chat.infoContextTokensEstimate', { used: formatTokenCount(composition.totalTokens), window: formatTokenCount(contextWindow) }) }}
                  </template>
                  <template v-else>
                    {{ $t('chat.infoContextTokensEstimateNoWindow', { used: formatTokenCount(composition.totalTokens) }) }}
                  </template>
                  <span
                    v-if="contextWindow != null"
                    class="font-normal ml-1"
                    :class="contextPercentColor"
                  >({{ contextPercent.toFixed(1) }}%)</span>
                </template>
                <template v-else-if="contextWindow != null">
                  {{ $t('chat.infoContextTokens', { used: formatTokenCount(usedTokens), window: formatTokenCount(contextWindow) }) }}
                  <span class="text-muted-foreground font-normal ml-1">({{ contextPercent.toFixed(1) }}%)</span>
                </template>
                <template v-else>
                  {{ $t('chat.infoContextTokensNoWindow', { used: formatTokenCount(usedTokens) }) }}
                </template>
              </span>
            </div>
            <ContextUsageBreakdown
              v-if="composition"
              :composition="composition"
              :context-window="contextWindow"
              :output-reserve="outputReserve"
              :auto-compact-tokens="autoCompactTokens"
            />
            <div
              v-else-if="contextWindow != null && contextWindow > 0"
              class="h-1.5 w-full overflow-hidden rounded-full bg-accent"
            >
              <div
                class="h-full rounded-full transition-all"
                :class="contextBarColor"
                :style="{ width: `${Math.min(contextPercent, 100)}%` }"
              />
            </div>
          </div>

          <!-- Provider-reported input of the latest turn: the only actual↔estimate bridge -->
          <div
            v-if="composition && usedTokens > 0"
            class="flex items-center justify-between py-2"
          >
            <span class="text-muted-foreground">{{ $t('chat.infoProviderInput') }}</span>
            <span class="font-medium text-foreground tabular-nums">{{ formatTokenCount(usedTokens) }}</span>
          </div>

          <!-- Cache Hit Rate -->
          <div class="flex items-center justify-between py-2">
            <span class="text-muted-foreground">{{ $t('chat.infoCacheHitRate') }}</span>
            <span class="font-medium text-foreground tabular-nums">{{ cacheHitRate }}%</span>
          </div>

          <!-- Cache Read -->
          <div class="flex items-center justify-between py-2">
            <span class="text-muted-foreground">{{ $t('chat.infoCacheRead') }}</span>
            <span class="font-medium text-foreground tabular-nums">{{ formatTokenCount(info?.cache_stats?.cache_read_tokens ?? 0) }}</span>
          </div>
        </div>

        <!-- Compact Now: only where Memoh owns compaction (native runtime) -->
        <Button
          v-if="compactionAvailable"
          variant="secondary"
          size="sm"
          class="mt-3 w-full"
          :disabled="!sessionId || contextTokens <= 0"
          :loading="isCompacting"
          loading-mode="icon"
          @click="triggerCompact"
        >
          <Minimize2 class="size-3.5" />
          {{ $t('chat.compactNow') }}
        </Button>

        <!-- Trajectory -->
        <Button
          variant="ghost"
          size="sm"
          class="mt-1 w-full"
          :disabled="!sessionId"
          @click="emit('openTrajectory')"
        >
          <Route class="size-3.5" />
          {{ $t('chat.trajectory.open') }}
        </Button>

        <!-- Subagents -->
        <div class="mt-4">
          <SubagentList />
        </div>

        <!-- Skills -->
        <div class="mt-4">
          <p class="mb-1.5 text-caption font-medium uppercase tracking-wider text-muted-foreground">
            {{ $t('chat.infoSkills') }}
          </p>
          <p
            v-if="!skills.length"
            class="text-body text-muted-foreground"
          >
            {{ $t('chat.infoNoSkills') }}
          </p>
          <div
            v-else
            class="space-y-0.5"
          >
            <div
              v-for="skill in skills"
              :key="skill"
              class="flex min-h-8 items-center gap-1.5 rounded-md px-2 text-body text-foreground"
            >
              <Sparkles class="size-3.5 shrink-0 text-muted-foreground" />
              <span class="min-w-0 flex-1 truncate text-left">{{ skill }}</span>
            </div>
          </div>
        </div>
      </template>
    </div>
  </ScrollArea>
</template>

<script setup lang="ts">
import { computed, toRef } from 'vue'
import { ScrollArea, Button } from '@felinic/ui'
import { Sparkles, Minimize2, Route } from 'lucide-vue-next'
import { useSessionInfo } from '../composables/useSessionInfo'
import { contextPressureToneClass, formatTokenCount } from '../composables/context-categories'
import SubagentList from './subagent-list.vue'
import ContextUsageBreakdown from './context-usage-breakdown.vue'

const emit = defineEmits<{ openTrajectory: [] }>()

const props = defineProps<{
  visible: boolean
  overrideModelId?: string
  fallbackContextWindow?: number | null
}>()

const visibleRef = toRef(props, 'visible')
const overrideModelIdRef = computed(() => props.overrideModelId ?? '')
const fallbackContextWindowRef = computed(() => props.fallbackContextWindow ?? null)

const { info, usedTokens, composition, contextWindow, outputReserve, autoCompactTokens, compactionAvailable, contextTokens, contextPercent, sessionId, isCompacting, triggerCompact } = useSessionInfo({
  visible: visibleRef,
  overrideModelId: overrideModelIdRef,
  fallbackContextWindow: fallbackContextWindowRef,
})

const contextPercentColor = computed(() =>
  contextPercent.value >= 70 ? contextPressureToneClass(contextPercent.value, 'text') : 'text-muted-foreground',
)

const contextBarColor = computed(() => contextPressureToneClass(contextPercent.value, 'bg'))

const cacheHitRate = computed(() => {
  const rate = info.value?.cache_stats?.cache_hit_rate ?? 0
  return rate.toFixed(1)
})

const skills = computed(() => info.value?.skills ?? [])
</script>
