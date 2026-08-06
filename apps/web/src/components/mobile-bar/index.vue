<template>
  <!-- The ONE mobile chrome bar: h-11, hairline bottom edge, page background.
       Every mobile shell (chat / settings / master-detail) composes this bar
       instead of re-deriving its geometry — the review found three hand-copied
       variants, which is exactly how they drift. Slots only; the bar owns no
       navigation semantics. -->
  <header class="flex h-11 shrink-0 items-center border-b border-border bg-background px-1.5">
    <slot name="left" />
    <slot />
    <slot name="right" />
  </header>
</template>
