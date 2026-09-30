<!-- Error mark drawn for small sizes (12–16px). A stock 24-grid circle-alert
     scaled to 14px leaves a short stem, a dot no bigger than the stroke and a
     wide gap between them, so it reads as a colon. This one is drawn on a
     16-grid (≈1:1 at the sizes it ships at): a filled, tapered stem that runs
     most of the ring's height, a dot clearly heavier than the stem tip, and a
     tight gap, so the "!" survives at 12px. Fills instead of strokes for the
     mark, so its weight doesn't thin out as the icon scales down.
     Staging copy: lives app-side until the shape is signed off, then moves to
     the shared icon layer. -->
<template>
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 16 16"
    fill="currentColor"
    aria-hidden="true"
  >
    <circle
      cx="8"
      cy="8"
      r="6.75"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
    />
    <path d="M6.95 4.75C6.93 4.33 7.27 4 7.69 4h.62c.42 0 .76.33.74.75l-.25 4.6a.8.8 0 0 1-1.6 0z" />
    <circle
      cx="8"
      cy="11.55"
      r="1.1"
    />
  </svg>
</template>
