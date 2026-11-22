<template>
  <main class="flex flex-col flex-1 min-w-0 overflow-hidden bg-background">
    <section class="flex-1 min-h-0 overflow-hidden">
      <!-- Render the chat home directly instead of via a child <router-view>.
           main-section is now a persistent shell mounted in App.vue (shown/
           hidden with v-show as the route changes), so chat never sits in a
           route outlet that would unmount it when navigating into settings.
           Keeping the instance mounted — DOM attached, not detached like
           KeepAlive — preserves workbench layout and message scroll position, so
           returning from settings has no black flash, no re-scroll, no relayout.
           home reads the route via useRoute() globally, so it still tracks the
           active bot regardless of which route is current (its URL-sync watchers
           are guarded to only fire on chat routes). -->
      <HomeView />
    </section>
  </main>
</template>

<script setup lang="ts">
import HomeView from '@/pages/home/index.vue'
</script>
