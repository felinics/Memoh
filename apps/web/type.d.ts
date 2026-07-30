declare module '@jamescoyle/vue-icon'

interface Window {
  api?: {
    desktop?: {
      openExternalUrl?: (url: string) => Promise<void>
    }
  }
}

// CSS-only package; ships no type declarations. TS 6 flags untyped
// side-effect imports (TS2882, TS2307 in TS 5).
declare module '@fontsource-variable/inter'
