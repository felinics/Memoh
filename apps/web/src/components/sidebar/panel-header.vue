<template>
  <!-- sidebar 面板组标题行 owner。抽出前 files-pane / panel-schedule /
       panel-sessions 三处手抄同一套标签字型(text-xs font-[550]
       tracking-[-0.02em] muted/80),files-pane 头注释自己承认在"手工同步"。
       owner 只收:行结构(flex shrink-0 items-center select-none)+ 标签字型。
       容器几何(px-2 / mx-1 / mt-2 / h-8 / min-h)是各面板对齐各自图标列的
       调校值,经根 class 透传;标签缩进(pl-[11px] 对齐 19px 标签列,或
       files-pane 的容器级缩进对齐 chevron 列)经 labelClass / 根 class 传入。
       folders/recents 的可折叠分组标题是另一种关系(整行可点的组头),
       归 group-header.vue,不用本组件。 -->
  <div class="flex shrink-0 select-none items-center">
    <slot name="label">
      <span
        :class="[labelTypeClass, labelClass]"
      >
        {{ label }}
      </span>
    </slot>
    <slot />
  </div>
</template>

<script setup lang="ts">
// 标签字型是 owner 级刻意墨色/字重,不属于页面注入
const labelTypeClass = 'min-w-0 flex-1 truncate text-xs font-[550] tracking-[-0.02em] text-muted-foreground/80' /* ui-allow-style */

withDefaults(defineProps<{
  label?: string
  // 标签自身的缩进调校(如 pl-[11px] 对齐标签列);files-pane 把缩进放在
  // 容器上对齐 chevron 列,则此项留空
  labelClass?: string
}>(), {
  label: '',
  labelClass: '',
})
</script>
