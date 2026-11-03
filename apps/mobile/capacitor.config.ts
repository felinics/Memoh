import type { CapacitorConfig } from '@capacitor/cli'

// The shell loads the Memoh web app from a server rather than bundling it, so
// web changes reach an installed build on reload. MEMOH_MOBILE_DEV_URL is read
// when `cap sync` runs and written into the Android project; changing it
// takes a new sync and build. Without it the shell opens www/, which only says
// how to point it at a server.
const devUrl = process.env.MEMOH_MOBILE_DEV_URL

const config: CapacitorConfig = {
  // Android takes its id from android/app/build.gradle; this one is the
  // default for platforms added later.
  appId: 'ai.memoh.mobile',
  appName: 'Memoh',
  webDir: 'www',
  server: devUrl
    ? { url: devUrl, cleartext: devUrl.startsWith('http:') }
    : undefined,
  plugins: {
    // The page draws under the status bar and the gesture bar and keeps its
    // content clear of them with the CSS safe-area insets (see the web app's
    // safe-area-frame utility).
    SystemBars: {
      insetsHandling: 'css',
      style: 'DEFAULT',
      initialViewportFitValueHint: 'cover',
    },
  },
}

export default config
