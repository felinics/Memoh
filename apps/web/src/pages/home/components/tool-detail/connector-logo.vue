<template>
  <!--
    Provenance mark for a tool call that came from a Connector. Drawn at 1em so
    it tracks the row's own font size (the tool rows sit at two different type
    scales — root level and inside a process capsule — and a fixed size would
    read as oversized in one of them). A connector the catalog has no artwork
    for still gets the generic plug, so the row never silently loses the "this
    came from a connector" signal.
  -->
  <ProviderIcon
    :icon="connector.iconUrl"
    :title="connector.name"
    class="size-[1em] shrink-0 object-contain"
  >
    <Plug class="size-[1em] shrink-0" />
  </ProviderIcon>
</template>

<script setup lang="ts">
import { Plug } from 'lucide-vue-next'
import ProviderIcon from '@/components/provider-icon/index.vue'
import type { ConnectorIdentity } from '../../composables/useConnectorLogos'

defineProps<{ connector: ConnectorIdentity }>()
</script>
