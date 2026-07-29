import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { createRequire } from 'module'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'url'

// https://vite.dev/config/
export default defineConfig(({ command }) => {
  const require = createRequire(import.meta.url)
  const defaultPort = 8082
  const defaultHost = '127.0.0.1'
  const defaultApiBaseUrl = process.env.VITE_API_URL ?? 'http://localhost:8080'
  const configuredProxyTarget = process.env.MEMOH_WEB_PROXY_TARGET?.trim()
  const configuredChannelProxyTarget = process.env.MEMOH_CHANNEL_PROXY_TARGET?.trim()
  const configuredPath = process.env.MEMOH_CONFIG_PATH?.trim() || process.env.CONFIG_PATH?.trim()
  const configPath = configuredPath && configuredPath.length > 0 ? configuredPath : '../../config.toml'

  let port = defaultPort
  let host = defaultHost
  let baseUrl = configuredProxyTarget || defaultApiBaseUrl

  if (command !== 'build') {
    try {
      const { loadConfig, getBaseUrl } = require('@memohai/config') as {
        loadConfig: (path: string) => {
          web?: { port?: number; host?: string }
        }
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
      // Fall back to env/default values when config.toml is unavailable.
    }
  }

  function isBrowserProxyHost(value: string | undefined): boolean {
    const host = (value ?? '').split(':')[0]?.toLowerCase() ?? ''
    return host.includes('.browser.')
  }

  const apiProxy = {
    target: baseUrl,
    changeOrigin: true,
    rewrite: (path: string) => path.replace(/^\/api/, ''),
    ws: true,
    xfwd: true,
  }

  const browserHostProxy = {
    target: baseUrl,
    changeOrigin: false,
    ws: true,
    xfwd: true,
    bypass(req: { headers: { host?: string }, url?: string }) {
      return isBrowserProxyHost(req.headers.host) ? undefined : req.url
    },
  }

  const channelProxy = {
    target: configuredChannelProxyTarget || baseUrl,
    changeOrigin: true,
    ws: true,
    xfwd: true,
  }

  const channelApiProxy = {
    ...channelProxy,
    rewrite: (path: string) => path.replace(/^\/api/, ''),
  }

  return {
    plugins: [
      // Guard against pnpm patchedDependencies cache poisoning at vite
      // startup, not only at install time: the lockfile rarely changes, so a
      // dev can pull a fix and restart vite without ever re-running `pnpm
      // install` (where the root postinstall guard lives). configResolved
      // runs before the dep optimizer reads its cache, so the script can
      // also delete an optimizer cache built from pre-patch code (its cache
      // key is the lockfile, which poisoning does not change). Runs as a
      // child process — the script is repo-root plain JS shared with
      // postinstall; importing it here would drag root files into this
      // package's TS project for no gain. Non-zero exit fails dev/build
      // loudly with the recovery recipe on stderr.
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
      vue(),
      tailwindcss(),
    ],
    server: {
      port,
      host,
      hmr: {
        overlay: false,
      },
      proxy: {
        '^/api/bots/[^/]+/channel/weixin/qr/(start|poll)$': channelApiProxy,
        '/api': apiProxy,
        '/channels': channelProxy,
        '/email/mailgun/webhook': channelProxy,
        '/': browserHostProxy,
      },
    },
    preview: {
      port,
      host: '0.0.0.0',
      proxy: {
        '^/api/bots/[^/]+/channel/weixin/qr/(start|poll)$': channelApiProxy,
        '/api': apiProxy,
        '/channels': channelProxy,
        '/email/mailgun/webhook': channelProxy,
        '/': browserHostProxy,
      },
      allowedHosts: true,
    },
    resolve: {
      // @felinic/ui is a linked workspace package and pnpm can resolve its
      // peer dependencies through a different Vue version than the host app.
      // Keep singleton/injection-based runtimes shared so useForm() and the
      // UI package's FormField see the same vee-validate context in builds.
      dedupe: ['vue', 'vee-validate'],
      alias: {
        '#': fileURLToPath(new URL('../../packages/ui/src', import.meta.url)),
        '@': fileURLToPath(new URL('./src', import.meta.url))
      },
    },
  }
})
