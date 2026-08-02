# Telegram 上下文、工具与缓存优化实施报告

日期：2026-08-02

## 结论

本轮需求已经按 Telegram 场景落到代码和 Web 配置中。实现原则是：数据库继续保存完整历史，只精简临时发送给模型的投影；工具集合由持久配置决定；System、工具顺序和旧历史保持稳定，新信息只追加在尾部。这里没有依赖 OpenAI 专属缓存参数，所有缓存措施都适用于兼容 Chat Completions、Messages 或其他支持前缀/KV Cache 的服务。

最终修正版遵循三个优先级：先减少 LLM 调用次数，再保护缓存命中，最后缩小上下文。两个线上 Bot 均配置为 Tool Call 开启、Skills 关闭、Telegram 只允许第一方 `send`、元数据 `compact`、自动补发兜底关闭。Sticker 已合并到 `send.sticker_id`，模型一次看到全部 Set 的合并目录，不需要搜索；当前会话 `send` 成功后本轮立即结束。

真实 Telegram 群聊捕获显示，优化后的连续两个 LLM step 分别为：

- 52,259 input / 52,096 cache read，命中约 99.7%。
- 52,633 input / 52,480 cache read，命中约 99.7%。

也就是说，连续回合实际新增、未命中的输入只有约 163 和 153 token。此时保护稳定前缀比逐轮动态改写 Prompt 更重要。

## 需求实现情况

### 1. Telegram 工具开关与配置感知

- 保留模型的 Tool Call 能力，不再要求在“全部工具”和“完全无工具”之间二选一。
- 在 Bot 的“平台 → Telegram”配置中增加 Tool Call 总开关和 Skills 独立开关。总开关关闭时，Native 与 Tool Gateway 都不会再把任何工具交给模型；Skills 关闭时也不会加载 Skill 目录或暴露 Skill 工具。
- 在 Bot 的“平台 → Telegram”配置中展示当前可暴露的工具，并支持逐项、全部开启、全部关闭。
- 工具选择保存到 Bot Metadata 的 `telegram_enabled_tools`。未出现该字段的老配置保持原有“全部可用工具开启”行为；显式空数组表示全部关闭。
- 工具白名单在生成工具 Schema 和 Tool Usage 之前应用，因此关闭的工具不会以 Schema 或说明文字进入请求。
- Search Provider 未选择或被停用时，不暴露搜索工具。
- Bot 没有邮箱绑定时不暴露邮件工具；只有读/写权限中的某一类时，也只暴露对应工具。
- Native Runtime 与 ACP/MCP Tool Gateway 使用同一 Telegram 策略；工具缓存 Key 使用排序后的白名单，但实际 Schema 仍保留 Provider 的稳定原始顺序。

主要实现位置：

- `internal/agent/channelpolicy/`
- `internal/agent/runtime/native/agent.go`
- `internal/mcp/tool_gateway_service.go`
- `internal/agent/tool/web.go`
- `internal/agent/tool/email.go`
- `internal/handlers/mcp_tools.go`
- `apps/web/src/pages/bots/components/channel-settings-panel.vue`

当前线上 `kosame` 与 `hatsuyuki` 都只允许模型使用 `send`。Sticker 后端工具只在组装阶段用于扩展 `send.sticker_id`，不会作为第二个模型工具出现；旧的 Sticker 搜索工具也被过滤。`list_skills`、`use_skill` 由 Skills 开关一并关闭。Memory=None 时 `search_memory` 不会暴露；Search Provider 和 Email 未配置时，对应原生工具也不会出现。

Web 工具目录不是硬编码清单：后端按 Bot、平台和实际 Provider/绑定/工作区能力实时列出“可打开”的工具，再用 `telegram_enabled_tools` 表示当前勾选项。因此当前只开放 `send`，但以后仍可在 Web 手动打开该 Bot 真正具备的其他工具。

### 2. 标题可直接使用第一条文字消息

现有标题流程已经具备所需行为：新 Session 会立即把第一条有效文字消息去 Markdown、取首行并按 50 个 Unicode 字符截断；只有用户另外选择标题模型时，才会在后台用模型生成的标题替换它。没有标题模型时，不产生标题 LLM 请求。

