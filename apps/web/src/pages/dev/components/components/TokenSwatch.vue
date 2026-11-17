<script setup lang="ts">
// One design-token swatch. Fills a block with `var(--color-<name>)` so it
// always resolves (every token is declared in @theme inline; dynamic Tailwind
// classes like `bg-${name}` would NOT — JIT can't see them). Also reads the
// resolved value via getComputedStyle for the CURRENT theme + color scheme,
// re-reading whenever the injected `wallThemeVersion` bumps.
import { computed, inject, onMounted, ref, watch, type Ref } from 'vue'
import { isForeground } from '../lib/token-catalog'

const props = defineProps<{
  /** Token base name without the `--color-` prefix. */
  name: string
}>()

const cssVar = computed(() => `--color-${props.name}`)
const fg = computed(() => isForeground(props.name))

const themeVersion = inject<Ref<number>>('wallThemeVersion', ref(0))
const resolved = ref('')

function readValue() {
  resolved.value = getComputedStyle(document.documentElement)
    .getPropertyValue(cssVar.value)
    .trim()
}

onMounted(readValue)
watch(themeVersion, readValue)
</script>

<template>
  <div class="flex flex-col gap-1 overflow-hidden rounded-md border border-border bg-card">
    <!-- Foreground tokens: show text color on card; others: filled block. -->
    <div
      v-if="fg"
      class="flex h-12 items-center justify-center bg-card text-base font-semibold"
      :style="{ color: `var(${cssVar})` }"
    >
      Ag
    </div>
    <div
      v-else
      class="h-12 w-full"
      :style="{ background: `var(${cssVar})` }"
    />
    <div class="flex flex-col gap-0.5 px-2 pb-1.5">
      <code class="text-[10px] font-mono text-foreground break-all">{{ name }}</code>
      <code class="text-[9px] font-mono text-muted-foreground break-all">{{ resolved || '—' }}</code>
    </div>
  </div>
</template>
