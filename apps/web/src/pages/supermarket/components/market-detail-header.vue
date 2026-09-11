<template>
  <!-- 超市 App 详情页头:图标盒 + 标题(右侧是安装按钮)+ 标签。返回行由外层 DetailPane 提供。
       图标盒带 overflow-hidden,将可能非方形的外链图片裁进圆角盒。
       标签容器与原实现一致地始终渲染(即使为空),保持 space-y-4 的节奏不变。
       installToBot 文案焊死在这里是因为两个调用方语义完全一致;若将来出现
       "装到 bot"说不通的第三种条目详情页,应分叉新组件而非在这里加文案分支。 -->
  <div>
    <header class="space-y-4">
      <div class="flex items-start gap-4">
        <div :class="iconBoxClass">
          <slot name="icon" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-3">
            <h1 class="break-words text-3xl font-semibold leading-tight">
              {{ name }}
            </h1>
            <Badge
              v-if="version"
              variant="secondary"
              font="mono"
            >
              v{{ version }}
            </Badge>
          </div>
          <p
            v-if="subtitle"
            class="mt-1 text-sm text-muted-foreground"
          >
            {{ subtitle }}
          </p>
        </div>
        <Button
          size="sm"
          class="shrink-0"
          @click="$emit('install')"
        >
          <Download class="size-4" />
          {{ $t('supermarket.installToBot') }}
        </Button>
      </div>

      <div class="flex flex-wrap gap-1.5">
        <Badge
          v-for="tag in tags"
          :key="tag"
          variant="secondary"
          size="sm"
        >
          {{ tag }}
        </Badge>
      </div>
    </header>
  </div>
</template>

<script setup lang="ts">
import { Download } from 'lucide-vue-next'
import { Badge, Button } from '@felinic/ui'

// 抽取前两页图标盒原样带的浅投影,随形状一起搬入 owner
const iconBoxClass = 'flex size-16 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-background shadow-sm' /* ui-allow-style */

defineProps<{
  name?: string
  tags?: string[]
  version?: string
  subtitle?: string
}>()

defineEmits<{
  install: []
}>()
</script>