本轮把 Web 选项文案明确为“截取第一条文字消息”，避免把空选项误解为功能关闭。

### 3. Web 手动压缩

Web 当前已经提供会话手动压缩：Session 信息面板与输入框 `/compact` 快捷动作调用同一个 `/bots/{bot_id}/sessions/{session_id}/compact` API。它只生成新的压缩上下文，不改写或删除原始历史记录。

本轮没有重复实现第二套入口，而是对现有路径做了代码和实际界面调用链核查。

### 4. Telegram 元数据精简且可配置

Telegram 配置新增：

- `精简（推荐）`
- `完整`

完整元数据仍写入 Canonical Timeline 和数据库。精简只发生在送给模型前，覆盖普通历史、当前消息、注入消息、Continuation 和 Discuss 群聊路径。

精简模式会去掉逐条重复的 `t`、`channel`、`conversation`、`type`、`target`；群聊保留发言者、消息 ID、回复关系、提及、自身消息、编辑与转发等动态信息，私聊还会去掉重复 sender。嵌套的回复引用也会一起投影。

真实样本中，逐条重复的 `channel="telegram"` 一类属性从首次捕获的 148 处降到 2 处静态示例；在历史消息更多的情况下，请求仍从约 221.0 KB 降至约 208.7 KB。

### 5. Memory 可以切换为 None

问题原因是后端把空字符串和“字段未提交”都当成不更新，导致 Web 选择 None 后保留原 Provider。

现在 API 使用可空字段区分两种意图：

- 不提交 `memory_provider_id`：保持原值。
- 显式提交空字符串：写入 `NULL`，即 None。

SQL、服务层、备份恢复、Slash Command 调用方、OpenAPI 与前端 SDK 已同步，并增加了明确清空的回归测试。

### 6. Telegram 流式 API

复查发现，原有 Telegram Adapter 虽然支持流式，但旧 Discuss Prompt 要求群聊文字必须通过 `send` 工具发送。工具参数只有在 JSON 完整生成后才执行，因此真实 Telegram 群聊并没有生成期文字流。这也是此前“看不到流式”的原因。

最终流式行为是：私聊使用 Telegram 官方 `sendMessageDraft`，结束后调用一次永久 `sendMessage`；群组和频道因为官方 Draft 不支持，不再做“先发占位消息、再 `editMessageText`”的伪流式。群组增量只在服务内缓冲，完整结果就绪后发送一次。

Discuss 普通 Assistant Text 和 Reasoning 始终是私有输出，不进入 Telegram。只有显式 `send` 工具参数代表允许公开。Adapter 回归测试验证群组的 Delta 与 PhaseEnd 不产生 Telegram 请求，Final 只产生一次发送；私聊测试继续覆盖官方 Draft。

### 7. `send`、Sticker 合并与历史投影

`send` 是模型工具。Tool Call 必须在同一步配对最小 Tool Result，不能把结果拖到下一条用户消息才补，否则历史会形成未闭合工具调用。优化点不是省略 Tool Result，而是成功的当前会话发送返回 `{"ok":true}` 后立即终止本轮，不再让 LLM 为“确认成功”重读一次完整上下文。下一条用户消息到来时，配对后的 Call/Result 才随历史一起进入请求。

第一方 `send` 支持纯文字、纯 Sticker、文字加 Sticker；`text` 可空，但它与 `sticker_id` 至少有一个。通常每条公开信息至少配一张合适 Sticker，这是推荐下限而非上限。模型从所有 Web 配置 Set 的合并完整目录直接选择，不需要搜索，也看不到旧的独立 Sticker MCP 工具。

成功发送的历史在未来模型投影中折叠为实际可见的短 Assistant 记录；失败或部分成功的闭环完整保留，Canonical History 不改写。

### 8. Sticker 第一方管理与永久元数据缓存

Web 的 Telegram 设置中增加 Sticker 目录入口，展示预览、视觉描述和 `ready` / `failed` / `pending` 状态；失败项可以单独手动重试识别。

Web 还提供 Sticker Set 列表编辑和独立视觉模型选择。多个 Set 在浏览器中分组展示，在模型看来是一个确定性排序的完整目录；每项失败都可单独重试。

Sticker Set 元数据使用 SQLite 永久缓存，没有 TTL：

