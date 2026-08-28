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

  it('reads error_code when code is absent', () => {
    expect(parseMemohError({
      error_code: 'agent.response_timeout',
      error: 'The model did not respond in time. Please try again.',
    })).toMatchObject({ code: 'agent.response_timeout' })
    expect(resolveApiErrorMessage({
      error_code: 'agent.response_timeout',
      error: 'backend fallback',
    }, 'fallback')).toBe('The model did not respond in time. Please try again.')
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
    ['en', 'skill.builtin_read_only', 'Built-in Skills are managed by Memoh and cannot be edited or deleted.'],
    ['zh', 'skill.builtin_read_only', 'Memoh 自带 Skill 由系统管理，无法编辑或删除。'],
    ['ja', 'skill.builtin_read_only', 'Memoh 組み込みの Skill はシステムによって管理されているため、編集または削除できません。'],
    ['en', 'skill.name_taken', 'A Skill with this name already exists. Choose a different name.'],
    ['zh', 'skill.name_taken', '已存在同名 Skill，请换一个名称。'],
    ['ja', 'skill.name_taken', '同じ名前の Skill がすでに存在します。別の名前を使用してください。'],
    ['en', 'skill.save_failed', 'The Skill could not be saved. Please try again.'],
    ['zh', 'skill.save_failed', 'Skill 保存失败，请重试。'],
    ['ja', 'skill.save_failed', 'Skill を保存できませんでした。もう一度お試しください。'],
    ['en', 'registry.unavailable', 'The Supermarket is unavailable. Please try again later.'],
    ['zh', 'registry.unavailable', '暂时无法连接 Supermarket，请稍后重试。'],
    ['ja', 'registry.unavailable', 'Supermarket に接続できません。しばらくしてからもう一度お試しください。'],
    ['en', 'registry.package_not_found', 'This package is no longer available.'],
    ['zh', 'registry.package_not_found', '该技能包已不存在。'],
    ['ja', 'registry.package_not_found', 'このパッケージは現在利用できません。'],
    ['en', 'registry.package_invalid', 'This package is invalid and cannot be installed.'],
    ['zh', 'registry.package_invalid', '该技能包无效，无法安装。'],
    ['ja', 'registry.package_invalid', 'このパッケージは無効なため、インストールできません。'],
    ['en', 'registry.package_install_failed', 'The package could not be installed. Please try again.'],
    ['zh', 'registry.package_install_failed', '技能包安装失败，请重试。'],
    ['ja', 'registry.package_install_failed', 'パッケージをインストールできませんでした。もう一度お試しください。'],
  ])('localizes %s error %s', (language, code, expected) => {
    locale = language
    expect(resolveApiErrorMessage({ code, detail: 'backend fallback' }, 'fallback')).toBe(expected)
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

  it.each([
    ['context.budget_unsatisfied', 'en', 'The model context window is too small for this request.'],
    ['context.budget_unsatisfied', 'zh', '模型上下文窗口不足，无法处理当前请求。'],
    ['context.budget_unsatisfied', 'ja', 'モデルのコンテキストウィンドウが不足しているため、このリクエストを処理できません。'],
    ['context.protected_overflow', 'en', 'Required context exceeds the model context budget.'],
    ['context.protected_overflow', 'zh', '必要的上下文内容超出了模型上下文预算。'],
    ['context.protected_overflow', 'ja', '必須コンテキストがモデルのコンテキスト予算を超えています。'],
  ])('localizes %s for %s', (code, language, expected) => {
    locale = language

    expect(resolveApiErrorMessage({
      type: 'error',
      code,
      message: 'backend English fallback',
    }, 'fallback')).toBe(expected)
  })

  it.each([
    ['agent.response_timeout', 'en', 'The model did not respond in time. Please try again.'],
    ['agent.response_timeout', 'zh', '模型未能及时响应，请重试。'],
    ['agent.response_timeout', 'ja', 'モデルから時間内に応答がありませんでした。もう一度お試しください。'],
    ['agent.response_interrupted', 'en', 'The model response was interrupted. Please try again.'],
    ['agent.response_interrupted', 'zh', '模型响应意外中断，请重试。'],
    ['agent.response_interrupted', 'ja', 'モデルの応答が中断されました。もう一度お試しください。'],
  ])('localizes structural stream failure %s for %s', (code, language, expected) => {
    locale = language

    expect(resolveApiErrorMessage({ code, detail: 'backend fallback' }, 'fallback')).toBe(expected)
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
