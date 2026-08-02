# Telegram 聊天上下文审查

日期：2026-08-02

## 范围

这份审查以 Telegram Channel 和原生 Model Runtime 为主，沿着以下调用链检查每轮请求送进模型的内容：

```text
Telegram 入站消息
  -> Channel 入站处理与会话路由
  -> 时间线、历史、记忆、附件和系统提示组装
  -> Twilight AI 主聊天请求
  -> 流式结果发送到 Telegram
  -> 持久化当前回合
  -> 可选的记忆提取、标题生成和上下文压缩
```

审查只覆盖代码和默认配置，没有读取运行中 PostgreSQL 里的实际 Bot 设置。因此，是否启用了 Memory、Compaction、Title Model 等功能，需要结合部署配置确认。

## 结论

如果 Memoh 只用作 Telegram 文字聊天机器人，当前默认形态偏重。最大的固定开销不是 Telegram Adapter，而是每轮都向模型暴露完整 Agent 能力：工具 Schema、工具使用说明、工作区提示、技能摘要和消息元数据。长期使用同一个 Session 时，历史上下文随后成为主要开销。

优先级最高的几项操作是：

1. 纯文字机器人使用不带 `tool-call` 兼容性的模型配置。
2. 开启上下文压缩，并明确选择压缩模型。
3. 不需要跨会话记忆时关闭 Memory Provider；需要时缩小每轮注入量。
4. 精简 Bot 工作区中的 `AGENTS.md`、`PROFILES.md` 和无用技能。
5. 保留工具时降低工具输出上限，并尽量缩短 Session 生命周期。

## 主要发现

### 1. 工具定义是最大的固定开销

`cmd/internal/core/providers.go` 中的 `provideToolProviders` 全局注册 19 个 Tool Provider。普通主会话在开启工具调用后，通常会向模型暴露约 34 个工具；新建 Bot 默认选中 Built-in Memory 后约 35 个。这个数字还不包括 Browser、TTS、图片、视频、技能和外部 MCP 等可选能力。

其中有些工具即使不可用也会出现在请求里：

- `internal/agent/tool/web.go` 只检查服务是否存在，不检查当前 Bot 是否真的配置了 Search Provider。
- `internal/agent/tool/email.go` 无条件返回 4 个邮件工具，邮箱绑定要到执行阶段才检查。
- Schedule、Background、Container、History 和 Subagent 工具也会随全局依赖一起注册。

`internal/agent/runtime/native/agent.go` 的 `Agent.Stream` 只在模型声明 `tool-call` 兼容性时组装这些工具。因此，纯文字 Telegram Bot 使用不带 `tool-call` 的模型配置，可以一次去掉整套工具 Schema 和工具说明。代价是搜索、文件、定时任务、附件发送等工具能力都会失效。

更合适的长期方案是增加按 Bot 或运行配置的工具白名单，并让每个 Provider 在返回 Schema 前检查真实配置和权限。Telegram 文字 Bot 通常只需要零到三个工具，不需要完整工作区 Agent 的工具面。

### 2. 自动压缩默认关闭，配置还有一处行为不一致

数据库默认值位于 `db/postgres/migrations/0001_init.up.sql`：

- `compaction_enabled = false`
- `compaction_threshold = 100000`
- `compaction_ratio = 80`
- `compaction_model_id = NULL`

前端文案写着压缩模型留空时使用聊天模型，但 `internal/agent/application/service_compaction.go` 的自动压缩路径在 `CompactionModelID` 为空时直接返回。手动 `/compact` 才会回退到聊天模型。实际配置时必须明确选择压缩模型，否则打开自动压缩也可能没有效果。

滚动压缩会把当前滚动摘要和全部未压缩历史合并为新摘要。`compaction_ratio` 控制摘要允许的最大输出长度，不是只压缩多少条历史。默认 80% 对普通聊天过于宽松。

建议起始值：

| 聊天模型窗口 | 压缩阈值 | 摘要比例 |
|---|---:|---:|
| 32K | 10K–16K | 15%–25% |
| 64K–128K | 24K–40K | 15%–25% |

压缩模型的上下文窗口要能容纳待压缩历史。可以用 Telegram 的 `/context` 查看最近一次输入量，用 `/compact` 手动压缩，用 `/new` 在话题完全切换时新建 Session。这些 Slash Command 在进入主聊天模型前处理。

