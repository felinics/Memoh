# Agent 上下文消息整理方案

> 状态：提案，等待确认
> 日期：2026-08-20
> 基线：`origin/main`（`820582bec`）

## 1. 一句话说明

现在同一条消息会在 timeline、application、turn、Context、Native 和 ACP 之间反复转换，导致每一层都在猜“这条消息应该长什么样”。

这次重构把流程理顺：

```text
历史和当前输入
      |
      v
Context 选择本轮需要的 Agent 消息
      |
      +--> Native：发送完整的 sdk.Message 序列
      |
      +--> ACP：把当前 turn 编成自己的 prompt/resource/session 输入
```

`sdk.Message` 继续是 Agent 层的核心消息。重构的重点是整理它的使用边界，不替换它。

## 2. 为什么要重构

### 2.1 现在每个入口都在组装自己的上下文

普通聊天、历史 replay、discuss、retry、subagent 和 ACP 都会自己拼消息。

同一段历史可能经过不同的转换：

```text
DCP pipeline  -> timeline.ContextMessage -> turn.ModelMessage -> sdk.Message
普通 history  -> HistoryRecord -> ContextFrag -> sdk.Message
discuss       -> timeline.ContextMessage -> sdk.Message
ACP           -> PromptInput
```

这些路径分别处理裁剪、tool call、reasoning、当前用户消息和附件，行为容易分叉。

### 2.2 消息语义和 runtime 格式混在了一起

Agent 只需要表达：这是用户消息、包含哪些 parts、tool call 和 tool result 如何关联。

Native 需要把它编码成 Twilight provider 的请求。ACP 需要把当前任务编码成 prompt、resource、image 和 session 输入。这两种编码方式不同，当前却由 application 和 runtime 代码交叉完成。

### 2.3 附件没有和消息 part 绑定

`attachment.Bundle` 在多个入口之间传递。path、URL、base64、MIME 和 fallback 分散在 application、Native、ACP、discuss 和 streaming injection 中处理。

文本和附件混合时，当前结构还缺少稳定的：

```text
第几个 message part -> 哪个附件引用
```

### 2.4 旧历史格式承担了太多工作

数据库 JSON、`turn.ModelMessage` 和 runtime message 经常直接互转。持久化时，`sdk.FilePart` 还会被替换成文本 placeholder，文件本体则留在 media store。

历史格式、turn 兼容结构和 runtime 输入需要分开维护，旧数据才能长期可读。

## 3. 先记住四个现有类型

### `sdk.Message`：Agent 真正使用的消息

来自 Twilight SDK，包含：

- role：user、assistant、system、tool、developer；
- text、reasoning、image、file；
- tool call、tool result 和 provider metadata。

它是 Agent loop、Native 输入和 ACP transcript 使用的核心消息。

### `timeline.ContextMessage`：timeline 的合并结果

位置：`internal/chat/timeline/context.go`

它只有四个字段：`Role`、`Content`、`RawContent`、`CompactionArtifactID`。

它负责把 RenderedContext 和 TurnResponseEntry 按时间合并，并插入 compaction summary。它适合表达“时间线里出现了什么文本”，不承载完整的 Agent parts。

### `turn.ModelMessage`：旧边界和兼容格式

位置：`internal/agent/turn/types.go`

它服务于 turn boundary、gRPC/API、旧 history JSON、retry 和 continuation。除了 `Role`、`Content`、`Usage`，还保留 legacy `ToolCalls`、`ToolCallID`、`Name`。

它继续保留，直到所有旧数据和调用方完成迁移。

### `ContextFrag`：给消息附加 Context 策略

位置：`internal/agent/context/fragment/types.go`

它给 payload 附加：

- 来源和 provenance；
- system/history/current user 的 slot；
- trust、priority、budget、cache；
- compaction coverage；
- 有序 `Parts`。

当前 `Parts` 可以保存文本、`sdk.Message` 或 `sdk.ImagePart`。

## 4. 目标架构

```text
timeline / history reader
        |
        +--> timeline.ContextMessage
        |       (文本、raw content、summary)
        |
        +--> HistoryRecord / turn.ModelMessage
                (来源、assets、usage、coverage、旧字段)
                         |
                         v
                ContextFrag + ContextView
                收集 -> 选择 -> 排序 -> 裁剪
                         |
                         v
              选中的 []sdk.Message
              附件引用和 part 顺序
                    /                 \
                   v                   v
             Native adapter       ACP adapter
             Twilight messages    PromptInput
                                       |
                                       v
                                ACP 长生命周期 session
```

Context 输出 Agent 消息和选择结果。Native 与 ACP 各自负责最后一步编码。

### 4.1 具体例子：用户发来一句话和一张图片

用户发送：`帮我看看这张图`，附件存储在 media store。

Context 处理后保留这些信息：

```text
消息：user
parts：文本“帮我看看这张图”
       附件引用 image-123
顺序：文本在前，附件在后
```

