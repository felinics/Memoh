<script setup lang="ts">
import { ref } from 'vue'
import { Button, PageShell } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import BuiltinConfig from './components/builtin-config.vue'

const { t } = useI18n()

// The built-in config owns the model draft + save; the Save button lives in
// this page's header (#actions), so read its state off the child instead of
// hoisting all the memory logic up here.
const builtinRef = ref<InstanceType<typeof BuiltinConfig> | null>(null)
</script>

<template>
  <PageShell :title="t('sidebar.memory')">
    <!-- Root-page manual save: picking an embedding model provisions an index
         backend, so it batches behind one deliberate Save rather than
         auto-saving. It lives in the header (disabled while synced) — the
         house pattern for a PageShell page — not a footer band inside the
         card. -->
    <template #actions>
      <Button
        :disabled="!builtinRef?.hasChanges || builtinRef?.saveLoading"
        :loading="builtinRef?.saveLoading"
        @click="builtinRef?.save()"
      >
        {{ t('common.saveChanges') }}
      </Button>
    </template>

    <BuiltinConfig ref="builtinRef" />
  </PageShell>
</template>
