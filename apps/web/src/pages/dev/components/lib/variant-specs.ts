// Variant/size key lists for each cva-based component.
//
// NOTE: cva 0.7.1 (the version this repo ships) returns a bare class-building
// closure that does NOT expose its `.config`, so variant keys cannot be read
// from the cva function at runtime. They are mirrored here instead — keep in
// sync with the `cva(...)` declarations in packages/ui/src/components/*/index.ts.
// (If @felinic/ui ever re-exports the raw variant config, this can go back to
// runtime introspection.)

export interface VariantSpec {
  /** The cva variant-axis prop name (usually 'variant', 'orientation' for ButtonGroup). */
  axis: string
  variants: string[]
  sizes?: string[]
}

export const variantSpecs = {
  button: {
    axis: 'variant',
    variants: ['default', 'destructive', 'outline', 'secondary', 'ghost', 'link', 'link-static', 'link-draw', 'primary', 'brand'],
    sizes: ['default', 'sm', 'lg', 'icon', 'icon-sm', 'icon-lg'],
  },
  badge: {
    axis: 'variant',
    variants: ['default', 'secondary', 'destructive', 'success', 'warning', 'info', 'outline'],
    sizes: ['default', 'sm'],
  },
  toggle: {
    axis: 'variant',
    variants: ['default', 'tint', 'outline'],
    sizes: ['default', 'sm', 'lg'],
  },
  buttonGroup: {
    axis: 'orientation',
    variants: ['horizontal', 'vertical'],
  },
} satisfies Record<string, VariantSpec>
