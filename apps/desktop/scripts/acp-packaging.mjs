import { execFile } from 'node:child_process'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const smokeScript = join(dirname(fileURLToPath(import.meta.url)), 'acp-packaging-smoke.mjs')

export function packagedApplicationPaths(context) {
  const platform = context.electronPlatformName
  const productFilename = context.packager.appInfo.productFilename
  if (platform === 'darwin') {
    const application = join(context.appOutDir, `${productFilename}.app`)
    return {
      executable: join(application, 'Contents', 'MacOS', productFilename),
      resources: join(application, 'Contents', 'Resources'),
    }
  }
  const executableName = platform === 'linux'
    ? (context.packager.executableName ?? context.packager.appInfo.sanitizedName.toLowerCase())
    : `${productFilename}.exe`
  return {
    executable: join(context.appOutDir, executableName),
    resources: join(context.appOutDir, 'resources'),
  }
}

export async function runPackagedACPSmoke(paths) {
  const runtimeExecutable = paths.runtimeExecutable ?? paths.executable
  const result = await execFileAsync(runtimeExecutable, [smokeScript, paths.resources], {
    encoding: 'utf8',
    env: {
      ...process.env,
      ELECTRON_RUN_AS_NODE: '1',
    },
    maxBuffer: 64 * 1024,
    timeout: 30_000,
    windowsHide: true,
  })
  process.stdout.write(String(result.stdout))
}

export function smokeRuntimeExecutable(context, hostPlatform, installedElectron) {
  const paths = packagedApplicationPaths(context)
  return context.electronPlatformName === hostPlatform ? paths.executable : installedElectron
}

export async function afterPack(context) {
  const paths = packagedApplicationPaths(context)
  paths.runtimeExecutable = smokeRuntimeExecutable(
    context,
    process.platform,
    await installedElectronExecutable(),
  )
  await runPackagedACPSmoke(paths)
}

export async function afterSign(context) {
  const paths = packagedApplicationPaths(context)
  paths.runtimeExecutable = smokeRuntimeExecutable(
    context,
    process.platform,
    await installedElectronExecutable(),
  )
  await runPackagedACPSmoke(paths)
  if (
    context.electronPlatformName === 'darwin'
    && process.platform === 'darwin'
    && shouldVerifyMacSignature(process.env)
  ) {
    await execFileAsync('/usr/bin/codesign', [
      '--verify',
      '--deep',
      '--strict',
      join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`),
    ], {
      encoding: 'utf8',
      maxBuffer: 64 * 1024,
      timeout: 30_000,
    })
  }
}

export function shouldVerifyMacSignature(environment) {
  return Boolean(environment.CSC_LINK?.trim())
}

async function installedElectronExecutable() {
  const electron = await import('electron')
  return electron.default
}
