<script setup lang="ts">
import { Spinner } from '@felinic/ui'
import { Check, X, ChevronRight } from 'lucide-vue-next'
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BotCreateTerminalLine, BotCreateTerminalLineKind } from '@/composables/api/botCreateTerminal'

const props = defineProps<{
  lines: BotCreateTerminalLine[]
}>()

const { t } = useI18n()

const lineLabelKey: Partial<Record<BotCreateTerminalLineKind, string>> = {
  command: 'bots.create.line.command',
  'bot-created': 'bots.create.line.botCreated',
  pulling: 'bots.create.line.pulling',
  creating: 'bots.create.line.creating',
  restoring: 'bots.create.line.restoring',
  ready: 'bots.create.line.ready',
  'applying-settings': 'bots.create.line.applyingSettings',
  'installing-agent': 'bots.create.line.installingAgent',
}

function labelFor(line: BotCreateTerminalLine): string {
  if (line.kind === 'error') return line.message ?? t('bots.create.failedTitle')
  const key = lineLabelKey[line.kind]
  if (!key) return ''
  return t(key, { name: line.message ?? '' })
}

const scroller = ref<HTMLElement | null>(null)

watch(
  () => props.lines.length,
  async () => {
    await nextTick()
    const el = scroller.value
    if (el) el.scrollTop = el.scrollHeight
  },
)

const hasLines = computed(() => props.lines.length > 0)
</script>

<template>
  <div
    ref="scroller"
    class="max-h-72 overflow-y-auto rounded-lg border border-zinc-800 bg-zinc-950 p-4 font-mono text-xs leading-relaxed text-zinc-100"
    role="log"
    aria-live="polite"
  >
    <p
      v-if="!hasLines"
      class="text-zinc-500"
    >
      &nbsp;
    </p>
    <div
      v-for="line in lines"
      :key="line.id"
      class="flex items-start gap-2 py-0.5"
      :class="{ 'text-zinc-500': line.kind === 'command' }"
    >
      <span class="mt-0.5 flex size-3.5 shrink-0 items-center justify-center">
        <Spinner
          v-if="line.status === 'running'"
          class="size-3.5 text-zinc-300"
        />
        <Check
          v-else-if="line.status === 'done'"
          class="size-3.5 text-emerald-400"
        />
        <X
          v-else-if="line.status === 'error'"
          class="size-3.5 text-red-400"
        />
        <ChevronRight
          v-else
          class="size-3.5 text-zinc-500"
        />
      </span>
      <span
        class="min-w-0 flex-1 break-words"
        :class="{ 'text-red-400': line.status === 'error' }"
      >
        {{ labelFor(line) }}
        <span
          v-if="line.image && line.kind === 'pulling'"
          class="text-zinc-500"
        >· {{ line.image }}</span>
        <span
          v-if="line.kind === 'pulling' && typeof line.percent === 'number' && line.percent > 0"
          class="text-zinc-400 tabular-nums"
        >· {{ line.percent }}%</span>
      </span>
    </div>
  </div>
</template>
