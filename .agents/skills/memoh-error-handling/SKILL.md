---
name: memoh-error-handling
description: |
  Memoh 统一错误契约的操作手册（apperror Kind/Code 模型 + 四传输渲染 + 前端稳定 code 解析）。
  新增任何错误出口、迁移 handler、或前端需要根据错误做业务分支时，必须先读本 skill。
  规范条文与理由见 docs/architecture/error-contract-decision.md，冲突时以该文件为准。
metadata:
  trigger: 新增/修改错误出口、迁移 echo.NewHTTPError、SSE 错误事件、前端错误分支或错误文案；审查错误处理相关改动
  contract: docs/architecture/error-contract-decision.md
---

# Memoh 错误处理操作手册

## 核心心智模型

错误只有一个载体 `internal/apperror.Error`，身份分三段：

- **Kind**（必填，12 档封闭枚举）—— 决定 HTTP status / gRPC code / JSON-RPC code。绝大多数
  错误只需要 Kind。
- **Code**（可选）—— 只在客户端会因它做不同的事时才有。catalog 是用户体验清单，不是错误清单。
- **op + cause**（永不出网）—— 只进日志。

最常犯的错是"觉得每个错误都得有个 code"。不是。`type: about:blank` 是 RFC 9457 明确允许的，
无 code 的错误照样有 status、有本地化文案（走 `errors.kind.*`）、有 request_id。

## 写一个错误出口

```go
// 默认姿势：只给 Kind。不需要注册任何东西。
return apperror.Internal("archive directory", err)
return apperror.Invalid("bot id", nil)
return apperror.NotFound("bot", err)
return apperror.Unavailable("workspace", err)

// 需要前端分支或专属文案时，才升级为带 code
return apperror.Conflict("create bot", err).
	WithCode(apperror.CodeBotNameTaken, map[string]string{"field": "name"})

// 输入类错误：用 FieldCode，不要发业务码
return apperror.Required("bot_id")
return apperror.Field("name", apperror.FieldTaken)
return apperror.Invalid("update profile", nil).WithFields(
	apperror.FieldError{Pointer: "title_model", Code: apperror.FieldUnsupported},
)
```

12 个构造器：`Internal` `Invalid` `Unauthenticated` `Forbidden` `NotFound` `Conflict`
`FailedPrecondition` `Exhausted` `Canceled` `DeadlineExceeded` `Unavailable` `Unimplemented`。

HTTP 路径**零额外代码**：直接 `return`，全局 handler 渲染 Problem，自动带 `request_id`，
≥500 自动记 cause 日志。

### op 怎么写

`op` 是代码位置的静态描述，小写动宾或资源名。**禁止拼接任何运行时值**（ID、路径、用户输入）
—— 它会进 `error.type` 指标标签，高基数打爆后端，而且是泄漏面。

```go
apperror.NotFound("bot", err)                       // ✓
apperror.Internal("archive directory", err)         // ✓
apperror.NotFound("bot "+botID, err)                // ✗ 高基数 + 泄漏
apperror.Internal(fmt.Sprintf("read %s", path), err) // ✗ 同上
```

### cause 怎么给

底层错误原样传入，**不要**先 `err.Error()` 再拼字符串。cause 通过 `Unwrap()` 可达，
`errors.Is(err, context.Canceled)` 能正常穿透；但它永远不会出现在任何用户可见输出里。

```go
apperror.Unavailable("workspace", err)                          // ✓
apperror.Unavailable("workspace: "+err.Error(), nil)            // ✗ 泄漏进 op
echo.NewHTTPError(503, "workspace unreachable: "+err.Error())   // ✗ 已被 forbidigo 拦截
```

## 三档决策：先问自己错误属于哪一档

迁移和新增时按这个顺序判断，**绝大多数出口落在前两档，不需要碰 catalog**：

1. **5xx** → 只给 Kind。用户无法据此行动，具体信息对他没价值，全部进日志。
   `apperror.Internal("archive directory", err)`
