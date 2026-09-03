<template>
  <!--
    Runtime line — the turn-level "alive" signal that rides with the composer
    instead of scrolling away with the transcript. Process-group headers
    answer "what is it doing" inside the message flow; this line stays put and
    answers "is it still working" with signals that MOVE rather than a fake
    percentage: the clock ticks and the step count grows. While the turn is
    blocked on the user (approval / ask_user) the caller hides it — the panel
    above is the one telling that story, and two status surfaces disagreeing
    is worse than one. Token throughput would be the stronger "model is alive"
    signal, but the stream does not carry usage yet; elapsed + steps are the
    honest client-side proxies until it does.
  -->
  <div
    class="flex items-center gap-1.5 px-1.5 pb-1.5 text-xs text-muted-foreground select-none"
    role="status"
  >
    <span class="tool-shimmer-text">{{ t('chat.runtime.working') }}</span>
    <template v-if="elapsedLabel">
      <span aria-hidden="true">·</span>
      <span class="font-mono">{{ elapsedLabel }}</span>
    </template>
    <template v-if="steps > 0">
      <span aria-hidden="true">·</span>
      <span>{{ t('chat.process.steps', { count: steps }) }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  active: boolean
  // Turn start in epoch ms when known (the streaming turn's timestamp); null
  // when the turn's start is not observable (e.g. an unbound composer stream),
  // in which case the line stamps its own start when `active` flips on.
  startedAt?: number | null
  // Tool calls the turn has run so far.
  steps?: number
}>(), {
  startedAt: null,
  steps: 0,
})

const { t } = useI18n()

const now = ref(Date.now())
let selfStart = 0
let timer: ReturnType<typeof setInterval> | null = null

function clearTimer() {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
}

watch(
  () => props.active,
  (active) => {
    clearTimer()
    if (!active) return
    selfStart = Date.now()
    now.value = Date.now()
    // One tick per second is the calmest cadence that still proves "alive";
    // only this line re-renders (the ref is local), not the dock around it.
    timer = setInterval(() => { now.value = Date.now() }, 1000)
  },
  { immediate: true },
)

onBeforeUnmount(clearTimer)

// Symbol units (s/m/h) are locale-neutral runtime chrome, deliberately not
// i18n'd — they name units, not sentences.
const elapsedLabel = computed(() => {
  if (!props.active) return ''
  const start = props.startedAt ?? selfStart
  if (!start) return ''
  const seconds = Math.max(0, Math.floor((now.value - start) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, '0')}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${String(minutes % 60).padStart(2, '0')}m`
})
</script>