Native 发送前把引用读出来，得到：

```text
sdk.Message{
  Role: user,
  Content: [TextPart, ImagePart],
}
```

ACP 发送前使用同一个附件引用，但生成自己的输入：

```text
Prompt: “帮我看看这张图”
Images: [PromptImage]
AttachmentReferences: []
```

如果 ACP 只能访问 workspace 文件，则会生成：

```text
Prompt: “帮我看看这张图”
Images: []
AttachmentReferences: [workspace path]
```

Context 负责保留消息内容、选择结果和附件顺序；Native/ACP 负责把它变成各自能发送的格式。

## 5. 每一层负责什么

### Timeline

负责 canonical event、RC/TR 排序、文本合并和 compaction summary。

`timeline.ContextMessage` 继续保持窄结构。source ID、时间、附件引用和 rich parts 通过 `RenderedSegment`、`TurnResponseEntry`、`CompactionArtifact` 或 history record 传递。

### Context

负责收集来源、选择内容、预算裁剪、tool closure repair、compaction coverage、稳定排序和 manifest。

Context 使用 `sdk.Message` 表达消息内容，用 `ContextFrag` 表达选择策略。

`Kind` 暂时保留，因为 manifest、selector 和旧数据都在使用它。第一阶段只为现有 kind 增加组合校验，例如 role、slot、part 和 capability 是否匹配。后续再逐步减少 kind 同时表达多个维度的情况。

### Application 和 turn

Application 负责准备 Context 输入、调用 runtime、处理用户输入和保存结果。

`turn.ModelMessage` 继续作为兼容边界。`messageconv` 先通过测试固定当前行为：

| 转换 | 当前结果 |
|---|---|
| SDK -> Model | role、JSON content、usage 进入 Model；application persistence 会先把文件 part 换成 placeholder |
| Model -> SDK | role 和可识别的 string/structured content 恢复；顶层 legacy tool 字段和 usage 当前不会写回 |
| Timeline -> SDK | 有 `RawContent` 时尝试恢复 structured parts，否则按 role 构造文本 message |

### Native

Native 接收选中的 `[]sdk.Message`，负责：

- provider 支持的 role 和 parts 编码；
- tool call/result 和 reasoning continuity；
- 附件引用的最终物化。

迁移期间保留 `RunConfig.Messages`、`Query`、`InlineImages` 和 `InlineAttachments`。新旧路径需要比较 SDK 序列、provider payload、stream event 和 persistence input。

### ACP

ACP 只接收当前 turn 的投影，生成自己的 `PromptInput`：

- `Prompt`：当前任务；
- `ContextMarkdown` + `ContextURI`：当前 turn 的虚拟 resource；
- `Images`：`PromptImage`；
- `AttachmentReferences`：workspace path 或允许访问的 URL；
- session metadata：continuation、approval、ask_user 等运行状态。

ACP session 自己维护历史和 continuation。Native 的完整消息序列不直接作为 ACP 的历史发送。

## 6. 附件怎么走

Context 保存附件引用，优先复用现有：

- `attachment.Bundle`；
- `timeline.ImageAttachmentRef`；
- `historyfrag.MediaRef`；
- `ContextRef`。

需要稳定关联时，为引用增加最小 `ref ID`。绑定关系保留 part 顺序：

```text
text -> attachment A -> attachment B
```

真正发送前，resolver 读取 media store、workspace path 或允许访问的 URL：

- Native 生成 `sdk.ImagePart`、`sdk.FilePart` 或当前文本 fallback；
- ACP 生成 `PromptImage`、resource/path reference 或 prompt 文本。

解析失败保留原始引用，并返回可分类的错误结果。bytes、base64 和 data URL 在 runtime 需要时生成。

## 7. 兼容要求

- 旧数据库 JSON、bare content、raw fallback、assets、usage 和 compaction coverage 继续可读。
- history replay、retry、continuation 和 subagent fork 保持 role、part 顺序、tool association 和 reasoning continuity。
- Native 的 unsupported MIME 路由、文件 placeholder 和 provider capability 行为保持现状。
- ACP 的 resume、continuation、approval、ask_user、transcript persistence 和 cleanup 保持现状。
- 第一阶段不改 context budget 算法、HEIC/libheif、全面格式转换、统一错误体系和数据库 envelope。它们分别安排后续 PR。

## 8. 迁移顺序

这次重构采用 3 个 PR。每个 PR 都有清楚的行为边界，合并顺序就是依赖顺序。

### 8.1 Stack 结构

这些 PR 使用一条依赖顺序固定的 stack。每一层只展示相对下层新增的改动：

```text
main
└── refactor/runtime-neutral-context   PR 1：Context 投影和兼容契约
    └── refactor/runtime-adapters     PR 2：Native / ACP / 附件发送边界
        └── refactor/context-persistence PR 3：持久化解耦和清理
```

