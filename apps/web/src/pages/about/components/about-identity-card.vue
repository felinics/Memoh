<template>
  <!-- Logo, name, tagline, blurb and the resource links all live in one card so
       About reads like the rest of Settings (cf. Profile). It is free-form
       content rather than a list of settings rows, so it takes SurfaceCard's own
       even padding instead of a settings card's row inset. -->
  <SurfaceCard>
    <div class="flex items-center gap-4">
      <div class="flex size-14 shrink-0 items-center justify-center rounded-xl border border-border bg-background">
        <img
          src="/logo.svg"
          alt="Memoh"
          class="size-9"
        >
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <h2 class="text-base font-semibold text-foreground">
            Memoh
          </h2>
          <Badge
            v-if="version"
            variant="secondary"
            size="sm"
            class="font-medium"
          >
            {{ $t('settings.versionTag', { version }) }}
          </Badge>
        </div>
        <p class="mt-0.5 text-[13px] leading-[18px] tracking-[-0.08px] font-[360] text-muted-foreground">
          {{ $t('about.tagline') }}
        </p>
      </div>
    </div>

    <p class="mt-4 text-[13px] leading-[19px] tracking-[-0.06px] font-[380] text-muted-foreground">
      {{ $t('about.description') }}
    </p>

    <div class="mt-5 flex flex-wrap gap-2">
      <Button
        v-for="link in links"
        :key="link.href"
        as="a"
        :href="link.href"
        target="_blank"
        rel="noopener noreferrer"
        variant="outline"
        size="sm"
        class="text-[13px] leading-[18px] tracking-[-0.08px] font-[420] text-foreground/82"
      >
        <component
          :is="link.icon"
          class="size-4"
        />
        {{ $t(link.labelKey) }}
      </Button>
    </div>
  </SurfaceCard>
</template>

<script setup lang="ts">
// The About page's identity card. The page computes the version (it also needs
// it for the update copy) and passes it in; everything else here is static.
import type { Component } from 'vue'
import { BookOpen, Github, MessageSquare } from 'lucide-vue-next'
import { Badge, Button, SurfaceCard } from '@felinic/ui'

defineProps<{
  /** Running version without the leading "v"; the badge is hidden when empty. */
  version?: string
}>()

const GITHUB_REPO = 'felinics/memoh'

interface ResourceLink {
  icon: Component
  labelKey: string
  href: string
}

const links: ResourceLink[] = [
  { icon: Github, labelKey: 'about.github', href: `https://github.com/${GITHUB_REPO}` },
  { icon: BookOpen, labelKey: 'about.docs', href: 'https://docs.memoh.ai' },
  { icon: MessageSquare, labelKey: 'about.feedback', href: `https://github.com/${GITHUB_REPO}/issues` },
]
</script>
