import { app, dialog } from 'electron'
import { readFileSync } from 'node:fs'
import { is } from '@electron-toolkit/utils'

// macOS 双击自安装:DMG 里只放 app,用户双击图标启动时,若发现自己不在
// /Applications,就把自己拷进去再重启 —— 免去"拖到 Applications"那一步。
//
// 为什么必须在 whenReady 的最前面调用(早于 ensureLocalServer):
//   首次启动时 app 跑在只读的 DMG 卷(甚至 App Translocation 的随机只读路径)。
//   若先拉起本地 server / Qdrant 再搬家,会从 DMG 路径 spawn 出子进程,搬家重启后
//   留下指向已卸载卷的孤儿进程。所以自安装是启动序列的第 0 步:要么搬家+重启,
//   要么原地继续 —— 二者必居其一,且在任何本地进程被拉起之前决定。

// 自安装弹框的界面文案跟随系统语言(app.getLocale),与 web 端 i18n 的
// en/zh/ja 三档对齐;其余语言回落英文。安装包流程先于登录/设置,读不到
// 应用内语言偏好,系统语言是这里唯一诚实的来源。
type SelfInstallLocale = 'en' | 'zh' | 'ja'

interface SelfInstallStrings {
  runningTitle: string
  runningDetail: string
  conflictTitle: string
  conflictDetail: string
  replace: string
  keepBoth: string
  ok: string
}

const SELF_INSTALL_STRINGS: Record<SelfInstallLocale, SelfInstallStrings> = {
  en: {
    runningTitle: 'Memoh is already running',
    runningDetail:
      'Memoh is still running in the background. Quit it from the Memoh menu bar icon ' +
      '(closing the window is not enough), then open this installer again.',
    conflictTitle: 'Memoh is already installed',
    conflictDetail:
      'A version of Memoh already exists in your Applications folder. Replace it with this one?',
    replace: 'Replace',
    keepBoth: 'Keep Both / Run Here',
    ok: 'OK',
  },
  zh: {
    runningTitle: 'Memoh 正在运行',
    runningDetail:
      'Memoh 仍在后台运行。请通过菜单栏的 Memoh 图标退出(仅关闭窗口并不会退出),然后重新打开本安装包。',
    conflictTitle: '已安装 Memoh',
    conflictDetail: '「应用程序」文件夹中已存在一个 Memoh,要用当前版本替换它吗?',
    replace: '替换',
    keepBoth: '保留两者 / 原地运行',
    ok: '好',
  },
  ja: {
    runningTitle: 'Memoh は実行中です',
    runningDetail:
      'Memoh はバックグラウンドで実行中です。メニューバーの Memoh アイコンから終了してから' +
      '(ウインドウを閉じるだけでは終了しません)、このインストーラーをもう一度開いてください。',
    conflictTitle: 'Memoh はすでにインストールされています',
    conflictDetail: 'アプリケーションフォルダにすでに Memoh があります。このバージョンで置き換えますか?',
    replace: '置き換える',
    keepBoth: '両方を残す / このまま実行',
    ok: 'OK',
  },
}

function selfInstallStrings(): SelfInstallStrings {
  // 必须在 ready 之后调用;两个弹框入口(第二实例提示 / 搬家冲突)都满足。
  const locale = app.getLocale().toLowerCase()
  if (locale.startsWith('zh')) return SELF_INSTALL_STRINGS.zh
  if (locale.startsWith('ja')) return SELF_INSTALL_STRINGS.ja
  return SELF_INSTALL_STRINGS.en
}

/**
 * 提示"旧版正在运行,先退出再装"。两个入口共用:
 * 1. index.ts 里没抢到单实例锁、被判定为安装/更新尝试的第二实例;
 * 2. 下方 moveToApplicationsFolder 的 existsAndRunning 冲突分支(防御性,
 *    有入口 1 之后基本不可达)。
 */
