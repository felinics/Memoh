<script setup lang="ts">
// Shared composition for dependency and App confirmations. The enclosing dialog
// owns scrolling and confirmation; this component only renders the reviewed plan.
import { computed, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { Alert, AlertTitle, AlertDescription, Button, InlineLoadingRow, SettingsSection, SettingsRow, TextButton } from '@felinic/ui'
import type { WorkspacedepsPlan, WorkspacedepsPlanNode } from '@memohai/sdk'
import DependencyScriptDialog from './dependency-script-dialog.vue'
import type { ScriptResponse } from '@/composables/api/useWorkspaceDependencies'

const props = defineProps<{ plan: WorkspacedepsPlan | null; loading: boolean; error: string }>()
const emit = defineEmits<{ retry: [] }>()
const { t } = useI18n()
const names = computed(() => new Map(props.plan?.nodes?.map(node => [node.dependency_id, node.name || node.dependency_id])))
const scriptOpen = ref(false)
const selected = shallowRef<WorkspacedepsPlanNode | null>(null)
const selectedScript = computed(() => selected.value?.script as ScriptResponse | undefined)
function show(node: WorkspacedepsPlanNode) { selected.value = node; scriptOpen.value = true }
</script>

<template>
  <InlineLoadingRow v-if="loading">
    {{ t('dependenciesPlan.preparing') }}
  </InlineLoadingRow>
  <Alert
    v-else-if="error"
    variant="destructive"
  >
    <AlertTitle>{{ t('dependenciesPlan.failed') }}</AlertTitle>
    <AlertDescription>
      <p>{{ error }}</p>
      <Button
        variant="outline"
        size="sm"
        @click="emit('retry')"
      >
        {{ t('common.retry') }}
      </Button>
    </AlertDescription>
  </Alert>
  <SettingsSection
    v-else-if="plan?.nodes?.length"
    :title="t('dependenciesPlan.title')"
  >
    <SettingsRow
      v-for="node in plan.nodes"
      :key="node.dependency_id"
      stack="sm"
      :label="node.name || node.dependency_id || ''"
      :description="node.required_by?.length ? t('dependenciesPlan.requiredBy', { names: node.required_by.map(id => names.get(id) || id).join(', ') }) : t('dependenciesPlan.requested')"
    >
      <span class="text-body text-muted-foreground">{{ t(`dependenciesPlan.actions.${node.action}`) }}</span>
      <TextButton
        v-if="node.script"
        @click="show(node)"
      >
        {{ t('dependenciesPlan.script') }}
      </TextButton>
    </SettingsRow>
  </SettingsSection>
  <DependencyScriptDialog
    v-model:open="scriptOpen"
    :script="selectedScript"
    :dependency-name="selected?.name"
  />
</template>