- 第一次缺失时调用 Telegram `getStickerSet` 并写入 SQLite。
- 后续读取优先内存，再读 SQLite；进程重启后仍不请求 Telegram。
- 缓存按 Bot Token 哈希与 Sticker Set 名称隔离，数据库不保存 Token。
- 只有 Web 中的“刷新 Sticker Set”或对应显式 API 才会重新请求 Telegram 并替换缓存。

`X-Telegram-Sticker-Set` 没有废弃，而是降为服务端内部兼容协议：Web 只提交 Set 名称列表，后端把经校验、去重、排序后的名称以逗号分隔写进 MCP 连接 Header，并保留原有认证 Header。Sticker 服务也兼容逗号、换行和分号分隔；Header 缺失时仍可使用独立部署的环境变量默认 Set。浏览器、模型和文档响应都不会拿到 Token 或 Header。

最终线上目录为 `kosame` 46 张、`hatsuyuki` 120 张。首次重试失败的根因是继承视觉模型时丢失 Provider，`gpt-5.6-luna` 在后端解析为跨 Provider 歧义；修复后继承配置保留 `openai-codex` Provider。随后强制重新识别全部 166 张，166/166 成功，失败和待识别均为 0。重启 Sticker systemd 服务后仍为 166/166，证明结果从永久 SQLite 缓存恢复。

完整目录嵌入唯一模型可见工具 `send.sticker_id`。这会增加稳定工具前缀，但每条消息不再为 Sticker 搜索增加 LLM 步骤；在已观测的高前缀缓存命中场景下，符合“调用次数优先、缓存其次、上下文第三”的目标。

### 9. Prompt 独立审查及安全修复

第一位子代理进行了只读 Prompt 审查。落地了其中风险低、收益明确的项目：

- 工具过滤提前到 Schema 和 Tool Usage 生成之前。
- Memory Provider=None 时不再注入约 2.6 KB 的 Memory 写作手册；用户自定义 `MEMORY.md` 仍保留。
- 平台身份只注入当前平台，并使用字段白名单。
- Telegram 自身身份不再保存含 Bot Token 的头像 URL；`avatar_url` 和未知身份字段不会进入 LLM Prompt。
- 重复的 external/user ID 会去重。
- `AGENTS.md`、`PROFILES.md`、`MEMORY.md` 的加载顺序调整为把更常变化的 Memory 放在末尾。
- Skills 和平台身份使用确定性顺序。

安全说明：旧版本曾可能把带 Bot Token 的 Telegram 头像 URL 放进 System Prompt。若这个 Bot 曾配置头像并在旧版本运行过，部署后应轮换一次 Telegram Bot Token。

### 10. 部署、真实群聊与原始请求

本轮使用当前实际 Telegram 群聊完成了一次真实触发，并通过只记录 HTTP request body、不记录 Header 的临时代理捕获模型请求。请求成功转发，Assistant 输出成功写回会话。

第一次捕获代理保留了错误的 Host，导致上游返回 403，并造成两个测试回合没有正常回答；发现后立即恢复 Provider 地址，修正代理，再完成成功捕获。测试结束后 Provider 已恢复到原始端点，临时代理已停止。

脱敏后的原始发送内容保存在：

- [Telegram 真实群聊 LLM 请求（脱敏原始 Payload）](./2026-08-02-telegram-live-payload.md)

该文件保留请求结构、消息顺序、工具 Schema 和普通文本，仅替换身份标识、Token/JWT 模式、媒体路径与 Base64 内容。它仍含私密群聊正文，不应公开提交或对外转发。

生成并完成脱敏扫描后，`/tmp` 中未脱敏的捕获文件和临时代理源码已删除；未脱敏副本不再保留。

最新版本已重新构建并部署 `server`、`channel`、`web`，Sticker MCP 由用户级 systemd 服务重启；四项服务健康。原始 LLM Payload 文档记录的是工具精简前的审查样本，适合对比为什么本轮要关闭 37 个工具和移除 18.3 KB 的动态 Sticker 目录，不应误认为当前工具集合。

## 供应商无关的缓存审查

### 已实现的缓存稳定措施

