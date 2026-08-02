# Telegram `send`、Sticker 与流式输出纠偏计划

> 状态：**已按用户审核后的方案实施并部署**
> 日期：2026-08-02
> 本文保留下方的原始纠偏计划作为决策记录；实际落地结果以本节和[实施报告](./2026-08-02-telegram-optimization-implementation.md)为准。

## 最终落地状态

- `send` 仍是 Telegram Discuss 唯一公开回复边界；普通 Assistant Text 和 Reasoning 都是私有输出。
- 模型未调用 `send` 时默认保持静默并记录违约。Web 提供“自动补发兜底”开关，默认关闭；只有用户显式开启后，强制回复轮才允许把最终 Assistant Text 补发出去。
- Sticker 已合并为第一方 `send.sticker_id`。模型只看到一个 `send`，完整 Sticker 目录一次性、确定性地暴露；旧的搜索和独立发送工具不再暴露给模型。
- “通常每条公开信息至少配一张合适 Sticker”是推荐下限，不是上限。`send(text="", sticker_id=...)` 支持纯 Sticker；需要多张时可以追加空文本 `send`，但不得机械重复。
- 成功发送到当前会话后立即终止本轮，不再发起“工具成功后的收尾” LLM 请求；最小 Tool Result 仍与 Tool Call 一起写入 Canonical History。
- 私聊流式使用 Telegram 官方 `sendMessageDraft`，结束后用 `sendMessage` 固化；群组和频道不再使用先发消息再编辑的伪流式，只在完整结果就绪后发送一次。
- Sticker Set 在 Web 中是列表。多个 Set 通过服务端内部 `X-Telegram-Sticker-Set` Header 选择，并在模型侧合并成一个稳定完整目录；浏览器和模型都看不到 Bot Token 或 Header。
- Sticker Set 元数据与视觉描述均使用 SQLite 永久缓存。只有显式刷新 Set、切换识别模型或手动重识别才改变相应缓存。
- Sticker 识别模型可在 Web 单独配置，默认继承 Bot 辅助视觉模型。继承路径会保留 Provider，避免相同 `model_id` 跨 Provider 时无法解析。
- 2026-08-02 最终上线后，两个 Bot 的 46 + 120 张 Sticker 已强制重新识别，166/166 为 `ready`；Sticker 服务重启后仍为 166/166。

## 1. 结论与纠错

上一轮实现发生了目标漂移，主要有四处：

1. 把“减少 `send` 后的额外 LLM 调用”错误地改成了“Telegram 普通文字不使用 `send`”。
2. 把普通 Assistant `TextDelta` 直接当成公开回复，削弱了 `send` 原本承担的“明确允许外发”边界。
3. 把完整且稳定的 Sticker 目录移出工具 Schema，改成“先搜索、再发送”，反而增加了工具轮次和 LLM 重读上下文的次数。
4. Sticker 视觉识别模型仍由 Sticker 服务的环境变量配置，Web 只能看目录和重试，不能选择识别模型。

这些改动不符合用户的原始目标。修复应围绕“显式外发边界、一次发送调用、成功发送即终止本轮、稳定前缀优先缓存”重新设计。

## 2. 当前代码的真实状态

### 2.1 `send` 提示与配置

当前并不是把所有 `send` 说明都删掉了：`MessageProvider.Usage` 中仍保留 Discuss 模式必须调用 `send` 才能说话的说明。但是 Telegram Native Discuss 会选择另一段 System Contract，内容明确表示普通 Assistant 文本会直接展示，并要求不要再通过 Messaging capability 重发。

同时，当前线上 Telegram 工具白名单关闭了原生 `send`。因此对实际 Telegram 会话而言，效果确实等同于“告诉模型不用 `send`，并且不给它 `send`”。这是错误状态。

涉及位置：

- `internal/agent/runtime/native/prompts/mode_discuss.md`
- `internal/agent/runtime/native/prompt.go`
- `internal/agent/application/service.go`
- `internal/agent/tool/message.go`
- `internal/agent/channelpolicy/`

### 2.2 旧的回复兜底没有删除

原有 `sendReplyFallback` 仍然存在。它会在 Telegram 强制回复、模型没有成功调用发送工具时，从 Assistant 输出里找一段文字直接发送。

