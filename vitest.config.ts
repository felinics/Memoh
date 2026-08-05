import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import dotenv from 'dotenv'
import { fileURLToPath } from 'url'

dotenv.config()

const webRoot = fileURLToPath(new URL('./apps/web', import.meta.url))
const desktopRoot = fileURLToPath(new URL('./apps/desktop', import.meta.url))
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
          alias: {
            '@': fileURLToPath(new URL('./apps/web/src', import.meta.url)),
            '#': fileURLToPath(new URL('./packages/ui/src', import.meta.url)),
          },
        },
        test: {
          name: 'web',
          globals: true,
          include: ['src/**/*.test.ts'],
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
