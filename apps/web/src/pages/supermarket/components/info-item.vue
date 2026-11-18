<template>
  <!-- 详情页 Information 区的"标签+值"条目。抽取前多个详情页
       各自内联一份逐字节相同的 defineComponent(h()) 实现,靠手工保持同步。 -->
  <div class="space-y-1">
    <p class="text-sm text-muted-foreground">
      {{ label }}
    </p>
    <p
      v-if="externalHref"
      class="break-all text-sm font-medium"
    >
      <a
        :href="externalHref"
        target="_blank"
        rel="noopener noreferrer"
        class="inline text-foreground underline decoration-border underline-offset-4 hover:decoration-foreground"
      >{{ value }}<ExternalLink
        class="ml-1 inline size-3.5 align-text-bottom text-muted-foreground"
        aria-hidden="true"
      /></a>
    </p>
    <p
      v-else
      class="break-words text-sm font-medium"
    >
      {{ value }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExternalLink } from 'lucide-vue-next'

const props = defineProps<{
  label: string
  value: string
  href?: string
}>()

const externalHref = computed(() => {
  const url = props.href?.trim() ?? ''
  return url.startsWith('https://') || url.startsWith('http://') ? url : ''
})
</script>