自动压缩目前主要在回答持久化之后异步触发。因此，第一次越过阈值的请求仍会携带较大的原始历史，压缩结果从后续请求开始生效。

### 3. 历史裁剪没有给其他上下文预留空间

`internal/agent/application/service.go` 使用模型完整 Context Window 作为历史预算，再追加工作区提示、Memory 上下文和请求技能。系统提示与工具 Schema 随后才在 Native Runtime 中组装。历史裁剪没有预留以下内容：

- 系统提示和 Bot 身份
- 工具 Schema 与工具使用说明
- Memory 检索结果
- 图片或辅助视觉描述
- 模型输出空间

这会让普通请求靠近模型窗口上限，也可能造成 Provider 侧溢出。历史预算应该从模型窗口中扣除固定提示、动态工具、当前消息、附件、最大输出和安全余量，而不是直接使用完整窗口。

项目使用 `internal/textutil/tokens.go` 中的 `2 bytes/token` 估算历史。这种算法实现简单，对混合语言偏保守，但不能替代模型对应的 Tokenizer。

### 4. 固定系统提示仍有精简空间

每轮都会读取 Bot 工作区的：

- `/data/AGENTS.md`
- `/data/MEMORY.md`
- `/data/PROFILES.md`

默认工作区模板加内置聊天提示约 6.9KB，尚未计算 Bot 身份、平台身份、技能摘要和工具说明。其中 `internal/agent/runtime/native/prompts/_memory.md` 单独约 2.63KB，即使未配置 Memory Provider 或模型不支持工具调用也会注入。

`internal/agent/runtime/native/prompt.go` 还会列出全部可用技能的名称和描述。技能正文只在激活时加载，这一点合理；但安装大量不相关技能仍会增加每轮固定输入。

不改代码时可以：

- 删除 `PROFILES.md` 中没有填写的示例。
- 将 `AGENTS.md` 缩成真正需要的人设和行为规则。
- 移除不使用的技能。
- 将全局 `system_files_max_bytes` 从 32768 降到约 8192。

代码层可以让 `_memory.md`、平台身份和技能目录按当前能力条件注入。

### 5. Memory 会在模型调用前后都参与当前回合

配置 Memory Provider 后，`internal/agent/application/service_memory.go` 会在主聊天请求前检索相关记忆，并作为额外 User Message 注入。Built-in Provider 默认最多放入 6 条、1800 字符。

当前新建 Bot 页面会自动选择 Built-in Memory。如果只需要当前 Session 内的对话连续性，可以清空 Memory Provider。仍需长期记忆时，建议把 `context_target_items` 调到 3–4，把 `context_max_total_chars` 调到 800–1200。

回答持久化后，当前回合还会异步传给 Memory Provider。Built-in Memory 的 Formation 流程通常会：

1. 把当前回合的 User 和 Assistant 文本发给记忆模型做 Extract。
2. 如果提取到事实，再把事实和候选记忆发给记忆模型做 Decide。

它发送的是当前回合，不是整段会话历史；但这仍意味着一次 Telegram 回合可能多出一到两次 LLM 请求。记忆模型优先使用 Compaction Model，否则回退到 Chat Model。

### 6. Telegram 消息包装重复了稳定元数据

`internal/chat/timeline/rendering.go` 会给每条用户消息生成 XML，包括 `id`、`sender`、时间、Channel、会话类型和 Target。私聊中 `channel="telegram"`、`type="private"` 和 Target 通常在整个 Session 内不变，却仍逐条重复。

一个典型的两字私聊消息，连同 XML 包装约 145 字节。按项目自身的估算方法约为 73 个 Token，其中约 70 个来自包装。100 条用户消息可能仅在包装上消耗约 7K 估算 Token。可以把稳定字段提升到 Session 级上下文，每条消息只保留 ID、时间和回复关系。

群聊还有额外开销：没有提及 Bot 的消息也会进入持久时间线，下一次真正触发 Bot 时成为上下文。私聊消息始终触发，不存在这种被动群聊积累。

### 7. 工具结果和多步调用可能迅速放大单轮上下文

