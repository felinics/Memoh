// Catalog of every semantic color token declared in `@theme inline`
// (packages/ui/src/style.css), grouped by family. Single source of truth for
// the token swatch wall. Names are the base token name WITHOUT the `--color-`
// prefix; the swatch renders `var(--color-${name})`.
//
// Keep in sync with style.css when tokens are added/removed. Intentionally
// excludes non-color tokens (--radius-*, --scrollbar-*, --terminal-*).

export interface TokenGroup {
  id: string
  label: string
  /** Token base names (no `--color-` prefix). */
  tokens: string[]
}

export const tokenGroups: TokenGroup[] = [
  {
    id: 'surface',
    label: 'Surface (background · hover · selected)',
    tokens: [
      'background', 'foreground',
      'accent', 'accent-foreground',
      'muted', 'muted-foreground',
      'border', 'input', 'ring',
    ],
  },
  {
    id: 'core',
    label: 'Core',
    tokens: [
      'card', 'card-foreground',
      'popover', 'popover-foreground',
      'primary', 'primary-foreground',
      'secondary', 'secondary-foreground',
      'destructive', 'destructive-foreground',
    ],
  },
  {
    id: 'brand',
    label: 'Brand',
    tokens: ['brand', 'brand-foreground', 'brand-soft', 'brand-border', 'brand-hover'],
  },
  {
    id: 'status',
    label: 'Status (success / warning / info)',
    tokens: [
      'success', 'success-foreground', 'success-solid-foreground', 'success-soft', 'success-border',
      'warning', 'warning-foreground', 'warning-solid-foreground', 'warning-soft', 'warning-border',
      'info', 'info-foreground', 'info-soft', 'info-border',
    ],
  },
  {
    id: 'chart',
    label: 'Chart',
    tokens: ['chart-1', 'chart-2', 'chart-3', 'chart-4', 'chart-5'],
  },
  {
    id: 'sidebar',
    label: 'Sidebar',
    tokens: [
      'sidebar', 'sidebar-foreground',
      'sidebar-accent', 'sidebar-accent-foreground',
      'sidebar-border',
    ],
  },
  {
    id: 'event',
    label: 'Event',
    tokens: [
      'event-schedule', 'event-schedule-foreground', 'event-schedule-soft', 'event-schedule-border',
      'event-heartbeat', 'event-heartbeat-foreground', 'event-heartbeat-soft', 'event-heartbeat-border',
      'event-subagent', 'event-subagent-foreground', 'event-subagent-soft', 'event-subagent-border',
      'event-discuss', 'event-discuss-foreground', 'event-discuss-soft', 'event-discuss-border',
    ],
  },
  {
    id: 'capability',
    label: 'Capability',
    tokens: [
      'capability-tool', 'capability-tool-foreground', 'capability-tool-soft',
      'capability-vision', 'capability-vision-foreground', 'capability-vision-soft',
      'capability-image', 'capability-image-foreground', 'capability-image-soft',
      'capability-reasoning', 'capability-reasoning-foreground', 'capability-reasoning-soft',
    ],
  },
  {
    id: 'context-window',
    label: 'Context window',
    tokens: [
      'context-window-xs', 'context-window-sm', 'context-window-md',
      'context-window-lg', 'context-window-xl', 'context-window-foreground',
    ],
  },
  {
    id: 'diff',
    label: 'Diff',
    tokens: ['diff-add', 'diff-add-border', 'diff-remove', 'diff-remove-border'],
  },
  // Accent-hue ramp — the SAME 10-hue palette demonstrated
  // interactively in SectionAccents.vue's dedicated "Accent palette" board.
  // Registered here too so the flat Design-tokens wall carries every token
  // declared in @theme inline, per this file's own contract; SectionAccents
  // keeps its bespoke ramp-row/interaction-demo layout because a flat swatch
  // grid can't show the hover/selected states that board exists to explain.
  {
    id: 'accent-gray',
    label: 'Accent · gray',
    tokens: ['accent-gray', 'accent-gray-soft', 'accent-gray-soft-hover', 'accent-gray-soft-active', 'accent-gray-border', 'accent-gray-deep'],
  },
  {
    id: 'accent-brown',
    label: 'Accent · brown',
    tokens: ['accent-brown', 'accent-brown-soft', 'accent-brown-soft-hover', 'accent-brown-soft-active', 'accent-brown-border', 'accent-brown-deep'],
  },
  {
    id: 'accent-orange',
    label: 'Accent · orange',
    tokens: ['accent-orange', 'accent-orange-soft', 'accent-orange-soft-hover', 'accent-orange-soft-active', 'accent-orange-border', 'accent-orange-deep'],
  },
  {
    id: 'accent-yellow',
    label: 'Accent · yellow',
    tokens: ['accent-yellow', 'accent-yellow-soft', 'accent-yellow-soft-hover', 'accent-yellow-soft-active', 'accent-yellow-border', 'accent-yellow-deep'],
  },
  {
    id: 'accent-green',
    label: 'Accent · green',
    tokens: ['accent-green', 'accent-green-soft', 'accent-green-soft-hover', 'accent-green-soft-active', 'accent-green-border', 'accent-green-deep'],
  },
  {
    id: 'accent-teal',
    label: 'Accent · teal',
    tokens: ['accent-teal', 'accent-teal-soft', 'accent-teal-soft-hover', 'accent-teal-soft-active', 'accent-teal-border', 'accent-teal-deep'],
  },
  {
    id: 'accent-blue',
    label: 'Accent · blue',
    tokens: [
      'accent-blue', 'accent-blue-soft', 'accent-blue-soft-hover', 'accent-blue-soft-active', 'accent-blue-border', 'accent-blue-deep',
      'accent-blue-fill', 'accent-blue-fill-hover', 'accent-blue-fill-active', 'accent-blue-foreground',
    ],
  },
  {
    id: 'accent-purple',
    label: 'Accent · purple',
    tokens: ['accent-purple', 'accent-purple-soft', 'accent-purple-soft-hover', 'accent-purple-soft-active', 'accent-purple-border', 'accent-purple-deep'],
  },
  {
    id: 'accent-pink',
    label: 'Accent · pink',
    tokens: ['accent-pink', 'accent-pink-soft', 'accent-pink-soft-hover', 'accent-pink-soft-active', 'accent-pink-border', 'accent-pink-deep'],
  },
  {
    id: 'accent-red',
    label: 'Accent · red',
    tokens: [
      'accent-red', 'accent-red-soft', 'accent-red-soft-hover', 'accent-red-soft-active', 'accent-red-border', 'accent-red-deep',
      'accent-red-fill', 'accent-red-foreground',
    ],
  },
]

/** A token name is a "foreground" token (rendered as text-on-surface sample). */
export function isForeground(name: string): boolean {
  return name.endsWith('-foreground')
}
