<template>
  <!-- 详情页 Information 区的"标签+值"条目。抽取前多个详情页
       各自内联一份逐字节相同的 defineComponent(h()) 实现,靠手工保持同步。 -->
  <div class="space-y-1">
    <p class="text-sm text-muted-foreground">
      {{ label }}
    </p>
    <p class="break-words text-sm font-medium">
      {{ value }}
    </p>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  label: string
  value: string
}>()
</script>
