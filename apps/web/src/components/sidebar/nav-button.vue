<template>
  <!-- sidebar 动作/导航行按钮 owner。抽出前同一串 class(h-9 justify-start
       gap-[9px] px-[11px] text-control …)在 sidebar/index(Settings 行)与
       panel-sessions(New Session / Bot Settings)三处逐字节相同——被注释掉的
       "experiment" 版本证明它已经变过一次,趁 0 漂移锁住。
       几何契约:容器 px-2 + 按钮 px-[11px] + 18px 图标把 glyph 放在 x=19、
       label 放在 x=45,与 nav tab / session 行共享图标列;图标由调用方以
       size-[18px] 传入默认插槽。 -->
  <Button
    variant="ghost"
    block
    :class="[rowClass, active ? activeClass : '']"
  >
    <slot />
  </Button>
</template>

<script setup lang="ts">
import { Button } from '@felinic/ui'

// 行几何 + 墨色是 owner 级刻意 chrome,不属于页面注入;px 值是 sidebar
// 图标列的对齐常量(见头注释),不是可换算成 rem 刻度的间距
const rowClass = 'h-9 justify-start gap-[9px] px-[11px] text-control font-medium text-foreground/92 dark:text-[color:oklch(0.86_0_0)] select-none' /* ui-allow-style */ /* ui-allow-px */ /* ui-allow-alpha: sole owner (see header comment) — one consumer doesn't earn a global -soft token */
const activeClass = 'bg-sidebar-accent text-foreground!' /* ui-allow-style */

withDefaults(defineProps<{
  active?: boolean
}>(), {
  active: false,
})
</script>
