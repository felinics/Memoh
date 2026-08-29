<template>
  <!-- 超市列表卡的形状由不同市场资源共用:
       图标槽(#leading)+ 标题 + 主页外链 + 两行截断描述 + 尾部动作槽(#actions)。
       卡高固定:描述框恒占两行(min-h-8 = 2 × text-xs 的 1rem 行高),短描述不塌、长描述
       被 line-clamp-2 省略,所以每张卡的高度都是 p-4 + 标题行 + 两行描述,网格不再参差。
       根是 Card[role=button](两处原本就这么写,天然避开 button 嵌套陷阱),
       整卡点击/回车/空格 → emit('open');#actions 区域和主页外链要同时 stop 掉 click 和
       keydown——根的 keydown.enter.prevent 会吞掉嵌套交互元素的原生回车激活,只 stop click 不够。
       业务(路由跳转、SDK 类型、安装逻辑)留在调用方,这里只own形状。 -->
  <Card
    :class="rootClass"
    role="button"
    tabindex="0"
    @click="$emit('open')"
    @keydown.enter.prevent="$emit('open')"
    @keydown.space.prevent="$emit('open')"
  >
    <!-- overflow-hidden:外链图标图片可能非方形,靠它裁进圆角盒。 -->
    <div class="flex size-9 shrink-0 items-center justify-center overflow-hidden rounded-md bg-accent">
      <slot name="leading" />
    </div>

    <div class="min-w-0 flex-1">
      <div class="flex items-center gap-1.5">
        <h3
          class="truncate text-sm font-medium"
          :title="name"
        >
          {{ name }}
        </h3>
        <a
          v-if="homepage"
          :href="homepage"
          target="_blank"
          rel="noopener noreferrer"
          :class="homepageLinkClass"
          @click.stop
          @keydown.stop
        >
          <ExternalLink class="size-3" />
        </a>
      </div>
      <!-- overflow-wrap:anywhere:描述里的长 URL / 无空格 token 否则会横向顶破卡片 -->
      <p class="mt-1 line-clamp-2 min-h-8 break-words text-xs text-muted-foreground [overflow-wrap:anywhere]">
        {{ description }}
      </p>
      <div
        v-if="$slots.meta"
        class="mt-3 flex min-w-0 flex-wrap items-center gap-2"
      >
        <slot name="meta" />
      </div>
    </div>

    <div
      v-if="$slots.actions"
      class="shrink-0 self-end sm:self-auto"
      @click.stop
      @keydown.stop
    >
      <slot name="actions" />
    </div>
  </Card>
</template>

<script setup lang="ts">
import { ExternalLink } from 'lucide-vue-next'
import { Card } from '@felinic/ui'

// 整卡可点，hover 是 owner 级交互反馈。
const rootClass = 'group flex cursor-pointer flex-col items-stretch gap-3 p-4 transition-colors hover:border-foreground/20 hover:bg-accent/20 sm:flex-row sm:items-start' /* ui-allow-style */
const homepageLinkClass = 'shrink-0 text-muted-foreground transition-colors hover:text-foreground' /* ui-allow-style */

defineProps<{
  name?: string
  description?: string
  homepage?: string
}>()

defineEmits<{
  open: []
}>()
</script>
