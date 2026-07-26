# Error Contract Decision

状态：**Accepted**

日期：2026-07-25

本文件冻结 Memoh 全项目的错误契约：错误在代码内部如何表示、如何跨层传播、如何渲染到
四种传输。若 skill、AGENTS.md 或既有实现与本文冲突，以本文为准。

本决策不修改数据库、RPC 部署边界、目录 owner 归属或 build profile。它只规定错误的
类型、身份、渲染与约束手段。

## 1. 决策摘要

1. 全项目唯一的错误载体是 `internal/apperror.Error`。禁止新增任何其他"对外错误"结构。
2. 错误身份分三段：`Kind`（必填，封闭枚举）、`Code`（可选，稳定业务码）、`op` + `cause`
   （永不出网，仅日志）。
3. `Kind` 取自 gRPC canonical code 并折叠为 12 档，是传输中立的类别词汇表。它决定
   HTTP status、gRPC code 与 JSON-RPC code，本身不含任何 HTTP 概念。
4. `Code` 是可选的。RFC 9457 允许 `type: about:blank`，因此绝大多数错误不需要 code。
   catalog 是用户体验清单，不是错误清单。
5. 对外 wire 形状只有一套语义源：`apperror` 渲染器。HTTP 用 RFC 9457 Problem Details，
   SSE/WS 内嵌同一个 Problem，gRPC 用 `google.rpc.Status`，MCP 用 JSON-RPC error object。
6. 全局错误渲染器兜底**所有** error，不要求 catalog 命中。未识别错误按 Kind 渲染。
7. 5xx 响应**永不**渲染 handler 提供的 message，一律替换为 Kind 通用文案。原 message 与
   cause 只进日志。
8. `apperror.Error` 实现 `Unwrap`。防泄漏由渲染层保证，不靠阉割类型。
9. 分层职责：infra 翻译、domain 判定、transport 只渲染。禁止手写 `xxxHTTPError` 映射函数。
10. 任何不变量必须有 linter 或测试兜底。写不出闸门的规则不写进文档。

## 2. 内部模型

### 2.1 三段身份

| 段 | 必填 | 出网 | 用途 |
| --- | --- | --- | --- |
| `Kind` | 是（零值 `KindInternal`） | 是，映射为 status 与通用 title | 错误类别 |
| `Code` | 否 | 有则出网 | 前端分支与本地化的依据 |
| `op` + `cause` | 否 | **永不** | 日志定位 |

### 2.2 Kind 枚举

封闭枚举，12 档。**新增 Kind 需要修改本文，不是日常操作。**

```go
type Kind uint8

const (
	KindInternal Kind = iota // 零值：未分类一律落此档
	KindInvalid
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindFailedPrecondition
	KindExhausted
	KindCanceled
	KindDeadlineExceeded
	KindUnavailable
	KindUnimplemented
)
```

### 2.3 Kind 到传输的映射（规范性，唯一真源）

| Kind | HTTP | gRPC | JSON-RPC |
| --- | --- | --- | --- |
| `KindInternal` | 500 | `INTERNAL` | -32603 |
| `KindInvalid` | 400 | `INVALID_ARGUMENT` | -32602 |
| `KindUnauthenticated` | 401 | `UNAUTHENTICATED` | -32001 |
| `KindForbidden` | 403 | `PERMISSION_DENIED` | -32003 |
| `KindNotFound` | 404 | `NOT_FOUND` | -32004 |
| `KindConflict` | 409 | `ALREADY_EXISTS` | -32009 |
| `KindFailedPrecondition` | 422 | `FAILED_PRECONDITION` | -32022 |
| `KindExhausted` | 429 | `RESOURCE_EXHAUSTED` | -32029 |
| `KindCanceled` | 499 | `CANCELLED` | -32099 |
| `KindDeadlineExceeded` | 504 | `DEADLINE_EXCEEDED` | -32005 |
| `KindUnavailable` | 503 | `UNAVAILABLE` | -32000 |
| `KindUnimplemented` | 501 | `UNIMPLEMENTED` | -32601 |

gRPC 入站折叠：`ABORTED` 与 `OUT_OF_RANGE` 分别归入 `KindConflict` 与 `KindInvalid`；
`DATA_LOSS` 与 `UNKNOWN` 归入 `KindInternal`。

三处有意偏离，理由记录如下：

- **`KindFailedPrecondition` 映射 422 而非 gRPC 官方的 400。** "语法合法但语义被拒"与
  "入参格式错误"对前端是两种处理路径，值得用不同 status 区分。既有的
  `workspace.image_incompatible` 已经是 422。