上一轮又新增了 `discussVisibleReplyStream`，把普通 `TextDelta` 直接送到 Telegram Outbound Stream，并在成功时抑制旧兜底。因此当前存在两条隐式外发路径：

1. 普通 Assistant `TextDelta` 的直接流式外发；
2. 强制回复时从最终 Assistant 文本提取内容的旧兜底。

即使 Twilight AI 能区分 `ReasoningDelta` 和 `TextDelta`，普通 Assistant Text 仍不等于模型显式授权发送的内容。只要目标是用 `send` 区分“内部输出”和“公开消息”，这两条路径都不应继续发送任意 Assistant 文本。

涉及位置：

- `internal/channel/discuss/visible_reply_stream.go`
- `internal/channel/discuss/runner.go`
- `internal/channel/discuss/forced_reply.go`
- `internal/agent/application/turn_discuss.go`
- `internal/agent/application/service_messages.go`

### 2.3 当前 Sticker 路径确实更贵

模型当前看到两个 Sticker 工具：

- `search_telegram_stickers`
- `send_telegram_sticker`

一次带 Sticker 的回复通常会形成：

1. 第一次 LLM 请求决定搜索；
2. 搜索工具结果回传后，第二次 LLM 请求选择 Sticker 并调用发送；
3. 发送结果回传后，当前无限 Tool Loop 还可能产生第三次 LLM 请求用于收尾。

上一轮虽然缩小了每次请求中的 Tool Schema，却忽略了后续每个模型步骤都会重新读取长历史。对于已经观测到约 99.7% 稳定前缀缓存命中的部署，永久、稳定地暴露约 18.3 KB Sticker 目录，通常比为每次 Sticker 选择增加一次甚至两次模型请求更符合本项目的成本目标。

### 2.4 Sticker 识别模型不能在 Web 配置

Sticker 服务当前只读取以下进程环境变量：

- `TELEGRAM_STICKER_MCP_VISION_API_KEY`
- `TELEGRAM_STICKER_MCP_VISION_MODEL`
- `TELEGRAM_STICKER_MCP_VISION_BASE_URL`

Web Sticker 页面只有目录、预览、刷新和失败重试，没有模型选择。现状与要求不符。

### 2.5 Telegram 官方流式 API 与当前实现

