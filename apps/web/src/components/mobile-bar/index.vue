<template>
  <!-- The ONE mobile chrome bar: h-11, hairline bottom edge, page background.
       Every mobile shell (chat / settings / master-detail) composes this bar
       instead of re-deriving its geometry — the review found three hand-copied
       variants, which is exactly how they drift. Slots only; the bar owns no
       navigation semantics.
       sticky top-0 is not a layout change (every host column is non-scrolling,
       so it renders exactly like a static bar): it makes iOS Home Screen web
       apps treat the bar as a fixed top edge and extend its color under the
       status bar instead of drawing the system scroll-edge fade over it. -->
  <header class="sticky top-0 flex h-11 shrink-0 items-center border-b border-border bg-background px-1.5">
    <slot name="left" />
    <slot />
    <slot name="right" />
  </header>
</template>
