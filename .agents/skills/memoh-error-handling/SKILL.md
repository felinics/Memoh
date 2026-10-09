---
name: memoh-error-handling
description: |
  Memoh 统一错误处理架构（apperror + Problem Details + SSE envelope + 前端稳定 code 解析）。
  新增任何面向用户的后端错误、或前端需要根据错误做业务分支时，必须先读本 skill。
  核心约束：业务判断只依赖稳定 code，用户文案由前端按语言本地化，私有诊断永不进入响应。
metadata:
  trigger: 新增/修改后端错误响应、SSE 错误事件、前端错误分支或错误文案；审查错误处理相关改动
  pilot: bot.name_taken（HTTP 409）、workspace.unreachable（HTTP 503 + SSE）、workspace.display_prepare_failed（SSE），参照实现文件见各节开头
---

# Memoh 错误处理架构

## 不变量（违反任何一条即为架构回归）

1. **稳定身份**：业务逻辑和客户端分支只依赖机器可读的 `code`（如 `bot.name_taken`），
   永远不匹配英文句子、不匹配 HTTP status 猜业务含义（legacy 兼容兜底除外，见下）。
2. **前端本地化**：用户看到的文案由前端按当前语言从 `errors.<code>` i18n 键渲染；
   后端 `detail` 只是英文兜底（Content-Language: en）。
3. **私有诊断不出网**：底层 cause（dial error、stderr、provider 响应）只进服务端日志，
   不进任何用户响应。`args` 走 catalog 白名单（`AllowedArgs`），未声明的键在构造时即被丢弃。
4. **传输可异、语义同源**：HTTP 用 `application/problem+json`，SSE 用各自 event envelope，
   但 code/args/detail/fault 全部来自同一个 `errs.Answer` 的选择，禁止各端点自造。
5. **传输错误也是 Problem**：`*echo.HTTPError`（路由不存在、方法不允许、body 过大，以及 handler
   返回的）在 `errs.Answer` 选择之前转成其状态对应的 `http.*` code，原错误作为 cause；
   没有专用 code 的 4xx 为 `http.bad_request`，5xx 为 `internal`。它的 message 不下发，
   handler 有 cause 时用 `WithInternal(err)` 附上，进结果记录
   （`internal/server/error_handler_test.go` 的 `TestBoundaryAnswersEveryErrorWithAProblem` 锁定此行为）。
   所以 handler 不要用 `echo.NewHTTPError(4xx, "...")` 告诉用户哪里错了，用户看不到：
   缺字段用 `apperror.FieldRequired("字段名")` 或 `httpx.RequiredParam/RequiredQuery`，
   字段值不合法用 `apperror.FieldInvalid("字段名", err)`。字段名照请求里的写法写成字面量
   （JSON 键、query 或路径参数名，大小写不改，嵌套用点）。修正需要的不止字段名时，
   在该领域建自己的 code。绑定类型错误由 `httpx.Binder` 自动给出字段，`c.Bind` 的错误直接返回。

## 后端：新增一个错误的标准步骤

参照实现：`internal/apperror/`（catalog）、`internal/server/error_handler.go` 与 `problem.go`（HTTP 渲染）、
`internal/server/stream_error.go` 与 `internal/handlers/display.go` 的 `PrepareDisplay`（SSE 错误帧）。

1. **catalog 注册**（`internal/apperror/error.go`）：
   ```go
   CodeXxxYyy Code = "xxx.yyy"   // 段间用点分层（<域>.<条件>），段内 snake_case
   //   ✓ workspace.display_prepare_failed   ✗ workspace_display.prepare-failed
   ```
   catalog 条目给 `HTTPStatus`、英文 `Detail`、`AllowedArgs` 白名单（没有公开参数就不写）。
   目前只走 SSE 的错误也必须给 `HTTPStatus`（选语义最接近的档，如中途失败 500、
   连接不上 503）——同一 code 未来出现在 HTTP 路径时不允许再改档。
2. **调用点**：领域 sentinel error → `apperror.New(code, args)`（无底层 cause）
   或 `apperror.Wrap(code, cause, args)`（保留 cause 供日志）。映射放在 handler 边界，
   不要让 apperror 渗入领域层。
3. **HTTP 路径零额外代码**：直接 `return` 该 error。`server.NewHTTPErrorHandler` 用 `errs.Answer`
   选出公共错误，渲染成带 `request_id` 的 `server.Problem`。这个请求只有一条结果记录，
   是 `server.AccessLog` 写的 access 记录（`msg=request`），错误字段和级别由 `errlog.Finish`
   按 fault 给出。handler 和下层 helper 不记日志，否则同一失败会出现第二条记录。
4. **SSE / WebSocket 路径**（流已打开）：`frame, rendered := server.NewStreamError(ctx, err, requestID)`
   取得错误帧（code/args/catalog detail/fault/request_id，`Message` 重复 detail，不含错误原文），
   发出帧后 `return rendered`。响应已提交，access 记录按同一规则归因 `rendered`，不会再写一次响应；
   handler 不记日志。客户端断开导致的写失败不算失败，不返回。参照 `display.go` 的 `fail`。
   后台工作（标题生成、记忆写入、压缩、bot 删除、工作区 provision/teardown、schedule 触发、
   后台命令）用 `job.Go` / `job.Run` 包成一个单元，单元结束时写一条 `msg=job` 记录
   （`errlog.Finish` 带 `Async: true`，成功为 INFO）。单元内的代码返回错误，不记日志；
   处理后继续的失败记 `errlog.Event`（WARN）。同一进程里已有内层单元记过的失败
   （run 的 `agent run` 记录）由生产方套 `errs.Recorded`，外层记录最高 WARN。
