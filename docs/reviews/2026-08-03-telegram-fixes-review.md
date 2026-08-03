# 2026-08-03 Telegram 修复系列 — 提交审查

审查范围：2026-08-03 当天的 5 个提交（作者 mizorewww），均为 Telegram 相关修复。
审查重点：hack、坏味道、过度设计、潜在 bug。

| 提交 | 主题 | 规模 |
|------|------|------|
| `9a30c3cc` | fix(telegram): localize settings and recognize stickers | +764/-93，14 文件 |
| `d9d13129` | fix(telegram): harden reply targeting and context cleanup | +781/-63，18 文件 |
| `323a363c` | fix(telegram): show typing for selected discuss turns | +105/-11，5 文件 |
| `c5a1b92c` | fix(telegram): keep discuss typing active | +136/-13，6 文件 |
| `cce79a5d` | fix(telegram): reject invalid text before sticker send | +53/-1，2 文件 |

验证：所有受影响包的 Go 测试全部通过（`internal/handlers`、`internal/messaging`、`internal/agent/tool`、`internal/agent/runtime/native`、`internal/channel/inbound`、`internal/channel/discuss`、`internal/agent/application`、`internal/agent/context/compaction`、`cmd/telegram-sticker-mcp`）。

## 总体结论

整体质量较高，没有 hack，没有过度设计。几个亮点：

- **测试覆盖扎实**。每个行为变更都配了针对性测试，且测试断言的是行为契约（如 schema 不随可见 reply ID 变化、投递矩阵），不是实现细节。
- **`323a363c` → `c5a1b92c` 是同一天的自我修正**：先在入站处放了一次性 typing 通知，当天又发现 Telegram typing 只存活约 5 秒、discuss turn 动辄几十秒，于是重构为 driver 在 `Run` 前启动、带 4s ticker 刷新、turn 结束即停的生命周期。最终形态是干净的，没有留下废弃中间态。
- **`d9d13129` 的 reply 可见性校验**方向正确：把"哪些消息 ID 可被引用"建模为执行期校验（`AllowedReplyMessageIDs`）而不是塞进 schema 的动态 enum，保持了 prompt/schema 的缓存稳定性，还专门有测试锁定这一点。失败时返回稳定的 `error_code` + `retryable` 结果而不是 Go error，符合 memoh-error-handling 约定。
- **`9a30c3cc` 的识别队列**生命周期管理正确（fx `OnStop` → `Close` → `WaitGroup`），去重、批量后一次性刷新 tool schema 的设计是合理的。

## 发现的问题

### 需要关注（潜在 bug）

**1. 识别失败状态未持久化时会形成 3 秒级重试循环**（`9a30c3cc`，`internal/handlers/telegram_stickers.go`）

`ListTelegramStickers` 是 GET 请求但每次都会 `enqueueTelegramStickerRecognition`；前端面板在 `pending_count > 0` 时每 3s 轮询一次。正常失败路径（preview 失败、vision 失败等）会通过 `failed()` 把 `status=failed` 写回 sticker 服务，之后该贴纸被排除在 pending 之外，循环自然终止。但有两个漏洞：

- `recognizeTelegramSticker` 中**第一段 `telegramStickerEndpoint` 失败（连接不存在/解析失败）直接 return，不写失败状态**。此时贴纸永远是 pending，只要用户开着贴纸面板，就会以 3 秒为周期无限重试一个注定失败的任务（每次还伴随 preview 下载和一次 LLM vision 调用的前置流程）。
- `storeTelegramStickerRecognitionFailure` 自身失败时只记日志，同样回到上面的无限重试。

建议：endpoint 解析失败也应尝试写 failed 状态（或至少在任务侧记录 attempts 并做退避/上限）；队列层可以加 per-sticker 的最大尝试次数。

**2. `Enqueue` 在 shutdown 中途退出时残留 `queued` 条目**（`9a30c3cc`，`telegram_sticker_recognition_queue.go`）

```go
case <-q.ctx.Done():
    q.complete(task)
    return enqueued
```

ctx 取消时只清理了当前 task，`accepted` 中尚未投递的其余 task 永久留在 `q.queued` map 里。因为此时队列正在关闭、map 也不再被读取，实际影响仅是关闭期的少量内存残留，不构成泄漏bug，但语义不完整；顺手把剩余 accepted 全部从 map 删掉更干净。

**3. `Enqueue` 在队列满时会阻塞 HTTP 请求**（`9a30c3cc`）

`q.jobs` 容量 8192，worker 只有 2 个、单任务超时 10 分钟。极端情况下（大量 bot × 大量贴纸且 vision 服务 hang），`q.jobs <- task` 会在 `ListTelegramStickers` 的请求 goroutine 里长时间阻塞。实际触发概率很低（去重 key 是 bot+sticker，单 bot 贴纸数量有限），但建议要么 `select` + `default` 丢弃并记日志，要么把 Enqueue 挪到请求返回之后。

### 坏味道（建议跟进，不阻塞）