Telegram 官方提供 [`sendMessageDraft`](https://core.telegram.org/bots/api#sendmessagedraft)。官方当前约束是：

- 目标必须是私聊；
- Draft 是 30 秒的临时预览；
- 生成完成后必须调用 `sendMessage` 发送永久消息；
- 相同非零 `draft_id` 的更新会显示为连续动画。

项目的 Telegram Adapter 在本轮修改前就已经实现了这条官方 API：私聊使用 `sendMessageDraft`，结束时使用 `sendMessage`。最终决策是不在群组和频道模拟增量更新，而是等待完整内容后一次发送。

上一轮新手搓的部分不是 Telegram API 本身，而是 `TextDelta → discussVisibleReplyStream → TelegramAdapter.OpenStream` 这条上层桥。它绕过了 `send`，应删除。

## 3. 修复后的不可破坏约束

实施时以下内容作为硬性验收条件：

1. Discuss 模式只有成功执行、目标为当前会话的 `send` 才表示内容获准公开。
2. `ReasoningDelta`、普通 Assistant Text、工具参数之外的任何模型输出都不得直接发送到 Telegram。
3. `send` 一次调用可以发送纯文字、纯 Sticker、或文字加一张 Sticker；文字允许为空，但 `text` 与 `sticker_id` 至少有一个。
4. 模型选择 Sticker 不需要先调用搜索工具。
5. 成功发送到当前会话后，本轮 Agent 立即结束，不再为“确认工具结果”请求一次 LLM。
6. Tool Call 与最小 Tool Result 仍写入 Canonical History；等下一条用户消息到来时再随历史进入下一次请求。
7. Sticker 目录按 ID 确定性排序，并保持在稳定工具前缀中；只有用户手动刷新 Sticker Set、修改识别模型或重新识别成功时才变化。
8. 私聊流式预览使用 Telegram 官方 `sendMessageDraft`；群组和频道不做 `sendMessage + editMessageText` 伪流式，只发送最终结果。

## 4. 拟实施方案

### 阶段 A：恢复 `send` 的公开边界

1. 把 Discuss System Contract 恢复为：普通输出是私有的，只有调用可用的 Messaging capability 才能在会话里发言。
2. 删除 `DiscussPublicText` 分支及其 System Prompt 文案。
3. 删除 `discussVisibleReplyStream` 和所有把普通 `TextDelta` 标记为公开消息的逻辑。
4. 删除 `memoh.visible_in_conversation` 这类为直出 Assistant Text 新增的历史标记。
5. 线上 Telegram 白名单重新启用原生 `send`。
6. 保留 Tool Call 总开关和逐工具开关；如果用户关闭 `send`，Web 明确提示“Discuss 模式将无法公开回复”，但不擅自替用户开启。

### 阶段 B：删除会泄露内部文本的旧兜底

1. 删除 `sendReplyFallback` 中“从任意 Assistant Text 提取内容并发送”的行为。
2. 兜底只允许围绕一个已经形成的显式 `send` 调用工作：
   - 官方 Draft 或群组编辑预览失败时，`send` Executor 仍尝试一次最终发送；
   - 最终发送失败则返回失败 Tool Result，让模型决定是否重试；
   - 模型从未调用 `send` 时保持静默并记录 Contract Violation，不发送其普通文本。
3. `force_reply` 的提示改为明确要求模型最终调用 `send`，而不是要求生成一段会被服务自动外发的普通文本。

### 阶段 C：把 Sticker 真正并入原生 `send`

Telegram 当前会话中的 `send` 增加：

```json
{
  "text": "可选文字",
  "sticker_id": "可选 Sticker ID"
}
```

规则：

- `text` 和 `sticker_id` 至少提供一个；
- 同时提供时，在同一次工具执行中先发送文字，再发送 Sticker；
- 当前会话的 `chat_id` 由服务端 Session Context 注入，不再要求模型复制元数据；
- 跨平台或非 Telegram 目标不展示 `sticker_id` 字段；
- 文字、附件、结构化消息等现有 `send` 能力保持不变。

实现上把 Sticker MCP 从“模型直接调用的两个工具”降为 `send` 背后的第一方 Sticker Service：

1. Tool Assembly 从 Sticker Service 读取永久缓存的 Ready 目录；
2. 以固定 ID 顺序把完整“ID → 视觉描述”放入 `send.sticker_id` 的 Schema/Description；
3. 不再向模型暴露 `search_telegram_stickers` 和 `send_telegram_sticker`；
4. `send` Executor 通过内部 Sticker Service Port 解析 ID 并发送，模型只发生一次 Tool Call；
5. Sticker 服务暂时继续负责 Telegram File ID、媒体缓存和目录管理，避免在模型上下文中暴露 File ID 或 Bot Token。

### 阶段 D：成功 `send` 后终止 Tool Loop

当前 Native Agent 使用 Twilight AI 的无限 Tool Loop。工具成功后 SDK 默认继续下一次模型调用，所以只缩小 Tool Result 不能解决额外 API 费用。

计划在 Twilight AI 的通用工具编排层增加“执行工具后是否终止”的 Provider 无关能力，例如：

```go
sdk.WithStopAfterToolResults(func(step *sdk.StepResult) bool)
```

Memoh 的判定规则：

- 当前会话 `send` 成功：终止本轮；
- 文字加 Sticker 全部成功：终止本轮；
- `send` 失败或只完成一部分：不终止，保留错误结果供模型重试或说明；
- 发往其他会话是否终止，继续沿用当前工作流语义，不与当前会话混为一谈；
- 同一步包含多个并行工具时，所有工具完成并持久化结果后再判断终止，不能中断其他正在执行的工具。

终止时仍把 Assistant Tool Call 和 Tool Result 写入本轮消息，只是不立刻拿它们发起下一次 LLM 请求。下一条用户消息到来后，历史顺序为：

```text
assistant: send tool call
tool:      {"ok": true}
user:      next message
```

这符合工具消息配对要求，同时避免发送完成后立刻让模型重读整个上下文。

### 阶段 E：在显式 `send` 参数上实现流式预览

Twilight AI Provider 已产生 `ToolInputStartPart`、`ToolInputDeltaPart` 和 `ToolInputEndPart`，但 Memoh Native Agent 当前只转发 Start 和完整 Tool Call，没有转发参数 Delta。

计划：

1. 在内部事件模型中加入 Tool Input Delta/End，并保持 Tool Call ID。
2. 只对工具名为原生 `send`、目标为当前 Telegram 会话、字段为 `text` 的参数增量建立预览。
3. 使用增量 JSON String Decoder 解码 `text`；不读取普通 `TextDelta`，也不读取 `ReasoningDelta`。
4. 私聊：
   - 首个可见字符到达后调用官方 `sendMessageDraft`；
   - 使用由 Tool Call ID 稳定派生的非零 `draft_id`；
   - 按现有节流策略更新 Draft；
   - Tool 参数完整并执行成功后，由 `send` 调用永久 `sendMessage`，随后发送可选 Sticker。
5. 群组/频道：Telegram 官方 Draft 不支持，因此不建立预览消息、不调用 `editMessageText`，只在 Tool 参数完整并执行时发送一次最终内容。
6. 纯 Sticker 的 `send` 不开启文字 Draft。
7. 参数最终无效、工具被拒绝或本轮取消时：私聊 Draft 自然过期；群组兼容预览应删除或替换为明确失败状态，不能留下半句永久消息。

### 阶段 F：在 Web 配置 Sticker 识别模型

在 Telegram Sticker 管理对话框新增“Sticker 识别模型”配置：

- 默认：继承 Bot 的辅助视觉模型；
- 可选：从已启用且声明 `vision` compatibility 的 Chat Model 中单独选择；
- 显示当前实际模型、Prompt Version、已识别/失败/待识别数量；
- 修改模型后不自动花费大量 API 重新识别，用户确认后再对缺失项或全部目录执行任务；
- 单项失败重试始终使用当前选择的模型。

后端不把 Provider API Key 下发给 Web 或写入 Telegram Metadata。识别任务复用 Memoh 已有的 Model Resolver 和 Provider Credential 管理；Sticker Service 只提供待识别媒体和持久化描述结果。

建议新增独立的 `telegram_sticker_vision_model_id`，默认继承现有 `auxiliary_vision_model_id`，避免用户为了 Sticker 识别而改变普通聊天图片的辅助视觉模型。

描述缓存键继续包含：

- Sticker File Unique ID；
- 实际识别模型 ID；
- Prompt Version。

切换模型不会覆盖旧模型缓存，回切时可以复用。

### 阶段 G：历史投影与缓存稳定性

保留上一轮中合理的部分，但按新边界收紧：

1. Canonical History 永远保留真实 Tool Call/Result，不做破坏性改写。
2. 下一次发给模型时，成功的当前会话 `send` 可投影为简短的已公开 Assistant 消息，避免长期重复读取完整发送 Schema 和成功回执。
3. 失败或部分成功的发送闭环完整保留，防止模型误判已经送达。
4. 删除 Sticker Search 历史，因为不再存在 Sticker Search 工具。
5. 删除所有“普通 Assistant Text 已公开”的 Service Marker。
6. 完整 Sticker 目录按固定排序放在稳定 Tool Schema/Usage 中；目录未变化时不进行逐消息动态拼接或重排。

## 5. 预期调用数与费用变化

以“不使用其他工具的一次回复”为例：

| 场景 | 当前错误实现 | 修复目标 |
|---|---:|---:|
| 纯文字 | 1 次 LLM，普通 Text 直出，但边界不安全 | 1 次 LLM → `send` → 成功即结束 |
| 文字 + Sticker | 搜索、发送、收尾，通常 2–3 次 LLM | 1 次 LLM → 单次 `send(text, sticker_id)` → 结束 |
| 纯 Sticker | 搜索、发送、收尾，通常 2–3 次 LLM | 1 次 LLM → 单次 `send(sticker_id)` → 结束 |
| 发送失败 | 后续行为不稳定 | Tool Error 回传，允许下一模型步骤重试 |

完整目录会增加稳定输入前缀，但不会增加每次回复的模型步骤；在当前高缓存命中环境下，这是有意选择。验收时不能只看 Context 字节数，要同时比较：

- 每次用户消息触发的 LLM HTTP 请求数；
- Input、Cache Read 和未缓存 Input Token；
- 首次冷调用与后续热调用成本；
- 首 Token/首 Draft 延迟；
- Sticker 回复总延迟与总费用。

## 6. 测试与验收计划

### 自动测试

1. Prompt 测试：Telegram Discuss 明确要求通过 `send` 公开回复，不出现“普通 Text 直接公开”。
2. 非泄露测试：Reasoning 和未调用 `send` 的 Assistant Text 永远不会触发 Telegram Sender。
3. 兜底测试：没有 `send` 时保持静默；只有显式 `send` Payload 才允许最终发送重试。
4. 合并发送测试：纯文字、纯 Sticker、文字加 Sticker、非法空输入、部分失败。
5. Terminal Tool 测试：成功 `send` 只发生一次 Provider `DoStream`；失败时允许第二步。
6. Tool Input Stream 测试：只解析匹配 Tool Call ID 的 `send.text`，忽略 Reasoning、普通 Text 和其他工具参数。
7. 私聊 Adapter 测试：调用 `sendMessageDraft`，最后调用一次永久 `sendMessage`。
8. 群组 Adapter 测试：增量阶段不调用 Telegram 发送或编辑 API，最终只发送一次文字。
9. Catalog 测试：完整目录固定排序；相同目录生成完全相同的 Tool Schema 字节。
10. Web/Settings 测试：只列 Vision 模型、继承逻辑、模型变更确认、失败重试使用所选模型。

### 真实 Telegram 验收

在用户批准并部署后，分别执行：

1. 私聊纯文字：确认 Telegram 客户端出现官方 Draft 动画，最终只留一条永久消息。
2. 私聊文字加 Sticker：确认一个 `send` Tool Call、一次 LLM 请求、文字后跟 Sticker。
3. 群聊文字：确认生成阶段不出现占位消息或编辑闪烁，最终只发送一次。
4. 群聊文字加 Sticker：确认文字不重复、Sticker 只发一次。
5. 思考隔离：构造包含 Reasoning 和普通 Assistant Text 但没有 `send` 的测试响应，群里不得出现任何内容。
6. 记录脱敏 LLM Payload 和请求次数，形成新的对比文档，不覆盖上一轮基线样本。

## 7. 预计修改范围

审核通过后预计涉及：

- `internal/agent/runtime/native/`：恢复 Prompt Contract、转发 Tool Input Delta、终止条件接入；
- Twilight AI SDK：增加成功工具结果后的通用停止谓词；
- `internal/agent/tool/message.go`：扩展 Telegram `send` 的 Sticker 字段和一次执行语义；
- `internal/channel/discuss/`：删除普通 Text 直出和任意 Assistant Text 兜底；
- `internal/channel/adapters/telegram/`：保留官方 Draft，补齐与 `send` Tool Call 的生命周期协调；
- `internal/agent/application/service_messages.go`：恢复显式发送边界后的历史投影；
- `internal/settings/`、数据库迁移、OpenAPI/SDK：Sticker Vision Model 配置；
- `internal/handlers/telegram_stickers.go`：模型配置、识别任务和 Sticker Service 协调；
- `apps/web/src/pages/bots/components/telegram-sticker-catalog.vue`：识别模型选择与确认；
- `cmd/telegram-sticker-mcp/`：去除模型可见的 Search/Send 工具或停止向 Agent 暴露，保留目录、媒体、缓存与管理 API。

## 8. 需要用户审核的两个决策

### 决策 1：群组的兼容流式（已决定）

原计划曾评估群组用 `sendMessage + editMessageText` 做兼容流式。用户明确不接受这种模拟流式；最终实现为群组在 Tool 参数完成后一次性发送，私聊仍使用官方 Draft。

### 决策 2：Sticker 识别模型继承关系

建议增加独立选择项，默认继承 Bot 辅助视觉模型。这样不重复配置时保持简单，需要不同成本/质量模型时也能单独覆盖。

## 9. 审核门禁（历史）

以下门禁在用户批准前生效；用户随后已明确批准并要求实施、部署和提交：

- 不修改上述业务代码；
- 不调整线上 Telegram 工具白名单；
- 不重建或重启服务；
- 不发送新的真实 Telegram 测试消息。