- **502 折叠进 `KindUnavailable`，统一渲染为 503。** 502 与 503 语义上都是上游不可用，
  不值得为此保留一档 Kind。此项改变了 `acp.config_update_failed` 等端点的 wire status。
- **`KindCanceled` 映射 499。** 499 不是 IANA 注册 status。客户端已断开时通常不产生响应，
  该档主要用于日志与指标，渲染路径正常不会走到。

### 2.4 Code 规则

- 命名 `<域>.<条件>`，段间以点分层，段内 `snake_case`。域取 `domains/` 下的目录名或明确子域。
- **准入唯一判据：客户端会因此码做不同的事** —— 不同的 UI 分支，或给用户不同的下一步指引。
  表现与通用错误无差异的，禁止发明 code。这条不是本项目的发明：Zitadel 在
  [#11917](https://github.com/zitadel/zitadel/issues/11917) 里给出同样的措辞，Grafana 的
  `errutil` 文档把它拆成"最大化/最小化"双规则 —— 需要不同公开文案就分开，内部可区分但对外
  不该区分（登录失败是用户不存在还是密码错）就**故意合并**。
- 反面标尺是 Mattermost：`AppError.Id` 即 i18n key，`server/i18n/en.json` 现有 3495 个键、
  其中 2111 个以 `.app_error` 结尾。那不是设计出来的规模，是没有准入判据的累积结果。
- Code 一旦发布，语义永不变更。只能废弃（保留并行期），不能改义。文案可自由迭代。
- Code 存在时，catalog 的 `HTTPStatus` 覆盖 Kind 的默认 status；但 `Kind` 仍必须正确填写，
  因为 gRPC 与 JSON-RPC 两条渲染路径只看 Kind。
- catalog 的健康规模是几十条。它随用户体验需求增长，不随错误数量增长。

### 2.5 op 与 cause

- `op` 是**代码位置的静态描述**，小写，动宾短语或资源名，例如 `create bot`、
  `archive directory`。**禁止拼接 ID、路径、用户输入或任何运行时值**：`op` 会进入
  指标标签，高基数会打爆指标后端，同时它也是潜在的泄漏面。
- `op` 存在的理由不是"给人看的上下文"—— 那用 `fmt.Errorf("op: %w", err)` 就够了。它的理由
  是**结构化**：作为独立字段它能直接做 slog attribute 和低基数指标维度，字符串拼接进 message
  之后就只能靠解析。这也是它必须保持静态的原因。
- `op` 是 12 个构造器的**必填位置参数**，不是可选字段。InfluxDB 把 `Op` 做成可选结构体字段，
  实测填充率约 17%，同一个包里有的填有的不填 —— 可选的结构化字段必然腐化。空 `op` 由闸门拦截。
- `cause` 通过 `Unwrap()` 可达，使 `errors.Is(err, context.Canceled)` 一类判断在中间层
  正常工作。
- **禁止对 `apperror.Error` 使用 `errors.Is` 判等。** 分类一律走 `KindOf` 与 `CodeOf`。

### 2.6 FieldCode：客户端错误的主路径

`Code` 回答"哪条业务规则失败了"，`FieldCode` 回答"这个输入哪里不对"。后者在整个 API 里高度
重复，因此它是一个独立的、更小的封闭集合，**不进 catalog**：

```go
FieldRequired  FieldInvalid  FieldTooLong  FieldOutOfRange  FieldTaken  FieldUnsupported
```

这是本项目最常见的一类错误出口的归宿。迁移前有数百处手写句子：

```go
- echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
+ apperror.Required("bot_id")
```

渲染进 RFC 9457 的 `errors[]` 扩展成员，`pointer` 用 RFC 6901 JSON Pointer。前端按
`errors.field.<code>` 本地化并绑到对应输入控件；非浏览器客户端拿到 `pointer` + `code` 也足以
自行组织文案。

**为什么不给它们每人一个 Code**：这类失败对客户端的程序化行为没有区别（都是"高亮这个字段"），
按 2.4 的准入判据不该发码。Mattermost 走了相反的路，`server/i18n/en.json` 有 3495 个 id、
其中 2111 个以 `.app_error` 结尾，膨胀、漏译与废弃 id 无人清理是可观察的代价。Zitadel 的
[#11917](https://github.com/zitadel/zitadel/issues/11917) 则明文规定
"Unique error codes should only be created if a client must take a different programmatic
action"，与本契约 2.4 同源。

### 2.7 已考虑并否决：开发者可见的自由文本 detail

Grafana 的 `PublicMessage` 与 Coder 的 `Response.Detail`（后者直接是 `err.Error()`）都允许
一段不保证稳定的人类可读文本出网，用于排障。本契约**不提供**这样的字段，理由：

1. 它会成为惰性路径。有了自由文本，没人再注册 code、也没人再用 FieldCode，最终退回到本次
   重构要消灭的那 1009 处手写字符串。
2. 它是泄漏面。`WithDetail(err.Error())` 只要出现一次就等于本次重构白做，而这恰恰是最顺手的
   写法。
3. 三档划分已经覆盖了真实需求：5xx 用 Kind 通用文案（用户无法据此行动，具体信息对他无价值）；
   输入类 4xx 用 FieldCode；其余 4xx 是用户可行动的业务失败 —— 那本就该有 code 和本地化文案。

代价是排障更依赖 `request_id` 关联日志。接受这个代价：错误响应里每多一个字段，就多一条泄漏
路径和一处双源文案。

## 3. 分层职责

| 层 | 必须 | 禁止 |
| --- | --- | --- |
| repository / infra 适配器 | 把 `pgx.ErrNoRows`、gRPC status、第三方 SDK 错误翻译为 `apperror` | 定义包级 `ErrNotFound` 一类 sentinel；让 `pgx`、`status` 等类型逃出包外 |
| application / service | 产出带正确 Kind 的错误；中间层只做 `fmt.Errorf("op: %w", err)` | 触碰 HTTP status；记录日志 |
| 领域内核（实体、值对象、领域服务） | 返回领域概念错误 | 依赖 `apperror` |
| transport | 交给全局渲染器 | 手写任何 `xxxHTTPError` 映射函数；log 之后再 return |

`apperror` 是应用层的通用语言。application 与 service 层可直接产出，因为 `Kind` 不含
HTTP 概念，使用它不构成对传输层的依赖。只有纯领域内核禁止依赖。

日志**只在 transport 边界记录一次**，携带 `op` 链、`cause`、`request_id`、`code` 或 `kind`。

被禁的是**映射表**，不是判断。`FSHTTPError`、`runtimeHTTPError` 这类多分支 helper 之所以有害，
是因为同一份 sentinel-到-status 的知识在 18 个地方各写一遍并且互相不一致（`bridge.ErrUnavailable`
在一个分支是 503、掉进 default 变 500 并泄漏）。而单个端点对单个 Kind 做一次显式、可 grep 的
决定是允许的 —— 例如兼容层遇到 `KindNotFound` 时返回 200 加空列表。那不是错误映射，那是业务
决定，写在该端点里即可，不要为它建表。

这条边界不宣称"传输层零代码"：Harbor 保留了一张 `codeMap`，Gitea 刻意保留了让 handler 显式传
status 的入口。宣称零映射然后被现实逼回去，结果会比现在 18 个函数更散。

## 4. 传输渲染

四个渲染器，全部由 `internal/apperror` 提供，各传输不得自行构造错误体。

### 4.1 HTTP

RFC 9457，`Content-Type: application/problem+json`。

```json
{
  "type": "urn:memoh:error:bot.name_taken",
  "title": "Conflict",
  "status": 409,
  "detail": "This name is already taken.",
  "kind": "conflict",
  "code": "bot.name_taken",
  "args": { "field": "name" },
  "errors": [{ "pointer": "/name", "code": "taken" }],
  "request_id": "01HZX..."
}
```

无 code 时 `type` 为 `about:blank`，`code` 与 `args` 省略，`title` 与 `detail` 取 Kind
通用文案。`kind` 始终存在：迁移后绝大多数错误只有 Kind 没有 code，它是这些错误唯一可供
客户端本地化的稳定身份，读一个字段也好过解析 `type` URI 或在每个客户端维护一张状态码文案表。
前端为此需要 12 个 `errors.kind.*` 本地化键。

`errors[]` 是 RFC 9457 扩展成员，承载字段级校验明细，`pointer` 用 RFC 6901 JSON Pointer。

#### 4.1.1 Kind 不建模的四个状态码

`405`、`413`、`415`、`426` 拒绝的是请求的**信封** —— 方法、体积、媒体类型、协议 —— 而不是
请求所要求的东西。gRPC 没有对应的 code，这正是 Kind 不建模它们的原因：它们是 HTTP 组帧层的
概念，就留在 HTTP 自己的词汇里。

这四个状态码允许用裸的 `echo.NewHTTPError(status)` 抛出，**不带消息**。全局渲染器仍把它们
渲染成 Problem，并原样透传状态码 —— 若按 Kind 推导，`KindFromHTTPStatus(413)` 会得到
`KindInvalid`，413 会被静默改写成 400，等于让客户端去重试一个永远不可能成功的请求。文案则
一律丢弃：状态码本身已经把这类拒绝要说的话说完了。

这是对 Kind 的豁免，不是对线格式的豁免。

#### 4.1.2 upstream：代理场景的原文透传

调 model provider 时本服务只是代理。provider 说的 "you exceeded your current quota"，
能据以行动的只有拥有那个 provider 账号的用户；把它换成我们的通用文案，等于契约亲手删掉了
响应里唯一有用的东西。

```json
{ "kind": "internal", "detail": "...", "upstream": { "provider": "openai", "message": "You exceeded your current quota." } }
```

`upstream` 是独立扩展成员，**不并入 `detail`**：`detail` 是我们的、闭集的、已本地化的；
`message` 是外来的、开放的、语言由 provider 决定。合并之后客户端就再也分不清哪一半可以翻译。
客户端应把 `message` 作为**引述**呈现并标注来源，前端为此需要 `errors.upstream.*` 两个键。

Kind 仍然负责分类（决定状态码与重试语义），引述只补充"对方说了什么"，绝不覆盖"我们判定了什么"。

**`message` 必须已由持有凭证的那一层擦除密钥** —— apperror 不知道什么是密钥。

### 4.2 SSE 与 WebSocket

流式错误用**扁平**的 `apperror.Public` 投影，不嵌套 Problem：

```json
{ "type": "error", "kind": "unavailable", "code": "workspace.unreachable", "args": {}, "detail": "...", "request_id": "..." }
```

`upstream`（§4.1.2）在流里同样适用，且这是它最常出现的地方 —— 模型调用失败绝大多数发生在流中。

Problem 是一元 HTTP 响应的形状，不是流的形状。理由有三：流式生态的事实标准是扁平错误对象
（OpenAI 的 `data: {"error":{...}}`、Ollama 同形），嵌套一层 `problem` 会让任何 OpenAI 兼容
客户端需要额外适配；`type`/`title`/`status` 三个字段在流里没有意义，流已经返回 200 了；本项目
现有的 SSE event 本来就是扁平的，扁平投影不需要前端重写。

各端点仍不得自造字段：`type` 由 envelope 提供，其余全部来自 `apperror.PublicOf`。

### 4.3 gRPC

`google.rpc.Status`，`code` 取 Kind 映射，`details` 携带
`ErrorInfo{reason: <code>, metadata: <args>}`。`channel.ErrorReason` int32 枚举废除，
改用字符串 code —— proto enum 的演进成本远高于字符串，且 `google.rpc.ErrorInfo` 的
`reason` 本身就是字符串。

### 4.4 MCP / JSON-RPC

```json
{ "code": -32603, "message": "<Kind 通用文案>", "data": { "code": "...", "args": {}, "request_id": "..." } }
```

`message` 取 Kind 通用文案，绝不放 cause。业务身份放 `data.code`。

## 5. 不变量与闸门

**没有闸门的规则不写进文档。** 本项目已有两个反例：skill 曾写明"不要让 apperror 渗入
领域层"，而 `domains/agent/application/service_acp.go` 违反 7 次；skill 曾写明 SECRET
泄漏自查，但从未变成测试。在 1009 个调用点的规模上，靠人和 agent 自觉是无效的。

闸门全部住在 `internal/apperror` 里，紧挨着它们守护的东西。这不是随手放的：本次迁移中途，
一次无关的并发重构把整个 `internal/arch` 包连同放在里面的错误契约闸门一起删掉了。**一个能被
别人挪目录顺手带走的闸门，不算闸门。**

| 不变量 | 闸门 |
| --- | --- |
| handler 不自写错误文案 | `adoption_test.go`：`echo.NewHTTPError` 只允许裸状态码形式，且只限 §4.1.1 四个信封状态码 |
| `err.Error()` 不进入响应构造 | `forbidigo` |
| 响应体不含 cause | `server_test.go`：cause 注入 `errors.New("SECRET")`，遍历各类错误形状断言不含该串 |
| 信封状态码不被 Kind 改写 | `server_test.go`：405/413/415/426 各自断言状态码原样透传 |
| 领域内核不依赖 apperror | review（无守卫测试；见 AGENTS.md 分层契约）|
| `op` 非空 | `adoption_test.go`：AST 检查，构造器 op 参数不得是空字符串字面量 |
| coded 错误的 Kind 由 catalog 决定 | `error_test.go`：`WithCode` 断言采纳 catalog 的 Kind，杜绝 500 配 409 code |
| catalog 与三份 locale 同步 | `locale_test.go`：遍历 catalog/Kind/FieldCode，断言 en/zh/ja 三份齐全且无多余键 |
| Kind 映射表无缺口 | `kind_test.go`：12 档 Kind 对四张表全覆盖，名称有 golden 测试锁定 |

## 6. 废止清单

| 废止对象 | 位置 | 去向 | 状态 |
| --- | --- | --- | --- |
| `httpx.ErrorResponse` | `domains/api/http/httpx/error.go` | `apperror.Problem` | 已删除 |
| `httpx.NewI18nHTTPError` | 同上 | `apperror` 构造器 | 已删除 |
| 11 个 `error_alias.go` | 各 HTTP 子包 | swagger 统一注解 `apperror.Problem` | 已删除 |
| `wsErrorMessage` | `domains/api/http/chat/local_channel.go` | `apperror.PublicOf` | 已删除 |
| echo 默认错误处理器兜底 | `domains/api/http/server/server.go` | 单一渲染器 | 已删除 |

以下尚未完成，由串行 pass 处理：

| 废止对象 | 位置 | 去向 |
| --- | --- | --- |
| `feedback.Error` 与 20 个 `acp_*` code | `domains/agent/decision/feedback/` | catalog 的 `acp.*` |
| `slash.Error` | `domains/agent/command/slash/errors.go` | `apperror` |
| `CommandActionError` | `domains/api/http/chat/local_channel.go` | `apperror` |
| `CreateContainerErrorEvent`、`displayPrepareStreamEvent` 的错误字段 | `domains/api/http/runtime/` | 内嵌 Problem |
| `channel.ErrorReason` int32 枚举 | `domains/channel/errors.go` | 字符串 code |
| 18 个手写映射函数 | 见迁移批次记录 | 删除 |
| 15 个以上重复的包级 `ErrNotFound` | 各领域包 | repository 边界翻译 |

## 7. 迁移

Kind 化已完成：1040 处遗留构造，19 个目录，按目录分片并行执行，现存零处。守护它的不再是
递减预算表，而是 §5 那条永久规则 —— 预算棘轮是迁移期的脚手架，迁移结束就该拆掉，留着只会腐坏。

迁移期间的两个观察值得留档：

- **信封状态码**是并行批次唯一没法机械处理的一类，因为 Kind 表里没有它们。答案不是硬塞进
  Kind，而是承认它们不属于 Kind，见 §4.1.1。
- **502 收敛为 503** 在 handler 侧发生过一次；渲染器侧后来改为原样透传状态码，因此这条
  收敛只对显式写了 `KindUnavailable` 的调用点成立，不再是渲染器行为。

### 7.1 待办：串行 code 铸造

由单一负责人按 2.4 的准入判据裁决，统一修改 catalog 与三份 locale。此工作没有覆盖率
指标 —— 设定覆盖率会直接催生无信息量的 code。

并行批次记录下来的候选（各自只有 Kind、暂无 code）：OAuth 未配置、provider 不支持、
memory rebuild/ingest 能力冲突、ACP feedback 的 `no_workspace_exec` 一族、model_id
冲突与歧义。

### 7.2 待办：把 catalog 发布给非浏览器客户端

`detail` 始终是一句消过毒的英文，所以 CLI、webhook 消费者和第三方集成不会只拿到一个裸 code ——
这一档的体验与 Coder 的 `Response.Message` 相当。但**本地化**目前只有 Web 有：三份 locale
住在 `apps/web`。官方 CLI 与桌面端要跟上，就得把 `errors.*` 作为构建产物发布，而不是让每个
客户端各抄一份。这是"文案归客户端"这个选择的真实代价，需要兑现。

### 7.3 待办：provider 错误的密钥擦除

`ChatModel.SanitizeError` 目前**没有任何非测试调用者** —— chat 路径上的 provider 错误
未经密钥擦除，就以 `err.Error()` 字符串一路传到 SSE 与 IM 通道。§4.1.2 的 `upstream`
透传把同一段文本也带到了 WebSocket，因此这个缺口现在覆盖全部出口，必须在执行层补上。
