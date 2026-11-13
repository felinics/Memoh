import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import dotenv from 'dotenv'
import { fileURLToPath } from 'url'

dotenv.config()

const webRoot = fileURLToPath(new URL('./apps/web', import.meta.url))
const desktopRoot = fileURLToPath(new URL('./apps/desktop', import.meta.url))
const runtimeRoot = fileURLToPath(new URL('./packages/runtime', import.meta.url))
const repoRoot = fileURLToPath(new URL('.', import.meta.url))

export default defineConfig({
  test: {
    globals: true,
    env: process.env,
    testTimeout: Infinity,
    projects: [
      {
        root: webRoot,
        plugins: [vue()],
        resolve: {
          dedupe: ['vue', 'vee-validate'],
          alias: {
            '@': fileURLToPath(new URL('./apps/web/src', import.meta.url)),
            '#': fileURLToPath(new URL('./packages/ui/src', import.meta.url)),
          },
        },
        test: {
          name: 'web',
          server: { deps: { inline: ['reka-ui', /@vueuse\//] } },
          globals: true,
          include: ['src/**/*.test.ts'],
          // The app loads only its active language before mounting; tests read
          // copy in every language, so this loads all three first.
          setupFiles: ['./src/vitest-setup.ts'],
          env: process.env,
          testTimeout: Infinity,
        },
      },
      {
        root: desktopRoot,
        test: {
          name: 'desktop',
          globals: true,
          include: ['src/**/*.test.ts'],
          env: process.env,
          testTimeout: Infinity,
        },
      },
      {
        root: runtimeRoot,
        test: {
          name: 'runtime',
          environment: 'node',
          include: ['test/**/*.test.ts'],
          env: process.env,
          // Windows filesystem integration tests launch native PowerShell ACL checks.
          testTimeout: process.platform === 'win32' ? 120_000 : 5_000,
          hookTimeout: process.platform === 'win32' ? 120_000 : 10_000,
        },
      },
      {
        root: repoRoot,
        test: {
          name: 'docker',
          include: ['docker/**/*.test.ts'],
          env: process.env,
          testTimeout: 15_000,
        },
      },
    ],
  },
})
