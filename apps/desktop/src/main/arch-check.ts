import { app, dialog, shell } from 'electron'
import { readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { is } from '@electron-toolkit/utils'

// macOS 安装包架构检测:Apple 芯片 Mac 上跑着 Intel(x64)安装包时,提示换装
// Apple 芯片版。用户常把下载页上的 x64 / arm64 看错,误装后整个应用经 Rosetta
// 转译运行,卡顿、耗电,却会被当成桌面端本身的性能问题。
//
// 只处理这一个方向:arm64 包装在 Intel Mac 上,macOS 自己就拒绝启动,代码没有
// 机会执行。
//
// 必须在 whenReady 的最前面、maybeSelfInstallMacOS 之前调用:从 DMG 首次打开时
// 先拦下,避免把错误架构的包拷进 /Applications(甚至替换掉已装的正确版本)。
//
// 「仍然继续」按应用版本记住:electron-updater 在 Rosetta 下会优先挑 arm64
// 更新包(MacUpdater.filterFilesForArch),下一次版本更新会自动换成 arm64,
// 因此不必每次启动都打扰。

// 文案跟随系统语言,理由同 self-install.ts:此时尚未登录、读不到应用内语言偏好。
type ArchCheckLocale = 'en' | 'zh' | 'ja'

interface ArchCheckStrings {
  title: string
  detail: string
  detailNoDownload: string
  download: string
  continue: string
}

const ARCH_CHECK_STRINGS: Record<ArchCheckLocale, ArchCheckStrings> = {
  en: {
    title: 'You are using the Intel version of Memoh',
    detail:
      'This Mac has Apple silicon. The Intel version runs through Rosetta translation, '
      + 'which makes Memoh noticeably slower and uses more battery.\n\n'
      + 'Download the Apple silicon version, open it, and choose "Replace" to switch.',
    detailNoDownload:
      'This Mac has Apple silicon. The Intel version runs through Rosetta translation, '
      + 'which makes Memoh noticeably slower and uses more battery.\n\n'
      + 'Install the Apple silicon (arm64) version of Memoh instead.',
    download: 'Download Apple Silicon Version',
    continue: 'Continue Anyway',
  },
  zh: {
    title: '你正在使用 Intel 版 Memoh',
    detail:
      '这台 Mac 使用 Apple 芯片。Intel 版需要经过 Rosetta 转译运行，'
      + '会明显变慢、更耗电。\n\n'
      + '请下载 Apple 芯片版，打开后选择「替换」即可切换。',
    detailNoDownload:
      '这台 Mac 使用 Apple 芯片。Intel 版需要经过 Rosetta 转译运行，'
      + '会明显变慢、更耗电。\n\n'
      + '请改为安装 Apple 芯片（arm64）版 Memoh。',
    download: '下载 Apple 芯片版',
    continue: '仍然继续',
  },
  ja: {
    title: 'Intel 版の Memoh を使用しています',
    detail:
      'この Mac は Apple シリコンを搭載しています。Intel 版は Rosetta による変換で動作するため、'
      + '動作が遅くなり、バッテリーの消費も増えます。\n\n'
      + 'Apple シリコン版をダウンロードして開き、「置き換える」を選んで切り替えてください。',
    detailNoDownload:
      'この Mac は Apple シリコンを搭載しています。Intel 版は Rosetta による変換で動作するため、'
      + '動作が遅くなり、バッテリーの消費も増えます。\n\n'
      + '代わりに Apple シリコン（arm64）版の Memoh をインストールしてください。',
    download: 'Apple シリコン版をダウンロード',
    continue: 'このまま続ける',
  },
}

function archCheckStrings(): ArchCheckStrings {
  const locale = app.getLocale().toLowerCase()
  if (locale.startsWith('zh')) return ARCH_CHECK_STRINGS.zh
  if (locale.startsWith('ja')) return ARCH_CHECK_STRINGS.ja
  return ARCH_CHECK_STRINGS.en
}

// 发布流程把 DMG 与 latest-mac.yml 平铺上传到更新源同一目录,文件名取自
// electron-builder.yml 的 artifactName(${productName}-${version}-mac-${arch}.${ext})。
// 未配置更新源的自建包没有可信的下载地址,返回 null,只给文字说明。
function arm64DmgUrl(): string | null {
  const base = (process.env.MEMOH_DESKTOP_UPDATE_BASE_URL ?? '').trim()
  if (!base) return null
  try {
    return new URL(`${app.getName()}-${app.getVersion()}-mac-arm64.dmg`, base.endsWith('/') ? base : `${base}/`).toString()
  } catch {
    return null
  }
}

interface SavedArchCheck {
  dismissedVersion: string | null
}

function settingsPath(): string {
  return join(app.getPath('userData'), 'arch-check.json')
}

function dismissedVersion(): string | null {
  try {
    const value = JSON.parse(readFileSync(settingsPath(), 'utf8')) as SavedArchCheck
    return typeof value.dismissedVersion === 'string' ? value.dismissedVersion : null
  } catch {
    return null
  }
}

function rememberDismissed(): void {
  const path = settingsPath()
  try {
    const value: SavedArchCheck = { dismissedVersion: app.getVersion() }
    writeFileSync(`${path}.tmp`, JSON.stringify(value))
    renameSync(`${path}.tmp`, path)
  } catch (error) {
    // 记不住只会让下次启动再提示一次,不能因此阻塞启动。
    console.error('arch-check: failed to persist dismissal', error)
  }
}

/**
 * Apple 芯片上运行 Intel 包时提示换装。
 *
 * @returns true 表示用户选择下载,已打开下载链接,调用方应立即 return,
 *          由本函数负责退出;false 表示照常启动。
 */
export async function maybePromptAppleSiliconBuild(): Promise<boolean> {
  if (process.platform !== 'darwin' || is.dev || !app.isPackaged) return false
  if (!app.runningUnderARM64Translation) return false
  if (dismissedVersion() === app.getVersion()) return false

  const strings = archCheckStrings()
  const downloadUrl = arm64DmgUrl()
  const buttons = downloadUrl ? [strings.download, strings.continue] : [strings.continue]
  const response = dialog.showMessageBoxSync({
    type: 'warning',
    buttons,
    defaultId: 0,
    cancelId: buttons.length - 1,
    title: strings.title,
    message: strings.title,
    detail: downloadUrl ? strings.detail : strings.detailNoDownload,
  })

  if (downloadUrl && response === 0) {
    try {
      await shell.openExternal(downloadUrl)
    } catch (error) {
      // 打不开浏览器时退出也没意义:用户什么都没拿到,原地继续运行。
      console.error('arch-check: failed to open download URL', error)
      return false
    }
    // 退出当前实例:新 DMG 自安装时遇到正在运行的旧版只会提示「请先退出」。
    app.quit()
    return true
  }

  rememberDismissed()
  return false
}
