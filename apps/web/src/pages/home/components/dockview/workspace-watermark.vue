<template>
  <div class="h-full w-full bg-background">
    <PanePlaceholder :title="t('chat.workspaceEmpty')">
      {{ t('chat.workspaceEmptyHint') }}
    </PanePlaceholder>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { PanePlaceholder } from '@felinic/ui'

const { t } = useI18n()
</script>