默认工具输出上限是 64KB 或 2000 行。历史消息总数超过 10 后，代码才会清除大部分旧工具消息；短会话中的大工具结果仍可能进入下一轮。Twilight AI 当前使用 `WithMaxSteps(-1)`，工具多步调用没有固定步数上限，循环检测又默认关闭。

保留工具时，可以先使用以下全局配置：

```toml
[agent]
tool_output_max_bytes = 16384
tool_output_max_lines = 400
system_files_max_bytes = 8192
```

代码层应在工具回合结束后，把过去的工具调用替换为短结果摘要，并为多步调用设置上限。

### 8. Prompt Cache 省计费，不省逻辑窗口

`internal/models/prompt_cache.go` 目前只为 Anthropic Messages 显式设置 Cache Control。它会缓存系统提示和整套工具定义，适合 Telegram 的重复前缀。默认 TTL 是 5 分钟，也支持 1 小时。

缓存命中的内容依然属于逻辑上下文，只是 Provider 可能按 Cache Read 价格计费。`/context` 会显示 Cache Hit Rate。`show_tool_calls_in_im = false` 也只影响 Telegram 上是否显示工具进度，不会减少发给模型的工具 Schema。

## Telegram 消息是否会在发送前后都完整发给 LLM

主聊天路径不会因为 Telegram 的发送动作，把同一条消息再发一次。

一次没有工具调用的普通文字回合大致是：

1. 当前 Telegram 用户消息进入时间线。
2. Server 组装系统提示、裁剪后的历史、当前消息和可选 Memory 上下文。
3. 这些内容发送给主聊天 LLM。Pipeline 路径中当前用户消息已经在历史里，代码明确避免再次追加，因此主请求内不会重复两份。
4. LLM 的流式 Delta 直接送到 Telegram；Private Chat 使用 Draft，其他场景编辑同一条消息。
5. 最终 User/Assistant 回合写入数据库。
6. 下一次用户发言时，最近的 Assistant 回复会作为历史再次发送给主聊天 LLM，除非已被裁剪、压缩或过滤。

所以要区分两个问题：

- 同一个主聊天请求里，当前用户消息不会因为 Telegram 前后发送而重复两次。
- 跨回合时，上一轮机器人回复通常会完整作为历史重新发送，这是多轮聊天正常维持上下文的方式。

以下功能会产生额外 LLM 请求：

| 条件 | 额外请求 | 发送内容 |
|---|---|---|
| 模型调用了工具 | 每个工具步骤后可能再次调用主模型 | 原上下文加当前工具调用和结果 |
| 新 Session 配置了 Title Model | 通常一次 | 第一条用户消息，最多 500 字符 |
| 配置 Built-in Memory | 通常一到两次 | 当前回合 User/Assistant 文本；随后可能发送事实和候选记忆 |
| 达到自动压缩阈值 | 一次压缩调用 | 滚动摘要和待压缩历史 |
| 图片需要 Auxiliary Vision | 一次或多次视觉调用 | 图片及视觉提示，结果描述再进入主模型 |

Telegram 最终发送的 HTML 格式化文本不会因为发送成功而回传给主模型。持久化的是 Agent 的规范化消息；它在后续回合中作为历史使用。

## 建议执行顺序

如果目标是一个简单、便宜的 Telegram 文字 Bot，可以按这个顺序调整：

1. 建立不带 `tool-call` 的聊天模型配置。
2. 打开 Compaction，明确选择模型，阈值设置为窗口的约 20%–35%，摘要比例从 15%–25% 开始。
3. 关闭不需要的 Memory Provider；需要时缩小注入量。
4. 精简工作区提示和技能。
5. 用 `/context` 观察真实 Input Tokens 和 Cache Hit Rate。
6. 话题完全改变时使用 `/new`，不要无限复用同一个 Session。

如果还需要部分工具，最值得实现的代码改动是按 Bot 配置工具白名单，而不是在完整工具模式和完全无工具模式之间二选一。

## 验证

审查期间运行了以下相关测试：

```text
go test ./internal/agent/application
go test ./internal/agent/context/compaction
go test ./internal/chat/timeline
go test ./internal/channel/inbound
go test ./internal/command
```

最终快照下这些包均通过。审查过程没有修改运行逻辑。
