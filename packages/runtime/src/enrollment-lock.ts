import { mkdir, rm } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

import { ensureDirectory, errorCode, writeFileAtomic } from './secure-files'
import { protectWindowsDirectory } from './windows-file-security'

// 锁仅覆盖 enrollment 的读取、比较与写入，不管理后台服务生命周期。
export async function withEnrollmentLock<T>(
  configPath: string,
  action: () => Promise<T>,
  timeoutMs = 30_000,
): Promise<T> {
  await ensureDirectory(dirname(configPath))
  const lockPath = `${configPath}.lock`
  const deadline = Date.now() + timeoutMs
  for (;;) {
    try {
      await mkdir(lockPath, { mode: 0o700 })
      break
    } catch (error) {
      if (errorCode(error) !== 'EEXIST') throw error
      if (Date.now() >= deadline) {
        throw new Error(`another enrollment operation holds ${lockPath}; inspect owner.json and remove the lock only after confirming that process has exited`)
      }
      await sleep(100)
    }
  }
  try {
    if (process.platform === 'win32') await protectWindowsDirectory(lockPath)
    await writeFileAtomic(join(lockPath, 'owner.json'), `${JSON.stringify({ pid: process.pid })}\n`, 0o600)
    return await action()
  } finally {
    await rm(lockPath, { recursive: true, force: true })
  }
}
