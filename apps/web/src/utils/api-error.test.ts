import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  apiErrorStatus,
  isApiErrorCode,
  parseMemohError,
  resolveApiErrorMessage,
} from '@/utils/api-error'

describe('resolveApiErrorMessage', () => {
  let locale = 'en'

  beforeEach(() => {
    locale = 'en'
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => key === 'language' ? locale : null,
      setItem: vi.fn(),
      removeItem: vi.fn(),
      clear: vi.fn(),
    })
  })

  it.each([
    ['en', 'The transcription provider\'s rate limit or quota was reached. Check your quota or try again later.'],
    ['zh', '语音识别请求已达到频率或额度限制，请检查额度或稍后重试。'],
    ['ja', '音声認識のリクエスト頻度または利用上限に達しました。利用枠を確認するか、しばらく待ってからお試しください。'],
  ])('localizes transcription errors for %s using the stable code', (language, expected) => {
    locale = language
    const error = { code: 'transcription.rate_limited', status: 429, detail: 'private upstream diagnostic' }
    expect(resolveApiErrorMessage(error, 'fallback')).toBe(expected)
    expect(isApiErrorCode(error, 'transcription.rate_limited')).toBe(true)
  })

  it('renders ACP feedback i18n keys before raw backend messages', () => {
    locale = 'zh'

    const message = resolveApiErrorMessage({
      body: {
        code: 'no_workspace_exec',
        i18n_key: 'chat.acp.noWorkspaceExec',
        args: {},
        message: 'raw backend message',
      },
    }, 'fallback')

    expect(message).toBe('你没有执行该 Bot 工作区命令的权限。')
  })

  it('renders ACP feedback when structured payload is nested under message', () => {
    const message = resolveApiErrorMessage({
      message: {
        code: 'no_workspace_exec',
        i18n_key: 'chat.acp.noWorkspaceExec',
        args: {},
        message: 'raw backend message',
      },
    }, 'fallback')

    expect(message).toBe('You do not have permission to run workspace commands for this bot.')
  })

  it('renders ACP feedback when WebSocket stream errors carry it under feedback', () => {
    locale = 'zh'

    const message = resolveApiErrorMessage({
      type: 'error',
      message: 'raw backend message',
      feedback: {
        code: 'no_workspace_exec',
        i18n_key: 'chat.acp.noWorkspaceExec',
        args: {},
        message: 'raw backend message',
      },
    }, 'fallback')

    expect(message).toBe('你没有执行该 Bot 工作区命令的权限。')
  })

  it('falls back to existing detail extraction', () => {
    expect(resolveApiErrorMessage({ detail: 'plain detail' }, 'fallback')).toBe('plain detail')
  })

  it.each([
    ['en', 'Network connection failed. Check your connection and try again.'],
    ['zh', '网络连接失败，请检查网络后重试。'],
    ['ja', 'ネットワークに接続できません。接続を確認してもう一度お試しください。'],
  ])('localizes native fetch failures for %s', (language, expected) => {
    locale = language

    expect(resolveApiErrorMessage(
      new TypeError('NetworkError when attempting to fetch resource.'),
      'fallback',
    )).toBe(expected)
  })

  it.each([
    ['zh', '启动工作区失败'],
    ['ja', 'Workspace を起動できませんでした'],
  ])('localizes workspace errors for %s instead of exposing backend English', (language, expected) => {
    locale = language

    const message = resolveApiErrorMessage({
      code: 'workspace_start_failed',
      i18n_key: 'bots.container.startFailed',
      args: {},
      message: 'failed to start container: connection refused',
    }, 'fallback')

    expect(message).toBe(expected)
  })

  it.each([
    ['en', 'This name is already taken.'],
    ['zh', '该名称已被占用。'],
    ['ja', 'この名前はすでに使用されています。'],
  ])('derives the localized message from the stable code for %s', (language, expected) => {
    locale = language

    const error = {
      code: 'bot.name_taken',
      args: { field: 'name' },
      detail: 'This name is already taken.',
      request_id: 'req-1',
      status: 409,
    }

    expect(resolveApiErrorMessage(error, 'fallback')).toBe(expected)
    expect(isApiErrorCode(error, 'bot.name_taken')).toBe(true)
    expect(parseMemohError(error)).toEqual({
      code: 'bot.name_taken',
      args: { field: 'name' },
      message: 'This name is already taken.',
      requestId: 'req-1',
      status: 409,
    })
  })

  it.each([
    ['en', 'The workspace could not be reached.'],
    ['zh', '暂时无法连接工作区，请稍后重试。'],
    ['ja', 'Workspace に接続できません。しばらくしてからもう一度お試しください。'],
  ])('localizes workspace.unreachable for %s', (language, expected) => {
    locale = language

    expect(resolveApiErrorMessage({
      code: 'workspace.unreachable',
      args: {},
      detail: 'The workspace could not be reached.',
    }, 'fallback')).toBe(expected)
  })

  it('keeps unknown codes as open strings and uses their safe fallback', () => {
    const error = {
      code: 'future.new_condition',
      args: {},
      detail: 'A future error occurred.',
    }

    expect(parseMemohError(error)?.code).toBe('future.new_condition')
    expect(resolveApiErrorMessage(error, 'fallback')).toBe('A future error occurred.')
  })

  it('reads legacy HTTP status without parsing a message', () => {
    expect(apiErrorStatus({ response: { status: 409 }, message: 'legacy' })).toBe(409)
  })
})