2. **输入被拒的 4xx** → 用 `FieldCode`。这是数百处 `"x is required"` 的归宿。
   `apperror.Required("bot_id")` / `apperror.Field("name", apperror.FieldTaken)`
   六个码：`FieldRequired` `FieldInvalid` `FieldTooLong` `FieldOutOfRange` `FieldTaken`
   `FieldUnsupported`。前端按 `errors.field.<code>` 渲染并绑到输入控件。
3. **用户可行动的业务失败** → 才注册 catalog code，见下节。

没有第四档。**不存在"塞一句自由英文给用户看"的选项** —— 契约刻意不提供这样的字段，
因为它会立刻变成惰性路径和泄漏面（理由见决策文档 §2.7）。如果你觉得三档都不合适，
那说明它属于第 3 档，去注册一个 code 并写三份文案。

## 新增一个 code（需要判断，别顺手做）

**准入判据只有一条：客户端会不会因为这个 code 做不同的事。** 不同的 UI 分支，或给用户不同的
下一步指引。表现和通用错误一模一样的，不要发明 code。

反例：`profile.update_failed` → "The profile could not be updated." 这和通用 500 在用户侧
表现完全一致，它的存在只是让 catalog 多一行、locale 多三个键。

正例：`workspace.unreachable` 虽然也是 5xx，但前端要显示重连状态、提示用户稍后重试，
和通用 500 的处理路径不同。

判定通过后：

1. `internal/apperror/catalog.go` 注册。命名 `<域>.<条件>`，段内 snake_case。
   给 `HTTPStatus`、英文 `Detail`、`AllowedArgs` 白名单（没有公开参数就不写）。
2. 三份 locale 各加 `errors.<code>` 键（code 的点 = JSON 嵌套层级）。
   **错误文案是 UX 不是翻译**：要回答"接下来能做什么"。可重试的说"请稍后重试"，
   要改输入的指向那个输入；无法行动的才只陈述事实。
3. handler 加 `@Failure <status> {object} apperror.Problem` 注解，
   然后 `mise run swagger-generate && mise run sdk-generate`。

**并行迁移期间禁止新增 code** —— catalog 是唯一的串行资源，并行改必冲突。发现该有 code 的
位置写进 PR 描述，由串行 pass 统一裁决。

## echo.NewHTTPError 什么时候还能用

只有一种情况：拒绝的是请求的**信封**而不是内容，且**不带任何消息**。

```go
echo.NewHTTPError(http.StatusRequestEntityTooLarge)   // ✓ 405 / 413 / 415 / 426 四个
echo.NewHTTPError(http.StatusRequestEntityTooLarge, "media is too large")  // ✗ 闸门拒绝
echo.NewHTTPError(http.StatusNotFound)                // ✗ 404 有 Kind，用 apperror.NotFound
```

这四个状态码 gRPC 没有对应 code，Kind 刻意不建模它们。全局渲染器仍会把它们渲染成 Problem
并原样透传状态码。消息一律不给：状态码已经把这类拒绝要说的话说完了。

`internal/apperror/adoption_test.go` 是闸门，AST 检查，越界直接失败。

## SSE / WebSocket

流用**扁平**投影，不是 Problem。Problem 是一元 HTTP 响应的形状；流已经发了 200，
`status`/`title`/`type` 在流里没有意义，而且扁平形是 OpenAI 兼容生态的事实标准。

```json
{ "type": "error", "kind": "unavailable", "code": "workspace.unreachable", "args": {}, "detail": "...", "request_id": "..." }
```

字段全部来自 `apperror.PublicOf(err, requestID)`，端点只负责加 `type`。不得自造字段。

## provider 错误要原样透传

调 model provider 时我们只是代理。"You exceeded your current quota" 只有拥有那个账号的
用户能处理，换成我们的通用文案等于把响应里唯一有用的东西删了。

```go
apperror.Internal("forward ws stream", err).WithUpstream("openai", text)
```

`upstream` 是独立成员，**不要**把 provider 原文塞进 `detail` 或 `op`：`detail` 是我们的、
已本地化的，`message` 是外来的、语言由 provider 决定，合并之后客户端分不清哪一半能翻译。
Kind 照常分类，引述只补充"对方说了什么"。

