import { app, dialog, shell } from 'electron'
import { appendFileSync, mkdirSync, renameSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { inspect } from 'node:util'

// 主进程未捕获错误的兜底。
//
// Electron 的默认处理是:进程上没有其他 uncaughtException 监听时,弹出
// 「A JavaScript error occurred in the main process」原生框并展示堆栈。对用户
// 来说这个框只有看不懂的堆栈,没有任何可操作项;而最常见的来源是网络请求被
// 重置/超时这类暂时性错误(某条请求路径漏挂 error 监听就会冒到这里,例如
// updates.ts 里处理过的自动更新响应),下一次请求自然恢复。
//
// 因此这里接管处理:所有未捕获错误都落盘到日志文件,供下次复现时定位源头;
// 网络类错误只记日志;其他错误仍然弹框,但说明日志位置。真实 bug 不能被静默
// 吞掉,否则 app 可能停在半坏状态而无人知晓。
//
// unhandledRejection 只记日志不弹框:Electron 默认对它只打印警告,改成弹框会让
// 现有被忽略的 rejection 突然打扰用户。

const LOG_FILE_NAME = 'main.log'
const ROTATED_LOG_FILE_NAME = 'main.old.log'
// 单个文件的上限;超出时整体轮转到 main.old.log,最多保留两份。
const MAX_LOG_BYTES = 1024 * 1024

// Node / undici 的连接层错误码,Electron net 栈的错误则以 net::ERR_ 开头的
// message 表达(没有 code)。
const NETWORK_ERROR_CODES = new Set([
  'ECONNRESET',
  'ECONNREFUSED',
  'ECONNABORTED',
  'ETIMEDOUT',
  'EPIPE',
  'ENOTFOUND',
  'EAI_AGAIN',
  'ENETDOWN',
  'ENETUNREACH',
  'EHOSTUNREACH',
  'UND_ERR_SOCKET',
  'UND_ERR_CONNECT_TIMEOUT',
  'UND_ERR_HEADERS_TIMEOUT',
  'UND_ERR_BODY_TIMEOUT',
])

type ErrorStrings = {
  message: string
  detail: (logPath: string) => string
  showLog: string
  ok: string
}

// 文案跟随系统语言,理由同 arch-check.ts:主进程读不到应用内语言偏好。
const ERROR_STRINGS: Record<'en' | 'zh' | 'ja', ErrorStrings> = {
  en: {
    message: 'Memoh ran into an unexpected error',
    detail: logPath => `Memoh may not work correctly until it is restarted. Details were saved to:\n${logPath}`,
    showLog: 'Show Log',
    ok: 'OK',
  },
  zh: {
    message: 'Memoh 遇到了意外错误',
    detail: logPath => `重启 Memoh 之前，部分功能可能无法正常工作。详细信息已保存到：\n${logPath}`,
    showLog: '查看日志',
    ok: '好',
  },
  ja: {
    message: 'Memoh で予期しないエラーが発生しました',
    detail: logPath => `Memoh を再起動するまで、一部の機能が正しく動作しない可能性があります。詳細は次の場所に保存されました：\n${logPath}`,
    showLog: 'ログを表示',
    ok: 'OK',
  },
}

let installed = false
// 同一个错误在循环里反复抛出时,不能叠出一串弹框;框开着期间只记日志。
let dialogOpen = false

export function installMainProcessErrorHandlers(): void {
  if (installed) return
  installed = true
  // stdout/stderr 是启动方给的管道:从终端启动后终端退出,往里写就会异步报
  // EPIPE。没有监听时它会成为 uncaughtException,而下面的处理器自己也会
  // console.error,于是再报 EPIPE,形成死循环(CPU 空转、日志反复写满轮转)。
  // 终端输出只是日志文件之外的副本,写不出去时没有可做的补救,直接忽略。
  for (const stream of [process.stdout, process.stderr]) stream.on('error', () => {})
  // 监听体里任何一步再抛(比如被抛出的值连描述都生成不了),Node 会把它当作
  // 处理器自身失败直接结束进程,既不记日志也不弹框,比没有兜底更糟。所以整体
  // 兜住,最多退化成只在终端留一行。
  process.on('uncaughtException', (error, origin) => {
    try {
      const network = isNetworkError(error)
      writeErrorLog(origin, error, network)
      if (!network) showErrorDialog()
    } catch (handlerError) {
      reportHandlerFailure(handlerError)
    }
  })
  process.on('unhandledRejection', (reason) => {
    try {
      writeErrorLog('unhandledRejection', reason, isNetworkError(reason))
    } catch (handlerError) {
      reportHandlerFailure(handlerError)
    }
  })
}

function reportHandlerFailure(handlerError: unknown): void {
  try {
    console.error('[main] error handler failed', handlerError)
  } catch {
    // 终端也写不了时已无处可报。
  }
}

function isNetworkError(value: unknown, depth = 0): boolean {
  if (!value || typeof value !== 'object' || depth > 4) return false
  const { message, code, cause } = value as { message?: unknown, code?: unknown, cause?: unknown }
  if (typeof message === 'string' && message.startsWith('net::ERR_')) return true
  if (typeof code === 'string' && NETWORK_ERROR_CODES.has(code)) return true
  // undici 的 fetch 把底层连接错误包成 TypeError('fetch failed'),原因在 cause 上。
  return isNetworkError(cause, depth + 1)
}

function logPath(): string {
  return join(app.getPath('logs'), LOG_FILE_NAME)
}

function describeError(value: unknown, depth = 0): string {
  // 非 Error 的值用 inspect:String() 会把普通对象记成 [object Object],遇到无原型
  // 对象还会直接抛错。
  if (!(value instanceof Error)) return inspect(value, { depth: 3 })
  const text = value.stack || `${value.name}: ${value.message}`
  if (value.cause === undefined || depth >= 4) return text
  return `${text}\nCaused by: ${describeError(value.cause, depth + 1)}`
}

function writeErrorLog(origin: string, error: unknown, network: boolean): void {
  const description = describeError(error)
  console.error(`[main] ${origin}${network ? ' (network)' : ''}:`, description)
  try {
    const path = logPath()
    mkdirSync(app.getPath('logs'), { recursive: true })
    try {
      if (statSync(path).size > MAX_LOG_BYTES) renameSync(path, join(app.getPath('logs'), ROTATED_LOG_FILE_NAME))
    } catch {
      // 文件还不存在,直接写新文件。
    }
    const header = `[${new Date().toISOString()}] ${origin}${network ? ' network' : ''} app=${app.getVersion()}`
    appendFileSync(path, `${header}\n${description}\n\n`, { mode: 0o600 })
  } catch (logError) {
    // 写日志失败不能再抛:这里本身就在处理未捕获错误,再抛会进入同一个监听。
    console.error('[main] failed to write error log', logError)
  }
}

function errorStrings(): ErrorStrings {
  const locale = app.getLocale().toLowerCase()
  if (locale.startsWith('zh')) return ERROR_STRINGS.zh
  if (locale.startsWith('ja')) return ERROR_STRINGS.ja
  return ERROR_STRINGS.en
}

function showErrorDialog(): void {
  if (dialogOpen) return
  const strings = errorStrings()
  const path = logPath()
  // ready 之前只有 showErrorBox 可用(同步、无按钮)。
  if (!app.isReady()) {
    dialog.showErrorBox(strings.message, strings.detail(path))
    return
  }
  dialogOpen = true
  void dialog.showMessageBox({
    type: 'error',
    message: strings.message,
    detail: strings.detail(path),
    buttons: [strings.ok, strings.showLog],
    defaultId: 0,
    cancelId: 0,
  }).then(({ response }) => {
    if (response === 1) shell.showItemInFolder(path)
  }).catch((error) => {
    console.error('[main] failed to show error dialog', error)
  }).finally(() => {
    dialogOpen = false
  })
}
