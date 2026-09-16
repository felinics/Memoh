<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AppDependencyConfirmation } from '@/composables/api/useApps'
import DependencyKvList from './dependency-kv-list.vue'

defineProps<{ dependencies: AppDependencyConfirmation[] }>()

const { t } = useI18n()
</script>

<template>
  <section class="space-y-3">
    <p class="text-body font-medium">
      {{ t('apps.prepare.dependencies') }}
    </p>
    <p
      v-if="!dependencies.length"
      class="text-caption text-muted-foreground"
    >
      {{ t('apps.prepare.noDependencies') }}
    </p>
    <div
      v-for="dependency in dependencies"
      :key="dependency.dependency_id"
      class="space-y-1"
    >
      <p class="break-all font-mono text-body font-medium">
        {{ dependency.dependency_id }}
      </p>
      <DependencyKvList
        :rows="[
          { label: t('apps.prepare.action'), value: t(`bots.dependencies.action.${dependency.action}`) },
          { label: t('apps.prepare.version'), value: dependency.version, mono: true },
          { label: t('apps.prepare.definitionRevision'), value: dependency.definition_revision, mono: true },
          { label: t('apps.prepare.registry'), value: dependency.registry_id, mono: true },
          { label: t('apps.prepare.source'), value: dependency.source_url, mono: true },
        ]"
      />
      <p class="text-body text-muted-foreground">
        {{ t('bots.dependencies.confirm.repairConsent', { version: dependency.version }) }}
      </p>
    </div>
  </section>
</template>
