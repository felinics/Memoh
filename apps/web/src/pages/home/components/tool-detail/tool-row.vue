<template>
  <!--
    Title row of one tool step: action label, target (a file link when it can
    open in the file manager), execution location, +N/-M counts, and the expand
    chevron when the row has a detail to open. tool-call-inline renders a call
    through it, and a successful apply_patch renders one per file, so a patched
    file's row is the same markup as an edit's rather than a copy of it.
    The default slot holds status labels (shown before the chevron), #leading
    the connector logo, #trailing the elapsed time.
  -->
  <component
    :is="expandable ? HeaderRow : 'div'"
    v-bind="expandable ? { open, nested: true } : { class: staticRowClass }"
    v-on="expandable ? { toggle: () => emit('toggle') } : {}"
  >
    <slot name="leading" />
    <span
      v-if="action"
      class="shrink-0"
      :class="actionClass"
    >{{ action }}</span>
    <button
      v-if="target && openable"
      class="truncate min-w-0 hover:underline cursor-pointer"
      :class="targetClass"
      :title="targetTitle || undefined"
      @click.stop="emit('openTarget')"
    >
      {{ target }}
    </button>
    <span
      v-else-if="target"
      class="truncate min-w-0"
      :class="targetClass"
      :title="targetTitle || undefined"
    >{{ target }}</span>
    <span
      v-if="executionLocation"
      class="shrink-0 text-muted-foreground"
      :title="t('chat.tools.executionLocation')"
    >· {{ executionLocation }}</span>
    <span
      v-if="add"
      class="font-mono shrink-0 text-success-foreground"
    >+{{ add }}</span>
    <span
      v-if="remove"
      class="font-mono shrink-0 text-destructive"
    >-{{ remove }}</span>
    <slot />
    <ExpandChevron
      v-if="expandable"
      :open="open"
      class="ml-0.5"
    />
    <slot name="trailing" />
  </component>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import HeaderRow from './header-row.vue'
import ExpandChevron from './expand-chevron.vue'

defineProps<{
  expandable?: boolean
  open?: boolean
  action?: string
  actionClass?: string
  target?: string
  targetClass?: string
  targetTitle?: string
  openable?: boolean
  executionLocation?: string
  add?: number
  remove?: number
}>()
const emit = defineEmits<{ toggle: [], openTarget: [] }>()
const { t } = useI18n()

// 工具标题是执行过程摘要。Agent 在虚拟机中试错、检查并修复命令是正常的
// 长任务行为；非零退出码（包括 -1）或工具 isError 不等于用户任务失败。
// 标题保持中性色，不附加退出码或错误染色；诊断留在展开详情中，真正的
// 任务失败由回合级错误反馈表达，不能从某一次工具调用推导。
// (The expandable variant gets the same ink from HeaderRow's default tone;
// this row owns the static variant's, as HeaderRow owns its own.)
const staticRowClass = 'flex items-center gap-1.5 w-full py-px text-cop-title hover:text-foreground transition-colors duration-75' /* ui-allow-style */
</script>
