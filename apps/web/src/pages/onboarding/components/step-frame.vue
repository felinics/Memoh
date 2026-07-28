<template>
  <!-- wizard 步骤帧的 owner:35rem 盒 + 标题结构。退出动画壳归 StepExitShell
       (每个 Step*.vue 的模板根),这里不再拥有——Step3 三个 frame 实例共用
       一个壳,壳只能在 frame 之上。
       标题 mb 是逐页单独调过的视觉位置(不是漂移),由调用方经 titleClass
       传入,owner 不强行统一。 -->
  <div
    class="text-left h-[35rem] max-h-[calc(100dvh-7rem)] flex flex-col"
    :class="bodyClass"
  >
    <h2
      v-if="title"
      class="text-3xl font-semibold transition-all duration-[350ms] ease-out"
      :class="[titleClass, visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3']"
    >
      {{ title }}
    </h2>
    <slot name="header" />
    <slot />
  </div>
</template>

<script setup lang="ts">
import type { HTMLAttributes } from 'vue'

withDefaults(defineProps<{
  title?: string
  visible?: boolean
  // 标题下边距,逐页单独调的值(Step2/Step4 mb-8 / Step3 list mb-3)
  titleClass?: string
  // Step3 renders three mode variants (list/form/acp), each with its own
  // scale-fade transition on the body div (a second animation layer on top
  // of the page-level StepExitShell). Currently Step3-only; other steps
  // leave it unset. Passed through as-is (array/object/string, same as a
  // native :class binding) so the caller's literal Tailwind classes stay
  // scannable — never built by string concatenation. Typed as the native
  // class binding's own type rather than unknown: vue-tsc 3.3 rejects
  // unknown here, and this is what the binding actually accepts.
  bodyClass?: HTMLAttributes['class']
}>(), {
  title: '',
  visible: false,
  titleClass: 'mb-6',
  bodyClass: undefined,
})
</script>
