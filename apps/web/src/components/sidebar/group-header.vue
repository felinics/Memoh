<template>
  <!-- sidebar 分组标题的交互 owner:每个 folder 和「文件夹」「最近」区头
       共用的一套交互逻辑——整行点击折叠/展开(click + Enter/Space +
       aria-expanded),chevron 常驻在名字右侧、随展开态旋转,trailing 动作
       在行最右、hover/focus 才显现。
       owner 只收交互与行几何(11px gutter;folder 行 34px 行高,区头
       compact 保持改造前的紧凑节奏);视觉语言(label 字型、leading 图标、
       是否有 hover 填充)是调用方各自的本地样式,经 labelClass / #leading /
       hoverFill 传入——folder 行是带图标和 hover pill 的列表行,区头是
       安静的小标签,两者样式刻意不同,统一的是交互。
       trailing 按钮由调用方自带显现类(menu trigger 另加
       data-[state=open]:reka 菜单打开时页面 pointer-events 被关,组头
       hover 失效,trigger 靠自己的 data-[state=open] 保持显现)。 -->
  <div
    role="button"
    tabindex="0"
    :aria-expanded="expanded"
    :class="[rowClass, compact ? compactClass : rowHeightClass, hoverFill ? hoverFillClass : '']"
    @click="emit('toggle')"
    @keydown.enter.prevent="emit('toggle')"
    @keydown.space.prevent="emit('toggle')"
  >
    <slot name="leading" />
    <span :class="['min-w-0 shrink truncate', labelClass]">{{ label }}</span>
    <ChevronRight
      class="ml-1.5 size-3 shrink-0 text-muted-foreground transition-[opacity,rotate] duration-150"
      :class="[revealClass, expanded ? 'rotate-90' : 'rotate-0']"
    />
    <span class="ml-auto flex shrink-0 items-center">
      <slot />
    </span>
  </div>
</template>

<script setup lang="ts">
import { ChevronRight } from 'lucide-vue-next'

// 11px gutter 与 session-item.vue 的行几何一致;px 值是 sidebar 行系统的
// 对齐常量,不可换算 rem
const rowClass = 'group/group-header relative flex w-full cursor-pointer select-none items-center rounded-[9px] px-[11px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring' /* ui-allow-px: matches session-item.vue's 11px sidebar row gutter */

// 34px 行高只给 folder 行(与会话行同体系);compact 的区头(文件夹/最近)
// 固定 26px——与改造前 TextButton 区头的块高一致(文字 16 + 上下各 5),
// trailing 按钮(12px icon + py-5 = 22px)装得下,不会把行撑高,
// 区头文字位置与改造前逐像素一致
const rowHeightClass = 'min-h-[2.125rem]'
const compactClass = 'h-[26px]' /* ui-allow-px: pins the old TextButton section header's 26px block height (16px text + 2×5px) so header text position is unchanged */

// 与 session-item.vue 相同的 hover token;folder 行用,「最近」区头不用
const hoverFillClass = 'transition-colors hover:bg-[color:var(--sidebar-hover)]' /* ui-allow-style: sidebar rows are a deliberately local row system — same hover token as session-item.vue */

// chevron 与 trailing 动作的显现逻辑:hover,或行内元素被键盘聚焦
// (focus-visible)时。刻意不用 focus-within——鼠标点击后焦点留在行内,
// focus-within 会一直成立,chevron 和按钮就变成永久显现。
const revealClass = 'opacity-0 group-hover/group-header:opacity-100 group-focus-visible/group-header:opacity-100'

withDefaults(defineProps<{
  label: string
  expanded: boolean
  // label 的字型由调用方自带(folder 行是 text-control 前景,区头是 text-xs
  // font-[550] 小标签);行内只负责截断
  labelClass?: string
  // 是否带 session 行同款 hover 填充;安静的区头标签不开
  hoverFill?: boolean
  // 紧凑形态:不给 34px 行高,由内容撑开——区头(文件夹/最近)用,
  // 保持改造前 TextButton 区头的纵向节奏;folder 行不用
  compact?: boolean
}>(), {
  labelClass: '',
  hoverFill: false,
  compact: false,
})

const emit = defineEmits<{
  toggle: []
}>()
</script>
