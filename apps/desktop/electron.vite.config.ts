import { defineConfig, externalizeDepsPlugin } from 'electron-vite'
import type { PluginOption } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const require = createRequire(import.meta.url)

const defaultPort = 8082
const defaultHost = '127.0.0.1'
const defaultApiBaseUrl = process.env.MEMOH_DESKTOP_BASE_URL ?? process.env.VITE_API_URL ?? 'http://localhost:18080'

function resolveProxyTarget(command: 'build' | 'serve'): { port: number; host: string; baseUrl: string } {
  const configuredProxyTarget = process.env.MEMOH_WEB_PROXY_TARGET?.trim()
  const configuredPath = process.env.MEMOH_CONFIG_PATH?.trim() || process.env.CONFIG_PATH?.trim()
  const configPath = configuredPath && configuredPath.length > 0 ? configuredPath : '../../config.toml'

  let port = defaultPort
  let host = defaultHost
  let baseUrl = configuredProxyTarget || defaultApiBaseUrl

  const shouldReadConfig = command !== 'build' && (Boolean(configuredProxyTarget) || Boolean(configuredPath))

  if (shouldReadConfig) {
    try {
      const { loadConfig, getBaseUrl } = require('@memohai/config') as {
        loadConfig: (path: string) => { web?: { port?: number; host?: string } }
        getBaseUrl: (config: unknown) => string
      }
      let config
      try {
        config = loadConfig(configPath)
      } catch {
        config = loadConfig('../../conf/app.docker.toml')
      }
      port = config.web?.port ?? defaultPort
      host = config.web?.host ?? defaultHost
      baseUrl = configuredProxyTarget || getBaseUrl(config)
    } catch {
      // fall back to env/default values when config.toml is unavailable.
    }
  }

  return { port, host, baseUrl }
}

export default defineConfig(async ({ command }) => {
  const { port, host, baseUrl } = resolveProxyTarget(command)
  const bundledElectronToolkit = ['@electron-toolkit/preload', '@electron-toolkit/utils']
  const desktopBaseUrl = process.env.MEMOH_DESKTOP_BASE_URL?.trim() ?? ''
  const desktopAppId = process.env.MEMOH_DESKTOP_APP_ID?.trim() ?? ''
  const updateBaseUrl = process.env.MEMOH_DESKTOP_UPDATE_BASE_URL?.trim() ?? ''

  const devtoolsPlugins: PluginOption[] = []
  if (command !== 'build' && process.env.MEMOH_VUE_DEVTOOLS !== '0') {
    try {
      const { default: vueDevTools } = await import('vite-plugin-vue-devtools')
      devtoolsPlugins.push(vueDevTools())
    } catch {
      // DevTools is optional — never block startup.
    }
  }

  return {
    main: {
      define: {
        'process.env.MEMOH_DESKTOP_BASE_URL': JSON.stringify(desktopBaseUrl),
        'process.env.MEMOH_DESKTOP_APP_ID': JSON.stringify(desktopAppId),
        'process.env.MEMOH_DESKTOP_UPDATE_BASE_URL': JSON.stringify(updateBaseUrl),
      },
      plugins: [externalizeDepsPlugin({ exclude: bundledElectronToolkit })],
    },
    preload: {
      plugins: [externalizeDepsPlugin({ exclude: bundledElectronToolkit })],
    },
    renderer: {
      root: resolve(__dirname, 'src/renderer'),
      // Reuse apps/web/public so absolute-path assets (e.g. /logo.svg) resolve
      // when web modules are imported directly from the desktop renderer.
      publicDir: resolve(__dirname, '../web/public'),
      plugins: [
        // Same startup guard as apps/web/vite.config.ts: verify pnpm
        // patchedDependencies content and sweep dep-optimizer caches built
        // from pre-patch code. The desktop renderer imports web modules
        // (dockview included) and keeps its own .vite cache, so it is
        // exposed to the same cache-poisoning path — see that file for the
        // full rationale.
        {
          name: 'memoh:verify-patched-deps',
          configResolved() {
            execFileSync(
              process.execPath,
              [fileURLToPath(new URL('../../scripts/check-patched-deps.mjs', import.meta.url))],
              { stdio: 'inherit' },
            )
          },
        },
        ...devtoolsPlugins,
        vue(),
        tailwindcss(),
      ],
      resolve: {
        // The renderer consumes both @memohai/web and the linked @felinic/ui
        // workspace package. Keep their injection-based runtimes shared just
        // like the standalone Web build does.
        dedupe: ['vue', 'vee-validate'],
        alias: {
          '@renderer': fileURLToPath(new URL('./src/renderer/src', import.meta.url)),
          // match apps/web/vite.config.ts aliases so imported web modules resolve correctly.
          '@': fileURLToPath(new URL('../web/src', import.meta.url)),
          '#': fileURLToPath(new URL('../../packages/ui/src', import.meta.url)),
        },
      },
      optimizeDeps: {
        // Only pre-bundle from the renderer entry — scanning all web pages
        // forces esbuild to crawl Monaco/xterm/ECharts/Mermaid/etc. on every
        // new import, which during AI-assisted editing triggers repeated
        // full dev-server restarts and page reloads. Vite's scanner follows
        // dynamic imports in the router, so page-level deps are still
        // discovered without listing every page here.
        entries: [
          'src/renderer/src/main.ts',
        ],
        // Monaco is the one heavy dependency the scanner can't front-load from
        // the entry: the editor host (stream-monaco) pulls Monaco's many ESM
        // sub-modules lazily, so the first mount of a bot-settings tab that
        // hosts an editor (bot-network / bot-skills / bot-mcp) makes the dev
        // server discover them mid-session, re-run esbuild, and force a full
        // page reload (in-flight dynamic imports 504, caught by the router's
        // onError → window.location.reload). Pre-bundling these once at
        // startup trades a slightly slower cold start for no mid-session
        // reload. Unlike adding them to `entries`, `include` does not re-crawl
        // on every edit, so the concern above still holds.
        include: [
          'monaco-editor',
          'stream-monaco',
          'shiki',
        ],
      },
      build: {
        rollupOptions: {
          input: {
            index: resolve(__dirname, 'src/renderer/index.html'),
          },
        },
      },
      server: {
        port,
        host,
        hmr: {
          overlay: false,
        },
        proxy: {
          '/api': {
            target: baseUrl,
            changeOrigin: true,
            rewrite: (path: string) => path.replace(/^\/api/, ''),
            ws: true,
          },
        },
      },
    },
  }
})