**传进去之前必须已经擦掉密钥** —— apperror 不知道什么是密钥。注意 chat 路径当前还没做擦除
（`ChatModel.SanitizeError` 无调用者），这是已知缺口。

新流复制现有三件套：event 类型并入 `SSEErrorEvent`、`fetch: fetchSSEProblem`（把流前 HTTP
拒绝解码为结构化错误）、收尾 `normalizeSSEFailure`。

**必须设 `sseMaxRetryAttempts: 1`** —— `fetchSSEProblem` 靠 throw 中断连接，不设上限会进 SSE
客户端的无限重试循环。

## 前端消费

共享工具：`apps/web/src/utils/api-error.ts` + `apps/web/src/composables/api/sse-error.ts`。

- **业务分支**：`isApiErrorCode(error, 'xxx.yyy')` 或 `parseMemohError(error)?.code`。
  禁止 `message.includes('...')` 判断业务状态。
- **文案**：`resolveApiErrorMessage` 按 `errors.<code>` → `errors.upstream.*`（provider 引述）
  → `errors.field.<code>` → `errors.kind.<kind>` → `detail` 顺序渲染。无 code 的错误落到
  `errors.kind.*`，所以它也是本地化的；provider 原文不翻译，只套一层标注来源的外框。
- **HTTP status**：`apiErrorStatus(error)`。Problem body 自带 `status` 是刻意设计 ——
  hey-api client `throw jsonError` 只抛 body 会丢 HTTP status，不要"优化"掉它。

## 验证

```bash
go test ./internal/apperror/... ./domains/api/http/...
cd apps/web && pnpm vitest run src/utils/api-error.test.ts src/composables/api/sse-error.test.ts
```

泄漏测试是表格驱动的，遍历全部注册路由、把 cause 换成 `errors.New("SECRET")` 断言响应体不含
该串。新增路由如果绕过了 `apperror`，这个测试会失败 —— 这是刻意的。

有运行环境时实测 wire 形状：

```bash
# HTTP：期望 application/problem+json + code/request_id，body 无底层诊断
curl -si -X POST $HOST/bots -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  -d '{"name":"<已存在的名字>","display_name":"x"}'          # 409 bot.name_taken
# SSE：期望 data: {"type":"error","problem":{...}}，无 stderr/dial 细节
curl -sN -X POST "$HOST/bots/$BOT/container/display/prepare" \
  -H "Authorization: Bearer $T" -H 'Accept: text/event-stream'
```

## 已知坑

- **`apperror.Error` 实现 `Unwrap`。** 这是 v1 契约的有意改变（旧版刻意不实现）。防泄漏由
  渲染层保证 —— 渲染器只读 Kind/Code/args，物理上碰不到 cause。所以可以放心 `errors.Is`
  穿透到底层，但**不要**对 `apperror.Error` 自身做 `errors.Is` 判等，分类走 `KindOf`/`CodeOf`。
- **502 折叠进 `Unavailable` 只对显式写了 `Unavailable` 的调用点成立。** 渲染器对非契约
  的 echo 错误原样透传状态码，不再按 Kind 推导 —— 否则 413 会被静默改写成 400。
- **code 语义要精确**：连接不上是 `workspace.unreachable`，连上后中途失败是
  `workspace.display_prepare_failed`。不要把"操作失败"笼统映射到连接性 code，文案会误导用户。
- Problem 的英文 `detail` 与前端 `en.json` 是双源，改文案两边同步（catalog 处有
  `codesync(error-catalog)` 注释）。有测试断言 catalog 与三份 locale 的键集合一致。
- `requestID` 统一用 `domains/api/http/httpx.RequestID`，不要在包内再写局部副本。
- 领域内核（实体、值对象、领域服务）**禁止**依赖 `apperror`；application/service 层可以。
  边界由 review 保证（见根 AGENTS.md 的分层契约），不用守卫测试复述。
