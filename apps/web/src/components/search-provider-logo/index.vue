<template>
  <span
    class="inline-flex shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground"
    :class="containerClass"
  >
    <component
      :is="iconComponent"
      v-if="iconComponent"
      :size="iconSize"
    />
    <Globe
      v-else
      :class="iconSizeClass"
    />
  </span>
</template>

<script setup lang="ts">
import { Globe } from 'lucide-vue-next'
import { computed, type Component } from 'vue'
import {
  Brave,
  Bing,
  BingColor,
  Google,
  GoogleColor,
  Yandex,
  Tavily,
  TavilyColor,
  Jina,
  Exa,
  ExaColor,
  Cloudflare,
  Firecrawl,
  Bocha,
  Duckduckgo,
  Searxng,
  Sogou,
  Serper,
} from '@memohai/icon'

const searchIcons: Record<string, Component> = {
  brave: Brave,
  bing: BingColor,
  'bing-mono': Bing,
  google: GoogleColor,
  'google-mono': Google,
  yandex: Yandex,
  tavily: TavilyColor,
  'tavily-mono': Tavily,
  jina: Jina,
  exa: ExaColor,
  'exa-mono': Exa,
  cloudflare: Cloudflare,
  cloudflare_markdown: Cloudflare,
  firecrawl: Firecrawl,
  bocha: Bocha,
  duckduckgo: Duckduckgo,
  searxng: Searxng,
  sogou: Sogou,
  serper: Serper,
}

const props = withDefaults(defineProps<{
  provider: string
  size?: 'xs' | 'sm' | 'md' | 'lg'
}>(), {
  size: 'sm',
})

const iconComponent = computed<Component | undefined>(() =>
  searchIcons[props.provider?.trim().toLowerCase()],
)

const containerClass = computed(() => {
  switch (props.size) {
    case 'xs': return 'size-5'
    case 'sm': return 'size-7'
    case 'md': return 'size-8'
    case 'lg': return 'size-10'
    default: return 'size-7'
  }
})

const iconSize = computed(() => {
  switch (props.size) {
    case 'xs': return '0.625em'
    case 'sm': return '1em'
    case 'md': return '1.25em'
    case 'lg': return '1.5em'
    default: return '1em'
  }
})

const iconSizeClass = computed(() => {
  switch (props.size) {
    case 'xs': return 'size-2.5'
    case 'sm': return 'size-3.5'
    case 'md': return 'size-4'
    case 'lg': return 'size-5'
    default: return 'size-3.5'
  }
})
</script>