分支按依赖顺序创建。下层 PR 合并后，上层 PR 的 base 自动前移；审查时始终从 PR 1 向 PR 3 阅读。

```bash
git config remote.pushDefault origin
gh stack init --base main refactor/runtime-neutral-context
gh stack add refactor/runtime-adapters
gh stack add refactor/context-persistence
gh stack submit --auto
gh stack view --json
```

实现过程中遵循三条规则：

1. 每个分支只提交属于本层的文件和测试；公共类型或接口先落在较低层。
2. 需要修改较低层时，先切回对应分支，提交后运行 `gh stack rebase --upstack`，再继续上层工作。
3. 每层都能单独运行相关测试并回滚。PR 2 的 Native、ACP 和附件 resolver 分别使用独立 commit，便于分别定位和撤回。

### PR 1：Context 投影和兼容契约

明确 `timeline.ContextMessage`、`turn.ModelMessage`、`ContextFrag` 和 `sdk.Message` 的边界，统一 DCP pipeline、history reader 和 discuss collector 进入 Context 的入口。

补充 `messageconv`、tool closure、reasoning continuity、附件和旧 history JSON 的 characterization tests。Context 继续输出现有 Native 所需的 SDK 消息和 manifest，不改变运行时行为。

编译与测试：

```text
go test ./internal/messageconv/...
go test ./internal/agent/context/fragment/...
go test ./internal/chat/timeline/...
go test ./internal/contextview/...
```

### PR 2：接入 runtime adapters 和附件 resolver

这个 PR 处理 Context 到 runtime 的最后一段，内部拆成三个 commit：

1. Native：选中的 `[]sdk.Message` 和附件绑定交给 Native adapter，保留旧 RunConfig 入口。
2. ACP：当前 turn projection 编成 `PromptInput`，沿用现有 prompt/resource/session 机制。
3. 附件：统一引用解析和物化，按普通发送、history replay、retry/continuation、subagent、discuss、read_media、streaming injection 接入。

Native、ACP 和附件分别验证 SDK 序列、provider payload、ACP session、resume、continuation、approval、ask_user、transcript、cleanup，以及 `ref -> part -> runtime representation` fixture。每个 commit 都能回滚到 PR 1 的边界。

### PR 3：持久化解耦并清理重复入口

把历史 JSON 的读写兼容集中到 `internal/agent/context/history`。codec 继续读旧
envelope、bare content、raw fallback 和 legacy tool 字段，写入时保持当前
`bot_history_messages.content` 的字段和结构。数据库字段与 runtime input 仍然分开维护。

新 envelope 和版本号等数据库格式升级放到后续迁移，先用 codec 把边界固定下来。

完成旧 writer/reader 的兼容测试后，再删除已经由 adapter 取代的重复转换和附件组装。

最终运行：

```text
go test ./...
go test -race ./internal/agent/runtime/acp/...
mise run lint
```

## 9. 主要文件

Context 与历史：

- `internal/chat/timeline/context.go`
- `internal/chat/timeline/rendering.go`
- `internal/chat/timeline/turn_response.go`
- `internal/agent/context/fragment/types.go`
- `internal/agent/context/fragment/compile.go`
- `internal/agent/context/fragment/render.go`
- `internal/agent/context/fragment/repair.go`
- `internal/agent/context/history/db_message.go`
- `internal/agent/context/history/frag.go`
- `internal/agent/context/history/types.go`
- `internal/contextview/collector_history.go`
- `internal/contextview/collector_discuss.go`
- `internal/contextview/render_sdk.go`

Native、ACP 与转换：

- `internal/agent/runtime/native/types.go`
- `internal/agent/runtime/native/context_frag.go`
- `internal/agent/runtime/native/agent.go`
- `internal/messageconv/messageconv.go`
- `internal/agent/application/service.go`
- `internal/agent/application/service_attachments.go`
- `internal/agent/application/service_messages.go`
- `internal/agent/application/turn_discuss.go`
- `internal/agent/application/acp_context.go`
- `internal/agent/application/service_acp.go`
- `internal/agent/runtime/acp/session_pool.go`
- `internal/agent/runtime/acp/client/session.go`
- `internal/agent/runtime/acp/client/transcript.go`

附件与持久化：

- `internal/attachment/bundle.go`
- `internal/attachment/normalize.go`
- `internal/chat/message/`

## 10. 完成标准

1. Context 能稳定地产生本轮选中的 `[]sdk.Message`，并保留 source、coverage、tool/reasoning continuity 和附件顺序。
2. Native 新旧路径产生等价的 SDK 序列和 provider payload。
3. ACP 继续使用自己的 prompt/resource/session 输入，session 生命周期和 transcript 行为一致。
4. 所有附件入口都能从统一引用绑定到目标 runtime representation。
5. 旧数据库 JSON、history replay、retry、continuation 和 compaction coverage 继续可用。
6. 代码调用链能清楚看出 Agent message、Context、Native、ACP、turn 和 persistence 的职责。
