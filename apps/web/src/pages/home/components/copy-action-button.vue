<!-- Host i18n adapter over @felinic/ui CopyButton (the implementation moved
     there). Keeps the three host call sites on one import while the library
     takes its copy through props. -->
<template>
  <CopyButton
    v-model:busy="busy"
    :text="text"
    :icon-class="iconClass"
    :label="t('common.copy')"
    :copied-label="t('common.copied')"
    :failed-message="t('common.copyFailed')"
  />
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { CopyButton } from '@felinic/ui'

withDefaults(defineProps<{
  text: string
  iconClass?: string
}>(), { iconClass: 'size-4.5' })

const busy = defineModel<boolean>('busy', { default: false })

const { t } = useI18n()
</script>