5. **OpenAPI**：handler 加 `@Failure <status> {object} server.Problem` 注解，
   然后 `mise run swagger-generate && mise run sdk-generate`。spec/SDK 的 diff 必须全部由再生成解释。

## 前端：消费错误的标准姿势

共享工具：`apps/web/src/utils/api-error.ts` + `apps/web/src/composables/api/sse-error.ts`。

- **业务分支**：`isApiErrorCode(error, 'xxx.yyy')` 或 `parseMemohError(error)?.code`。
  禁止 `message.includes('...')` 判断业务状态（legacy 兼容兜底除外，必须带注释说明目标旧版本）。
- **文案**：Web 与 IM 的三语言 locale 各加 `errors.xxx.yyy` 键（code 的点 = JSON 嵌套层级）。
  `errors` 下的键按字母序放（含嵌套层），`codes.golden` 也按 code 排序；新 code 用
  `go test ./internal/apperror -run TestCatalogGolden -update-golden` 写入。
  `resolveApiErrorMessage` 自动按 `errors.<code>` → `i18n_key`（legacy）→ `detail` 顺序渲染。
  **错误文案是 UX，不是英文 detail 的翻译**：要回答用户"接下来能做什么"——
  可重试的说"请稍后重试"（如 `workspace.unreachable` 的 zh 文案），需要用户改输入的
  指向那个输入（如 `bot.name_taken`）；无法行动的错误才允许只陈述事实。
- **HTTP status**：`apiErrorStatus(error)`。Problem body 自带 `status` 字段——这是刻意设计，
  因为 hey-api client `throw jsonError` 只抛 body、会丢 HTTP status，不要"优化"掉 body 里的 status。
- **SSE 流**：新流复制现有三件套模式（`useBotCreateStream.ts` 为范例）：
  event 类型并入 `SSEErrorEvent`、`fetch: fetchSSEProblem`（把流前 HTTP 拒绝解码为结构化错误）、
  yield 时 `localizeSSEErrorEvent`、收尾 `normalizeSSEFailure`。
  **必须设 `sseMaxRetryAttempts: 1`** —— `fetchSSEProblem` 靠 throw 中断连接，
  不设上限会进 SSE 客户端的无限重试循环。

## Legacy 兼容边界（读懂再动）

- 旧服务器（桌面端可能连接）返回 `{"message":"..."}`、无 code 无 status。
  对这类响应的英文匹配兜底**允许存在但必须带注释**（范例：files-pane 的
  `isTransientWorkspaceError`）。删除兜底 = 明确决定放弃对应旧版本，需报备。
- 服务端不再发 legacy SSE error：流错误帧由 `server.NewStreamError` 生成，
  HTTP 错误一律是 Problem（含 `echo.HTTPError`，见不变量 5）。前端渲染链不读 `i18n_key`。
- `bot-create-progress` 里 409→`bot.name_taken` 的启发式是旧服务器兜底，
  新增场景禁止模仿"从 status 猜 code"。

## 验证（新增/修改错误后必跑）

```bash
go test ./internal/apperror/... ./internal/server/... ./internal/handlers/...
cd apps/web && pnpm vitest run src/utils/api-error.test.ts src/composables/api/sse-error.test.ts
```

有运行环境时实测 wire 形状（登录取 token 后）：

```bash
# HTTP：期望 application/problem+json + code/args/request_id，body 无底层诊断
curl -si -X POST $HOST/bots -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  -d '{"name":"<已存在的名字>","display_name":"x"}'          # 409 bot.name_taken
curl -si "$HOST/bots/$BOT/container/fs/list?path=/" -H "Authorization: Bearer $T"
#   ↑ 先 POST .../container/stop 再测，期望 503 workspace.unreachable
# SSE：期望 data: {"type":"error","code":"...","request_id":"..."}，无 stderr/dial 细节
curl -sN -X POST "$HOST/bots/$BOT/container/display/prepare" \
  -H "Authorization: Bearer $T" -H 'Accept: text/event-stream'
```

泄漏自查一句话：把 cause 换成 `errors.New("SECRET")`，任何用户可见输出里 grep 不到 SECRET。

## 已知坑（试点审查沉淀）

- **迁移一个 handler 时列全它的错误出口再动手**（`grep -n 'NewHTTPError\|sendError'
  该文件`，别用 head 截断）。端点级迁移最容易漏"操作中途"的出口：
  `getGRPCClient` 成功不等于 workspace 可用——连接池缓存的 client 会在首次调用
  （Mkdir/WriteFile）才失败，这些 per-op 错误若还是 `fmt.Sprintf("%v", err)`
  就把 unix socket 路径泄给用户（skills.go 迁移时实测抓到的真实泄漏）。
  bridge 操作错误一律走 `fsHTTPError` 分类，不要手写 500。
- `apperror.Error` **刻意不实现 `Unwrap`**：`errors.Is(err, cause)` 穿不透，取 cause 用
  `apperror.CauseOf`。这是防止基础设施错误变成 API 契约，不是疏漏，别"修"它。
- Problem 的 `detail` 英文文案与前端 `en.json` 是双源，改文案要两边同步
  （catalog 处有 `codesync(error-catalog)` 注释；detail 仅是无 locale 时的兜底）。
- code 语义要精确：连接不上是 `workspace.unreachable`，连上后中途失败是
  `workspace.display_prepare_failed`——不要把"操作失败"笼统映射到连接性 code，
  文案会误导用户；语义不同就开新 code。
- 面向用户的响应里不放 stderr/exit code 等诊断——记日志（带 `request_id`），
  用户侧只给 catalog detail。
- `requestID` 统一用 `internal/httpx.RequestID`，不要在包内再写局部副本。
