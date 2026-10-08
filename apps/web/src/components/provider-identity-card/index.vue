<template>
  <!-- Identity header shared by the provider detail pages: leading mark,
       name, optional subtitle, and the page's own trailing actions
       (enable switch, delete).
       The card shell comes from SurfaceCard so the dark-mode edge rule and
       radius stay owned by @felinic/ui. -->
  <SurfaceCard
    as="section"
    padding="row"
    class="flex items-center gap-3"
  >
    <span
      class="flex size-9 shrink-0 items-center justify-center"
      :class="media === 'avatar' && 'rounded-full bg-muted'"
    >
      <slot name="media" />
    </span>
    <div class="min-w-0 flex-1">
      <h2 class="truncate text-control font-semibold text-foreground">
        {{ name }}
      </h2>
      <p
        v-if="subtitle"
        class="mt-0.5 truncate text-body text-muted-foreground"
      >
        {{ subtitle }}
      </p>
    </div>
    <div
      v-if="$slots.actions"
      class="ml-auto flex shrink-0 items-center gap-2"
    >
      <slot name="actions" />
    </div>
  </SurfaceCard>
</template>

<script setup lang="ts">
import { SurfaceCard } from '@felinic/ui'

withDefaults(defineProps<{
  name?: string
  subtitle?: string
  /**
   * `avatar` puts the mark on a muted circle (provider icons, initials,
   * generic glyphs); `bare` is for logos that carry their own shape.
   */
  media?: 'avatar' | 'bare'
}>(), {
  name: '',
  subtitle: '',
  media: 'avatar',
})
</script>