**4. reply 参数解析逻辑两处重复实现**（`d9d13129`）

`internal/channel/discuss/send_preview.go` 的 `sendReplyMessageID` 手工复刻了 `internal/messaging/executor.go` 对 `reply_to` / `message.reply.message_id` 的解析规则（包括"空字符串视为无 reply"、"字符串 message 是合法文本"等细节）。注释里也承认是 "mirrors"。两份实现各自演化必然漂移——executor 哪天接受新的 reply 形式，preview 就会开出错误引用关系的流式预览。建议把解析下沉到 `internal/messaging`（导出 `ReplyMessageIDFromArgs`），preview 直接复用。

**5. sticker 目录描述用中文字符串模板做解析**（`d9d13129`，`telegram_sticker.go` 的 `compactTelegramStickerEntry`）

```go
const emojiMarker = "（原始 emoji："
...
strings.Contains(prefix, "：待识别")
strings.TrimSuffix(emoji, "，仅供参考）")
```

这是对 `cmd/telegram-sticker-mcp` 生成的自然语言描述按字面中文标点做切割。它本质上是在消费一个没有结构化契约的文本格式，属于受控的脆弱耦合：上游措辞一改（哪怕只是换全角/半角括号），压缩逻辑就静默退化为"保留原文"。好在失败模式是安全的（不解析就返回原行），且有测试锁定当前格式。可以接受，但长期建议让 sticker 服务输出结构化字段（id/description/emoji/status），由运行时负责排版。

**6. GET `ListTelegramStickers` 携带副作用**（`9a30c3cc`）

List 接口除了读数据，还会配置 vision profile（POST `/api/profile`）并入队识别任务。REST 纯度角度是坏味道，且让"用户打开面板"成为识别调度的触发点，语义比较隐晦。务实上可以接受（否则需要额外的调度入口），但值得在 handler 注释里说明这是有意为之的惰性调度。

**7. `AuthorizeTelegramStickerRequestIdentity` 与服务层参数命名不一致**（`9a30c3cc` 顺带暴露的旧问题）

handler 侧已改名为 `channelIdentityID`，但 `application.Service.RecognizeTelegramSticker` 的形参仍叫 `userID`，实际传入的是 channel identity ID。本次提交只是改名对齐了 handler 侧，服务层的 `userID` 命名依旧误导。非本次引入，建议顺手清理。

### 已确认无问题的点

- **`cce79a5d`**（`hasNativeSendContent` 对非 map 的 `message` 值返回 `nonEmptyToolValue`）：修复正确。字符串 message 会被 native send 路径以可重试错误拒绝，不会再被 sticker-only 快路径吞掉；空字符串仍正确落到 sticker-only。测试覆盖到位。
- **`323a363c` 的 reply 附件提升**（`adapt.go` 把 `msg.Message.Reply.Attachments` 并入新消息附件）：是有意的语义折衷（代码注释已说明），让"看一下这张图"这类 discuss turn 能走辅助视觉路径。代价是 canonical event 里附件归属被改写，但渲染与持久化路径一致，测试锁定了行为，可接受。
- **`c5a1b92c` 的 typing goroutine**：`defer stopProcessing()` 在 worker 里兜底，processingCtx 是 turn ctx 的子 context，无泄漏路径；4s 刷新间隔对应 Telegram typing ~5s 有效期，取值合理。
- **`d9d13129` 的 `historyRecordIndexesForMessages` 重构**：把原来 `historySourceMessageIDsForMessages` 的对齐逻辑抽成公共函数供 fork source ID 和 replyable ID 复用，cursor 单调对齐避免了重复内容的错配，是正确的去重重构。
- **`AllowedReplyMessageIDs` 为 nil 时校验关闭、非 nil 空 map 时全部拒绝**：语义清晰，只有 telegram + discuss 会话会装配该校验（`toMessagingSession`），不影响其他平台/会话模式。
- **`9a30c3cc` 前端**：轮询有 `visibilityState === 'hidden'` 守卫和 in-flight 去重，`onBeforeUnmount` 清理定时器；`te()` 检查防止 i18n key 缺失时显示原始 key；重试按钮从 `status !== 'ready'` 收紧为 `status === 'failed'`，避免 pending 状态可重复点击。均合理。
- **`9a30c3cc` 的 i18n**：`bots.channels.fields.telegram.*` 三个 key 在 en/ja/zh 三语同步新增，符合项目的本地化规则。

## 建议行动项（按优先级）

1. `recognizeTelegramSticker` 的 endpoint 解析失败路径补写 failed 状态，并为队列任务加最大尝试次数/退避（问题 1）。
2. 抽出公共的 reply 参数解析函数，消除 preview/executor 双实现（问题 4）。
3. `Enqueue` 关闭分支清理所有已接受任务；队列满时改为非阻塞丢弃 + 日志（问题 2、3）。
4. 中期：sticker 目录描述改为结构化数据契约，淘汰中文标点左右解析（问题 5）；`RecognizeTelegramSticker` 形参 `userID` 改名（问题 7）。