1. 工具列表使用持久 Telegram 配置，不按每条消息的语义临时增删。
2. 过滤后仍保持 Provider 原始工具顺序；仅用于缓存 Key 的白名单副本会排序。
3. System Prompt 不注入当前时间。
4. 容易变化的 `MEMORY.md` 放到 Workspace 文件片段末尾。
5. Telegram 的强制回复状态不再动态改写 System；Native Discuss 模式改为在消息尾部追加一次性 operator signal，并在持久化前移除。
6. 图片先附着到真实用户消息，临时强制回复 signal 最后追加，避免改变语义和历史。
7. Metadata 只做确定性投影，不重排旧消息。
8. Sticker Set 元数据和视觉描述永久落 SQLite，目录读取不会周期性刷新；完整 Sticker 目录以固定排序写入稳定的 `send.sticker_id` Schema。
9. `send`/Sticker 成功闭环只在未来模型投影中折叠，Canonical 历史保持不变，避免每轮重写整个前缀。

真实捕获验证了这些措施的效果：部署或 Prompt/工具配置变化后的第一次调用是冷缓存，随后相同前缀的调用回到约 99.7%–99.8%。曾观察到动态改写 System 的强制回复调用只有约 5.1% cache read，这正是第 5 项修改的依据。

### 第二位子代理对真实请求的独立观察

子代理只读分析了脱敏 Payload，没有修改代码，也没有复述群聊内容。按紧凑序列化近似：

| 部分 | 体积 | 请求占比 |
|---|---:|---:|
| Messages | 约 157.7 KB | 76.1% |
| Tools | 约 49.5 KB | 23.9% |
| 其他顶层参数 | 约 0.1 KB | <0.1% |
| 全部 Assistant 消息 | 约 81.1 KB | 39.1% |
| System 消息 | 约 24.7 KB | 11.9% |

它确认当前最高优先级是保护 99.7% 的稳定前缀，而不是为少量 token 在每轮重写请求。

样本指出的前三个大项已经实施：持久工具白名单、稳定的 Sticker 完整目录、成功发送历史投影。剩余可继续做但不应自动动态化的项目是：

1. **继续合并 Tool Usage 与 Schema Description 的重复说明。** 只把跨工具工作流留在 System，其余规则由 Schema 负责。
2. **继续人工维护工作区 `AGENTS.md`。** 两个线上 Bot 已统一为 `send` 唯一公开边界、默认兜底关闭、Sticker 合并目录和“通常每条公开信息至少一张”的规则；后续修改人设时应避免重新加入独立 Sticker 工具、显式复制当前 `target` 或括号伪发送。
3. **增加不记录正文的缓存诊断。** 可记录 System、ordered tools、稳定历史前缀的哈希与字节/token 数，用于定位冷缓存来自哪一段。
4. **自有推理集群做一致性路由。** 若网关后面有多个推理副本，可按 `model + 稳定前缀哈希` 路由到同一实例；单端点或不可控网关不适用。

不建议做：逐消息动态增删工具、每轮重排或重写历史、用 HTTP gzip 代替 token 优化、把某家供应商的私有 cache 参数作为核心方案。

观察缓存时应同时看总 Input、Cache Read、未缓存 token、首 token 延迟和费用。只看百分比可能误判：手动压缩后总输入降低，cache-read 绝对值和比例都可能下降，但总成本仍更低。

## 验证记录

完成或执行过的检查包括：

- 相关 Go 单元测试。
- `go test ./...`。
- `mise run lint`。
- `pnpm --filter @memohai/web build`。
- Docker Server/Web 镜像重建、服务重启和健康检查。
- 真实 Telegram 群聊 LLM 触发、原始请求捕获和成功持久化。
- 私聊官方 Draft 与群组最终单次发送的 Adapter 回归测试。
- Sticker Set 元数据跨 systemd 服务重启永久复用实证。
- 两个 Bot 共 166 张 Sticker 的全量强制重识别及重启后 166/166 `ready` 验证。
- 原始 Payload 的 Token、JWT、UUID、Telegram 标识和媒体路径脱敏扫描。

`internal/agent/runtime/session` 的“subscriber buffer overflow recovery”时序测试在默认并行调度的一次全量运行中超时；该包没有被本轮修改，使用 `GOMAXPROCS=1` 连续三次复跑通过。最终验证结果以最后一次完整运行和部署状态为准。
