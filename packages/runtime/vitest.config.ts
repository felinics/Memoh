import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    // Windows filesystem integration tests launch native PowerShell ACL checks.
    testTimeout: process.platform === 'win32' ? 120_000 : 5_000,
    hookTimeout: process.platform === 'win32' ? 120_000 : 10_000,
    include: ['test/**/*.test.ts'],
  },
})
