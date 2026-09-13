<script setup lang="ts">
// ProviderScopeTabs — the scope rail inside the Providers settings page
// (Models / Web Search / Voice / Video / Email). Pure v-model: it does NOT
// navigate; the container (pages/providers/index.vue) owns the ?tab= query,
// so the rail stays mounted across scope switches and the underline indicator
// keeps its slide animation (a route-jump implementation remounted the rail
// per page and the indicator snapped in place without animating).
import { Tabs, TabsList, TabsTrigger } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { PROVIDER_SCOPES, type ProviderScopeTab } from '@/lib/provider-scopes'

defineProps<{ modelValue: ProviderScopeTab }>()
const emit = defineEmits<{ 'update:modelValue': [tab: ProviderScopeTab] }>()

const { t } = useI18n()
</script>

<template>
  <!-- Underline rail = scope navigation, not a value switch (same rule as the
       bot access tabs). pl-1 cancels the trigger's own px-1 so the first tab's
       text lands on the content rail. No overflow-x-auto: it pins a visible
       horizontal scrollbar under macOS "always show scrollbars", and the five
       short labels fit even on a phone. -->
  <Tabs
    :model-value="modelValue"
    class="w-full"
    @update:model-value="value => emit('update:modelValue', value as ProviderScopeTab)"
  >
    <!-- No mb here: the panel below (PageShell variant="tab") already pads its
         own top by pt-6, which IS the designed rail→content rhythm (same as
         the bot-detail tabs). An mb on the rail would stack on top of it. -->
    <TabsList class="pl-1">
      <TabsTrigger
        v-for="scope in PROVIDER_SCOPES"
        :key="scope.tab"
        :value="scope.tab"
      >
        {{ t(scope.labelKey) }}
      </TabsTrigger>
    </TabsList>
  </Tabs>
</template>