export function showQuitRunningInstanceDialog(): void {
  const strings = selfInstallStrings()
  dialog.showMessageBoxSync({
    type: 'info',
    buttons: [strings.ok],
    defaultId: 0,
    cancelId: 0,
    title: strings.runningTitle,
    message: strings.runningTitle,
    detail: strings.runningDetail,
  })
}

function plistString(plist: string, key: string): string | null {
  const match = new RegExp(`<key>${key}</key>\\s*<string>\\s*([^<]+?)\\s*</string>`).exec(plist)
  return match?.[1] ?? null
}

// /Applications 里已装那一份的版本。未安装 / 不可读 / 非预期格式时返回 null。
function installedAppVersion(): string | null {
  try {
    const plist = readFileSync(`/Applications/${app.getName()}.app/Contents/Info.plist`, 'utf8')
    return plistString(plist, 'CFBundleShortVersionString') ?? plistString(plist, 'CFBundleVersion')
  } catch {
    return null
  }
}

/**
 * 当前进程是不是"从 /Applications 之外启动、与已装版本不同的打包副本"
 * —— 即一次被在运行的旧实例挡住的 DMG 安装/更新尝试。
 *
 * 供 index.ts 在没抢到单实例锁时区分:普通第二实例静默退出(深链已转交);
 * 安装尝试必须先提示用户退出旧版,否则更新意图被无声吞掉(秒退 + 旧窗口
 * 拉到前台,用户以为已经更新成功)。
 *
 * 已装版本读不到(null)按"不同"处理:此时旧实例同样挡着安装,提示仍是对的。
 */
export function shouldPromptRunningInstall(): boolean {
  if (process.platform !== 'darwin' || is.dev || !app.isPackaged) return false
  try {
    if (app.isInApplicationsFolder()) return false
  } catch {
    return false
  }
  return installedAppVersion() !== app.getVersion()
}

/**
 * 若在 macOS 打包态且当前不在 /Applications,尝试把 app 搬进 /Applications。
 *
 * @returns true 表示已触发搬家 + 重启,调用方应立即 return、不要再启动任何本地进程。
 *          false 表示无需搬家 / 搬家失败 / 用户取消 —— 调用方按原地运行继续。
 */
export function maybeSelfInstallMacOS(): boolean {
  // 仅打包态的 macOS 才自安装。dev 下 app 路径本就不在 /Applications,不能动。
  if (process.platform !== 'darwin' || is.dev || !app.isPackaged) return false

  // 已在 /Applications(第二次及以后启动)-> 什么都不做,正常进。
  let alreadyInstalled = true
  try {
    alreadyInstalled = app.isInApplicationsFolder()
  } catch {
    // 某些沙盒/权限异常下该 API 可能抛;保守当作已安装,绝不阻塞启动。
    return false
  }
  if (alreadyInstalled) return false

  try {
    const moved = app.moveToApplicationsFolder({
      conflictHandler: (conflictType) => {
        // 已存在同名 app:让用户决定覆盖还是就地运行当前这份。
        // conflictType 取值是 'exists' / 'existsAndRunning'(Electron API 原文)。
        if (conflictType === 'exists') {
          const strings = selfInstallStrings()
          const response = dialog.showMessageBoxSync({
            type: 'question',
            buttons: [strings.replace, strings.keepBoth],
            defaultId: 0,
            cancelId: 1,
            title: strings.conflictTitle,
            message: strings.conflictTitle,
            detail: strings.conflictDetail,
          })
          // 返回 true = 继续搬家(丢弃旧版、装入这份);false = 放弃,原地运行。
          return response === 0
        }
        // 'existsAndRunning':旧版正在运行,无法安全覆盖 —— 提示用户退出旧版
        // 再重开安装包。此前静默 return false、原地从 DMG 运行,零反馈。
        showQuitRunningInstanceDialog()
        return false
      },
    })
    // moved===true 时 Electron 会拷贝到 /Applications、启动那一份并退出当前实例。
    return moved
  } catch (error) {
    // 搬家失败(权限/磁盘/只读目标等)绝不能卡死首启 —— 吞掉,原地运行。
    console.error('self-install: moveToApplicationsFolder failed', error)
    return false
  }
}
