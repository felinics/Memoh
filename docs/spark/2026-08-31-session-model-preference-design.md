# Session 模型+推理强度持久化 · 设计稿(v2)

- **Issue:** memohai/Memoh#879 — [Web] Chat session 模型切换未持久化,刷新后回退 bot 默认模型
- **状态:** v2 定稿 2026-09-02(v1 拍板于 2026-08-25 ~ 08-31;v1→v2 变更与依据见附录 A)。实现分支 `feat/session-model-preference` 基于 v1,需按 §6 调整
- **参照:** lobe-chat PR #15933 / #18178(独立同构演化,见 §5)
- **读法:** §1 用户路径是验收标准;§2 由路径推导机制;§3 技术设计由 §2 生成,每条机制能指出服务哪条路径。凡路径与技术冲突,以路径为准。代码位置以 main 头 cd496786c 附近为准,行号会漂。

> **TL;DR(给 reviewer / leader,零技术前提)**
>
> 以前,聊天框顶上选的模型和推理强度只活在网页组件的内存里:刷新、关标签、换设备,都会悄悄回到 bot 默认;在欢迎页选完没发,这个选择还会串进老对话。
>
> 之后,**你明确选过的**模型和强度成为对话本身的属性:在哪打开这个对话,它就显示上次的值,刷新、重开、换手机都一样。**没选过的对话**仍然跟随 bot 默认,管理员改默认它就跟着变,和今天一致。欢迎页的选择只是这台机器上的草稿:只进即将新建的对话,不碰老对话。模型和强度是一对:换模型,强度跟着新模型走。
>
> 弱网下:选了立刻显示,永不弹回;一条消息被服务端收到,选择就永久生效。Telegram 用户零感知;在网页选过模型的对话到 Telegram 里继续时沿用那个模型,在 Telegram 里执行 /model 则重新接管。
>
> 验收:照 §1.4 九幕走查,每幕"composer 显示 = 实际发送 = 事后读取"三处一致。

## 1. 用户路径

### 1.0 两个概念

- **对:** picker 的状态永远是一个完整的 **(模型, 强度)对**。改强度 = 只换强度分量;改模型 = 换掉整个对,强度落到新模型的默认档。发送、显示、记忆、播种全是完整对。
- **来源:** 对有一个来源:`默认`(bot 默认 / subagent pin 自动填入,用户没碰过)、`记忆`(从 session 持久值读出)、`用户`(本机 picker 明确选择)。**只有 `记忆` 和 `用户` 来源的对会随消息发出并被记住;`默认` 来源的对不发出、不记忆。** 这是"选过"与"没选过"的分界。

### 1.1 修复前路径(现状,逐条可复现)

| # | 用户动作 | 今天实际发生 |
|---|---|---|
| P1 | session 里把对换成 (2, high),发消息,刷新 | composer 变回 bot 默认;下一条**静默用默认发出** |
| P2 | welcome 选了 (2, high) 不发;打开上周用 (3, low) 的历史 session | composer 显示 (2, high),每条消息显式发出,session 被迫用错对 |
| P3 | 手机/另一台电脑打开该 session | 显示 bot 默认,值只在本机 pane 内存里 |
| P4 | 关 tab 重开、ephemeral tab 被顶后重开、切 bot 再切回 | 同 P1 |
| P5 | ACP(Codex/CC)会话把强度选到 max,发对话或刷新 | 回退 medium(Go 2026-08-18 实测) |
| P6 | 把对里的模型切到不支持推理的模型再切回 | 原强度档丢失(现状是跨模型保留,v2 有意改为不恢复,见 P6′) |
| P7 | 弱网下换对 | 能换(乐观显示),刷新即丢 |
| P10 | 从未碰过 picker;管理员改 bot 默认模型 | 下一条即用新默认(**此行为 v2 保留**) |

(P1/P2 为投诉原文:xhe 2026-08-14、Go 2026-07-29;其余从代码推出。)

### 1.2 修复后目标路径(验收基准)

- **P1′ 刷新不丢:** session 里换成 (2, high) → 立即显示 → 刷新 → 仍 (2, high) → 发消息用 (2, high)。
- **P2′ 会话隔离:** welcome 选 (2, high) 不发 → 打开历史 session(它的对是 (3, low))→ 显示 (3, low);回 welcome 仍 (2, high)(本机草稿)。welcome 选 (2, high) 发首条 → 新 session = (2, high);回 welcome 仍 (2, high)(此时它是"我最近 session 的对",多设备一致)。
- **草稿不跨设备:** 电脑 welcome 的未发草稿,手机上看不到 → 手机显示服务端"我最近 session 的对"。
- **P3′ 跨设备:** 桌面把某 session 换成 (2, high) → 手机打开 → 显示 (2, high) → 不碰 picker 直接发 → 用 (2, high)。
- **P4′ pane 无关:** 关 tab 重开 / ephemeral 被顶后重开 / 切 bot 再切回 → session 显示它自己的对。
- **P5′ ACP 不回退:** 换成含 max 的对,刷新/重开/进程重建 → 仍是 max。
- **P6′ 换模型=换整个对:** (2, high) 切模型到 5 → (5, 5 的默认档);切回 2 → (2, 2 的默认档),不恢复 high。**这是对现状的有意改变**,QA 基线标注。
- **P7′ 弱网:** 换对 → 立即显示,永不弹回;任何一条消息被服务端接收后对永久生效(回答生成失败不影响)。残余窗口(已接受):换了、PATCH 失败、一条没发就刷新 → 回旧值。
- **P8′ 多 tab:** 同一页面内(app 的 dockview 面板共享 JS 上下文),同一 session 的多个面板**显示同一个对**(改一处全变);两个浏览器标签页/跨设备不实时同步,**谁发消息谁赢**,刷新后一致。
- **P9′ 首发不闪回:** welcome 带着 (2, high) 发首条 → 新 session 从头到尾显示 (2, high)。
- **P10′ 没选过就跟默认:** 从未碰过 picker 的 session,管理员改 bot 默认后下一条即用新默认,composer 显示新默认。与今天一致。
- **P11′ 渠道接管:** 某 session 在网页选过 (2, high);在 Telegram 里继续 → 用 (2, high);在 Telegram 里执行 `/model 3` → 该 session 的记忆被清空,之后该 session 用 bot 默认(此时即 3),网页打开显示 3 且来源为 `默认`。
- **纯渠道路径不变:** 从未在网页选过对的渠道会话,选择逻辑与今天一致,渠道侧零新增概念/UI。
- **其余不变:** subagent 会话的 pin 是它的初始对(来源 `默认`),可在其中改;retry/edit 沿用 composer 当前对并同样写回;运行中 turn 不受 picker 影响。

### 1.3 路径语义(技术设计的验收标准)

- **S1 对是 session 的持久属性:** 重新打开、刷新、换设备,读到的都是 session 当前持久值。
- **S2 welcome 是草稿态:** 未发的对只待在本机 welcome;一发就进新 session。
- **S3 显式选择即记忆:** 只有来源为 `用户` 或 `记忆` 的对会被发送和记住。没有记忆的 session(渠道会话、从未选过的 web session)沿用 bot 默认链,与今天一致。
- **S4 选了就显示:** picker 不回弹;落库失败不产生用户可见错误。
- **S5 发送时捕获:** picker 显示的对 = 下一条消息用的对;运行中的 turn 不被追改。
- **S6 对永远完整合法:** 任何写库/播种/回放出口,对都经 reconcile 成完整合法对;DB 不存非法对。

### 1.4 第一人称走查(九幕,人类 QA 剧本)

**① 冷启动。** 第一次打开 bot 的聊天页,composer 显示 bot 默认。直接发第一条,新 session 诞生,composer 不跳变。这个 session 没有记忆:管理员之后改默认,我下一条就用新的。(P9′/P10′)

**② 换个模型干活。** 点 picker 换成 (2, high),composer 立刻变,无转圈无 toast。发消息,回答来自模型 2。刷新还是 (2, high);关 tab 下午重开,还是。(P1′/P4′)

**③ 换设备。** 手机打开同一 session,显示 (2, high)。不碰 picker 直接发,仍用模型 2。(P3′)

**④ welcome 草稿。** 回欢迎页,显示 (2, high),就是我最近在用的。换成模型 5 没发成被叫走;第二天回来,(5, 默认档) 还在。手机上打开 welcome 看不到这个草稿,显示 (2, high)。发出第一条,新 session 用 5;草稿清了,欢迎页此后显示 5。(P2′/P7′)

**⑤ 老 session 不被污染。** 打开上周用模型 3 的 session,显示 (3, 它的强度)。(P2′)

**⑥ 地铁弱网。** 换成 4,立刻显示,不弹错。一条消息发出去了,4 永久生效。另一个结局:一条没发成就关了 App,下次打开回 (2, high)。(P7′)

**⑦ 模型和强度是一对。** (2, high) 切到 7 → (7, 7 的默认档);切回 2 → (2, 2 的默认档),不记得 high。(P6′)

**⑧ 我是 Telegram 用户。** 界面零变化。这个对话若被人在网页上选过模型,我继续聊会用那个模型;我执行 /model 换成别的,之后就用我换的。(P11′)

**⑨ ACP 会话。** 强度调到 max,刷新、重开、进程重建之后仍是 max。(P5′)

**用户收到什么消息:什么都收不到。** picker 本身就是全部反馈。

## 2. 推导:路径强迫技术做什么

| 路径 | 强迫出的结论 |
|---|---|
| P1′/P3′/P4′ | 对存服务端、挂在 session 上;前端只是显示副本 |
| P2′ | 打开已有 session 只认它自己的对;repoint 一律重播种 |
| P2′(回 welcome 显示刚发的对) | welcome 来源链(低→高):bot 默认 < **该 bot 且该用户最近一条有记忆的 native session 的对** < 本机未发草稿 |
| P7′/S4 | picker 纯乐观;PATCH best-effort;不加重试队列,下一次发送就是重试 |
| P7′(发了一条即永久)+ S3 | 写回的门是**数据**:请求携带对才写。前端按来源决定携带/省略(`默认` 省略)。渠道请求结构上从不携带,自然不写 |
| P10′ | `默认` 来源的对不写库;读链里"记忆"为空时落 bot 默认,管理员改默认即时生效 |
| P9′ | session INSERT 时随行写入首发对,行诞生即完整;`session_created` 广播携带完整行 |
| P8′ | 同浏览器多 tab 共享一个前端 view 状态;跨浏览器不同步 |
| P11′ | 渠道 `/model` `/reasoning` 命中有记忆的 session 时清空该 session 的对 |
| P5′ | ACP 的对写进 session `runtime_metadata`;spawn/resume 后覆盖 profile 默认;PATCH 端点双写 |
| S6 | 三个写点(PATCH / INSERT / 每轮)全部先 reconcile |

## 3. 技术设计

### 3.1 存储

- 迁移:`bot_sessions` 加 `preferred_chat_model_id UUID REFERENCES models(id) ON DELETE SET NULL` 与 `preferred_reasoning_effort TEXT`。**编号取 main 当前最大号 +1**(2026-09-02 origin/main 已到 0145,实现分支的 0144 需重编),`0001_init.up.sql` 同步,`.down.sql` 成对。
- 语义:物理两列逻辑一个对,成对写、成对清。`NULL/NULL` = 无记忆。写 UPDATE 不碰 `updated_at`(侧栏按 `updated_at` 排序,picker 不能改变会话顺序)。
- 强度词汇与 bot settings 一致:tier 字符串或 `"disable"`;不存空串。
- **不加** `bot_history_messages.reasoning_effort`。
- ACP session 两列恒 NULL,其对在 `runtime_metadata`(§3.6)。

### 3.2 每轮解析链(native)

模型:
```
请求携带 > session 记忆 > subagent pin > bot 默认 > 历史消息最新 model_id
```
强度(在 `reasoning.ResolveConfig` 内,新增一级):
```
请求携带 > session 记忆 > bot 存储值 > 模型默认档
```
- 实现位置:`buildBaseRunConfig` 之前一次读 session 行(`GetSessionByID`),把记忆作为**独立参数**传给 `selectChatModel` 与 `ResolveConfig`;不冒充 `req.Model` / `requested`。
- `applySubagentThreadDefaults` 改为:仅当 session 记忆为空时才填 pin。
- `ReasoningStoredEffort` 传给子代理时保持 bot 存储值;session 记忆**不进** `ReasoningRequestedEffort`。
- `SessionType ∈ {schedule, heartbeat}` 的轮跳过"session 记忆"一级。
- ACP 轮不经此链(在 `resolve` 前分流,现状)。
- 恢复轮(ask_user / 工具审批,经 `ResolveRunConfig`)自动受益于记忆一级。

### 3.3 写点(全部经 reconcile)

reconcile = `PatchSessionModelPreference` 现有逻辑:目标模型必须存在且 provider 启用;强度经 `reasoning.NormalizeSelection` 对目标模型校验,非法或空则落模型默认档;模型不支持推理则强度写 NULL。

1. **PATCH** `PATCH /bots/:bot_id/sessions/:session_id`,body 新增 `preferred_chat_model_id` / `preferred_reasoning_effort`(与 title 等并列,可单独出现)。模型不可解析 → 400;强度非法 → 静默落默认档(200)。前端乐观显示,失败静默。
2. **INSERT** `POST /bots/:bot_id/sessions` body 新增同名两字段(仅前端有 `用户`/`记忆` 来源时携带)。handler 先 reconcile 再传 `CreateInput`。WS 内建 `createWSChatSession` 同样接收 `msg.ModelID/ReasoningEffort` 作兜底。fork 继承源 session 两列。
3. **每轮** 在 `resolve()` 中、`buildBaseRunConfig` 返回后:若 `req.Model != ""` 或 `req.ReasoningEffort != ""`(即请求携带),取解析出的 `chatModel.ID` 与 `reasoningConfig` 归一后的强度,**与读到的记忆比较,不同才 UPDATE**。比较口径:模型按 UUID;强度按归一值(`Active→Effort`,`Disabled→"disable"`,`nil→NULL`)。失败仅记日志。
4. **渠道清空** `internal/command` 的 `/model` 与 `/reasoning` 处理器,在写 bot settings 成功后,若 `cc.SessionID` 非空且该 session 两列非 NULL,则 `UpdateSessionModelPreference(NULL, NULL)`。两个命令都清整对(对是单值)。渠道确认文案不变。

不做的写点:轮末/成功后写(失败轮会丢对,违反 P7′);入口标志位(冗余且已漏 retry/edit)。

### 3.4 播种与前端结构

对搬到 `ChatViewEntry`(与 workspace target 并列):`pairModelId: Ref<string>`、`pairEffort: Ref<string>`、`pairSource: Ref<'unset'|'default'|'session'|'user'>`。chat-pane 的 `overrideModelId/overrideReasoningEffort` 与其播种/重置 watch 群删除;`chat-list.ts` 同名死代码删除。

来源转移表:

| 事件 | 动作 |
|---|---|
| view 绑定到已有 session,行已加载 | 两列非 NULL → 置 `session`;否则 pin 或 bot 默认 → `default` |
| session 行晚于 view 到达(列表刷新)| 仅当当前 `source ∈ {unset, default}` 时按上行重播种;`user` 不被覆盖 |
| PATCH 在飞期间行刷新 | 跳过重播种(pending 守卫) |
| welcome view 初始化 | 本机草稿 → `user`;否则种子端点 → `session`(它是别的 session 的记忆,但语义上是"我要携带");否则 bot 默认 → `default` |
| 用户选模型 | 整对换新(强度=新模型默认档),`user`;有 session 则 PATCH,welcome 则写草稿 |
| 用户选强度 | 只换强度,`user`;同上写点 |
| promoteDraft(welcome→新 session)| 对随 view 迁移(现有机制);清本机草稿 |
| view 切换 bot | reset → `unset` |
| native↔ACP 切换 | reset → `unset`,ACP 走 §3.6 |
| bot 默认变更(settings 刷新)| 仅 `default` 来源跟随 |

发送规则(send / retry / edit 共用):`source ∈ {user, session}` → payload 携带 `model_id` + `reasoning_effort`;`default`/`unset` → 两字段省略。createSession 同规则。

草稿:localStorage 键 `memoh:composer-pair:<botId>`,值 `{model_id, reasoning_effort}`,首发即清;与 `useComposerDrafts` 并列,不合并(它按 tab 键,对按 bot 键)。

种子端点 `GET /bots/:bot_id/sessions/model-preference-seed`:该 bot、`created_by_user_id = 当前用户`、`runtime_type='model'`、`type='chat'`、`visibility='user'`、`deleted_at IS NULL`、两列非 NULL,按 `updated_at DESC` 取 1。无结果返回空对象。

列表对账:PATCH 成功后 `patchSessionInList` 更新两字段;`replaceSessions/rememberSession` 覆盖本地行前保留两字段(它们来自服务端,覆盖即最新,只需保证不丢)。

### 3.5 多 tab

同一页面内同 session 的 dockview 面板共享 `ChatViewEntry`,天然同步。两个浏览器标签页(独立 JS 上下文)与跨设备一样不做同步,谁发消息谁的对写库,刷新后收敛(P8′)。

### 3.6 ACP

- 存储:`runtime_metadata.acp_model_id`(string)与 `runtime_metadata.acp_reasoning_effort`(string)。不进两列(ACP 模型 id 是 agent 命名空间字符串,不是 `models` UUID)。
- PATCH 端点 `SetModel` / `SetReasoning`(`handlers/acp_runtime.go`)在 `pool.SetX` 成功后,把 agent 自报的当前值经 `sessionService.UpdateDescriptorAndMetadata...` 合并写入 `runtime_metadata`(只改这两个键)。失败仅日志。
- 挂点:`session_pool.startRuntime` 内、`client.Start` 返回后(即 `client/session.go` 已应用 profile 默认之后),读 `runtime_metadata` 两键:有值则 `SetModel` / `SetReasoningEffort`(先模型后强度,同 `applyPromptConfig` 顺序);值不可用(agent 拒绝)则日志并保留 agent 值。resume 路径同一挂点。
- 前端 ACP composer 现状:PATCH 失败回滚并显示错误。**保留**,因为 ACP 端点是同步操作活体进程、有真实失败语义;S4"永不弹回"只约束 native 路径。
- ACP 会话不参与 §3.4 种子查询(runtime_type 已排除)。

### 3.7 非目标

- 跨浏览器/设备的实时同步
- 逐条消息对的 UI 投影
- 渠道 `/model` 写 session 对(只清)
- 跨模型的强度记忆
- 第二张全局偏好表(lobe 式 per-(user, model))

## 4. 验证

### 4.1 问题证明(修复前基线)

在 main 上复现 §1.1 P1–P7 留证;P10 记录为"必须保持"的基线。

### 4.2 路径正确性(实现前评审)

S1–S6 每条被 §1.2 覆盖;P1′–P11′ 每条能沿 §3 推出唯一确定的对与来源;退化窗口仅 P7′。

### 4.3 执行正确性(AI 自检)

机制级测试:
- 解析链五级顺序(含 pin 与记忆的先后、schedule 跳过、恢复轮读到记忆);`service_model_selection_test.go` 现有 `TestSelectChatModelFallsBackToSessionLastModel` 需加记忆一级用例。
- `ResolveConfig` 新一级:记忆在 stored 之上 requested 之下;记忆不进 `ReasoningRequestedEffort`。
- 写回条件:携带才写;渠道命令不携带不写;retry/edit 写;值相同不写;归一比较口径。
- reconcile:非法档落默认档;不支持推理的模型强度落 NULL;模型不存在 400。
- INSERT 随写 + `session_created` 行完整;fork 继承。
- 渠道 `/model` `/reasoning` 清空。
- 种子查询按用户过滤、排除非 native。
- ACP:spawn 后覆盖;PATCH 双写;agent 拒绝时保留。
- 前端来源转移表逐行;发送省略/携带;`reasoning-effort.test.ts` 中跨模型保留强度的用例按 P6′ 改为期望落新模型默认档。

路径级脚本:每条 P′ 输出三元组"composer 显示 = WS payload = 服务端两列",含 P8′ 同浏览器同步、P10′ 默认变更、P11′ 渠道清空。

### 4.4 人类 QA(终审)

§1.4 九幕,真手机、真 Telegram。通过标准同 v1。行为变化清单(需明确告知 QA 为预期):P6′ 不再跨模型保留强度;同浏览器多 tab 同步显示;渠道 `/model` 多一个清空动作;retry/edit 参与写回。

## 5. 取舍记录

| 决策点 | 采纳 | 否决及原因 |
|---|---|---|
| 配置单位 | (模型, 强度)成对单值 | 两个独立设置 = 双链双写双 reconcile |
| 记忆产生时机 | **显式选择即记忆**(来源 ∈ {user, session} 才写) | v1"首发即快照":前端总把 picker 填成 bot 默认,导致每个 session 都被冻结在当时的默认,管理员改默认对所有 web 老用户永久失效,包括从未选过的人 |
| bot 默认的角色 | 未选过的 session 实时跟随(与今天一致) | v1"降级为 IM 实时值"随上条撤回 |
| 写回的门 | 数据(请求携带才写) | 入口标志:已漏 retry/edit,且每新增入口需记得置位;`CurrentChannel` 字符串:WS 是 "web",retry/edit 是 "local",不可用 |
| 写回时机 | 请求被接收即记(`resolve()` 内) | 轮末写:失败轮丢对;放 `buildBaseRunConfig`:它是纯构建器且被恢复轮复用 |
| 首发送对 | 前端 createSession 带对,handler reconcile 后 INSERT | WS 内建传参:真实前端流程走 REST 建 session,该路径不触发;"首轮再补写":赶不上同步的 `session_created` 广播 |
| welcome 语义 | 草稿 + 按用户的最近 session 种子 | lobe 式写 agent 默认 = bot 默认被 IM 每轮实时消费;种子按 bot = 多成员互相污染 |
| 弱网 PATCH 失败 | 纯乐观 + 发送即落库 | 失败回滚 = 网卡时不让换 |
| 多 tab | 同浏览器同步(共享 view)、跨浏览器最后发送者赢 | v1"各显示各的":需要额外机制维持各自状态,反而更复杂 |
| history effort 列 | 不加 | 存量 session 无 effort 可兜;向前只服务非目标"逐条投影" |
| ACP 存储 | `runtime_metadata` 两键 | 两列:模型 id 不是 UUID 进不了 FK;`acp_session_states`:是转录快照索引不是会话配置 |
| ACP 前端失败回滚 | 保留 | ACP 端点同步操作活体进程,有真实失败;S4 只约束 native |
| 渠道 `/model` | 清空 session 对 | 不动:Telegram 用户看到"已切换"但下一条仍用旧模型;写入 session 对:渠道侧多出新概念 |
| subagent pin | 初始对,来源 `default`,用户可覆盖 | pin 是锁:违反 S4/S5 |
| 模型被删 | `ON DELETE SET NULL` | RESTRICT / 悬空 |

**lobe 参照结论:** PR #15933 模型 per-topic pin(创建快照 + topic 内 UPDATE)与本设计同构;分歧在强度层级(他们 per-(user, model))与 welcome(他们写 agent 默认)。其 pending-writes 对账对应 §3.4 的 PATCH 在飞守卫。

## 6. 实现顺序与对现有分支的调整

实现分支 `feat/session-model-preference`(`21917f949` 后端、`b1113cb24` 前端,基于 v1)需做:

**后端**
1. 迁移重编号(main 已到 0145);删 `bot_history_messages.reasoning_effort`。
2. 删 `ChatRequest.PersistModelPreference` 与 `baseRunConfigParams.PersistModelPreference`;写回从 `buildBaseRunConfig` 移到 `resolve()`,条件改为"请求携带且与记忆不同"。
3. `sessionModelPreference` 读取移到 `applySubagentThreadDefaults` 之前;pin 仅在记忆为空时填。
4. `resolveReasoningConfig` 增加 `sessionEffort` 参数,在 `reasoning.ResolveConfig` 内作为 stored 之上一级;不再赋给 `requestedEffort`。
5. `SessionType` schedule/heartbeat 跳过记忆。
6. REST `CreateSession` 收两字段,经 `PatchSessionModelPreference` 同样的 reconcile 后传 `CreateInput`。
7. 种子查询加 `created_by_user_id`。
8. `internal/command` `/model` `/reasoning` 成功后清空当前 session 两列。
9. ACP:`SetModel/SetReasoning` handler 双写 `runtime_metadata`;`startRuntime` 在 `client.Start` 后按 `runtime_metadata` 覆盖。
10. sqlc / swagger / sdk 重生。

**前端**
1. 对搬入 `ChatViewEntry` 三字段;按 §3.4 转移表实现;删 chat-pane 原 override ref 与相关 watch、删 `chat-list.ts` 死代码。
2. send / retry / edit / createSession 按来源决定携带或省略。
3. 删"行未加载则保留刚发的对"特判(INSERT 随写后不再需要)。
4. `reasoning-effort.test.ts` 按 P6′ 改期望。
5. 当前 worktree 落后 origin/main(#1060 重写 composer agent 菜单、#1092 retry/edit 改 turn_id),先 rebase 再动。

**QA** §4 全表。

## 附录 A. 评审修订记录(2026-09-02,对照仓库现状)

> 本附录记录 v1(2026-08-31 稿)到 v2(正文)的变化与判断依据,供审阅追溯;实现只看正文。附录中"修订前"指 v1,"修订后"即正文。

§1–§6 定稿后,对照当前代码(main 头 cd496786c 附近)与已开始的实现分支 `feat/session-model-preference` 逐条核对,发现三处路径冲突、若干技术假设不成立。

### A.1 路径层前后对照

| 项 | 修订前(§1–§5) | 修订后 | 原因 |
|---|---|---|---|
| S3 记忆产生时机 | 首发即事实快照:web 首发被接收即记下当时完整对,**不要求碰过 picker** | **显式选择即快照**:只有用户在 picker 上明确选过(或 session 已有记忆)时,对才随消息携带并被记住;从未选过的人不产生记忆,继续跟随 bot 默认 | 原规则让"没选过"和"选过"在数据上无法区分:管理员改 bot 默认后,对所有 web 老用户永久失效,包括从未做过任何选择的人。这与第⑧幕"行为不变"、以及"没选就用默认"的基本预期矛盾。现状里"请求省略对"从不是常态(前端总把 picker 填成 bot 默认),所以原稿"用解析结果写回"实际是把默认值冻结进每个 session。改为前端只在有明确来源时携带,服务端只在请求携带时写回,弱网兜底仍成立(显式选择必随消息发出) |
| §5 "bot 默认的角色降级" | 有 web 历史的人由"最近实际在用的"接管,bot 默认变更不再推给他们 | **撤回**。bot 默认对未做过选择的 session 继续实时生效;只有显式选过的 session 脱离 bot 默认 | 上一条的直接结果;原取舍记录的"残留怪味"随之消失 |
| 第⑧幕 / §3.6 IM `/model` | 渠道里继续对话沿用 session 的 web 对;IM `/model` 写 session 对列为非目标 | 渠道 `/model`、`/reasoning` 命令命中**已有记忆的 session** 时,**清空该 session 的对**(回落 bot 默认链),其余不变 | 否则 Telegram 用户执行 `/model` 看到"已切换"确认,下一条回复仍用旧模型,是用户可感知的自相矛盾。清空而非写入,渠道侧仍零新概念 |
| welcome 种子口径 | 该 bot 最近一条有对的 native session | 该 bot **且该用户创建**(`created_by_user_id`)的最近一条有对的 native session | 第④幕叙事是"我最近在用的";按 bot 不按用户,多成员 bot 下 A 的选择会成为 B 的欢迎页默认 |
| P8′ 多 tab | 两个 tab 开同一 session 各显示各的,最后发送者赢 | **同一 session 的多个 tab 显示同一个对**(同一浏览器内);跨设备/跨浏览器仍是最后发送者赢 | 由 §A.3 前端结构决定:对挂在 ChatView(同一 session 的多 tab 共享一个 view),天然同步。比"各显示各的"更不易出错,且不需要任何同步机制。**这是对已定边界的改变,需确认** |
| P6′ 换模型不恢复强度 | 目标路径 | 不变,但 §4.1 回归基线须**标注为预期行为变化**:现状 `reconcileStoredEffort` 是跨模型保留强度的,P6′ 是有意改掉它 | 避免 QA 把它当回归 |

### A.2 技术层前后对照

| 项 | 修订前(§2–§3) | 修订后 | 原因 |
|---|---|---|---|
| 写回的门 | **门=入口**:web 走 `StreamChatWS` 每轮必写,渠道走 `StartTurn` 族永不写;实现分支用 `ChatRequest.PersistModelPreference` 标志,只在 `StreamChatWS` 置位 | **门=数据**:请求携带对才写;不携带不写;无标志位 | ① 与 A.1 第一条同源;② 入口标志已漏掉 retry/edit(它们走 `streamChatWSResultWithHooks`,不经 `StreamChatWS`);③ 渠道被结构排除:渠道 inbound 只在 `InboundMessage.Metadata["model_id"]` 存在时填 `cmd.Model`,而唯一往里塞的是 local REST handler(它本身就是 web);④ `CurrentChannel` 不能当门(WS 发送是 `"web"`,retry/edit 写死 `"local"`) |
| 写回的位置 | `buildBaseRunConfig` 内,解析完成、生成未开始;每轮 UPDATE | `resolve()` 内、紧随 `buildBaseRunConfig` 返回之后;**值与库中相同不写** | `buildBaseRunConfig` 是纯构建器,还被 `ResolveRunConfig` 复用于 discuss、ask_user 恢复、工具审批恢复;给它加副作用会让恢复轮也写。它已返回 `chatModel`,写回在调用方做即可。实现分支是无条件 UPDATE,与原稿"值不变则不写"不一致 |
| 首发送对(D) | 首条消息在 WS 内建 session 时把对随 INSERT 写入(`createWSChatSession` 传参) | 前端首发实际走 **REST `createSession` 再开 WS**,不经 `createWSChatSession`。改为:前端 `createSession` 请求体携带对(仅当有显式来源),handler **先经 reconcile 再** INSERT;WS 内建路径保留同样处理作为兜底 | 原路径在真实流程中不触发,实现分支靠"session 行未加载则保留刚发的对"的特判撑住 P9′。行出生即有值,特判可删。INSERT 随写的值未经模型校验(校验原本在首轮 `selectChatModel` 才发生),必须先 reconcile |
| 解析链:session 记忆 vs subagent pin | §3.4"它自己的对 > pin" | 保持这个优先级,但要**修解析顺序**:现状 `applySubagentThreadDefaults` 在 `resolve` 内、`buildBaseRunConfig` 之前把 pin 填进 `req.Model`,请求省略字段时 pin 先占位,session 记忆永远查不到。改为 pin 只在 session 记忆为空时填 | 实现分支的解析侧与 §3.4 倒置 |
| 解析链:effort 记忆的位置 | 记忆当作 requested 传入 `ResolveConfig` | 记忆作为 `reasoning` 包 `pickEffort` 里 **stored 之上、requested 之下的新一级**,不冒充请求值 | `RunConfig.ReasoningRequestedEffort` 会传给 spawn 出来的子代理当"本轮显式选择";记忆走 requested 位会串到子代理 |
| `bot_history_messages.reasoning_effort` 列 | 新增,服务"存量 session 的历史对兜底" | **删除,不加** | 存量 session 在修复前从未记录过 effort,没有历史可兜;向前它只服务"逐条投影",而那是 §3.6 非目标。模型侧历史兜底 `GetLatestSessionModelID` 已存在,保留 |
| ACP 持久化 | 与 native 同两列;冷启动经 `applyPromptConfig` 回灌 | ACP 的对写进 session 的 **`runtime_metadata`**(`acp_model_id` / `acp_reasoning_effort`),不进两列;spawn 完成后按它覆盖 profile 默认 effort;`PATCH acp-runtime/model|reasoning` 端点同步双写 | ACP 模型 id 是 agent 命名空间字符串,进不了 `preferred_chat_model_id` 的 UUID 外键。P5 根因已定位:`client/session.go` 每次 spawn 都把 `profile.DefaultReasoningEffort` 应用一遍,Memoh 侧零持久化。ACP 配置本来就集中在 `runtime_metadata`,顺着结构走 |
| schedule / heartbeat 触发轮 | 未提 | **排除**:`SessionType` 为 schedule/heartbeat 的轮不读 session 记忆 | "记忆 > bot 默认"会让被 web 续聊过的 schedule session 在 payload 未带模型时改用记忆值,是未声明的行为变化 |
| fork | 未提 | fork 继承源 session 的对(实现分支已如此) | 同一对话的延续,打开时应长得一样 |
| 种子查询 | 按 `bot_id` | 加 `created_by_user_id` 条件 | 见 A.1 |

### A.2.1 判断依据(代码证据与被否决的备选)

以下每条对应 A.1 / A.2 表格中的一行,给出推理链、代码出处、以及考虑过但否决的做法。行号以 main 头 cd496786c 附近为准,会漂。

**A. "没选过"与"选过"被抹平(A.1 S3 / A.2 门=数据)**
- 推理链:① 原稿 §2 声称"没碰 picker 时前端整条 omit 字段是常态",实际不成立——`chat-pane.vue` 的 `initFromBotSettings` 在 botSettings 加载时总把 `overrideModelId/overrideReasoningEffort` 填成 bot 默认(≈2090–2100),发送时 `sentModelId = overrideModelId.value` 原样进 WS payload(≈3103,`send.ts:279–283`),所以 native 每条消息几乎都显式携带对;② 原稿因此规定"写解析结果而非请求原值",两者叠加的结果是:每个 web session 首发时无论有没有选,都被写进当时的 bot 默认;③ 之后管理员改 bot 默认,读链"记忆 > bot 默认"让这些 session 永远读到旧值。§5 把它记为"已接受怪味",但它违反第⑧幕同一批用户在 web 侧"没选就用默认"的预期。
- 否决的备选:保留"首发即快照"但写回时排除 bot 默认值——服务端无法区分"请求里的 bot 默认"是用户点选还是前端自动填充,信息在前端就丢了。唯一干净的做法是前端按来源决定携带/省略。

**B. 门=入口不成立(A.2 第 1 行)**
- 证据:实现分支 `21917f949` 的 `ChatRequest.PersistModelPreference` 只在 `service_stream.go StreamChatWS` 置位;retry/edit 在 `service_retry_edit.go` 走 `streamChatWSResultWithHooks`(≈249),不经 `StreamChatWS`,已漏。
- `CurrentChannel` 不可作门:WS 发送 `CurrentChannel: h.channelType.String()` 为 `"web"`(`local_channel.go:2195/2259`),retry/edit 写死 `"local"`(`service_retry_edit.go:81,124`),旧 REST `web/messages` 经渠道 inbound 进来也是 `"web"`,schedule/heartbeat 为空。
- 渠道被结构排除的依据:`inbound/channel.go:1196–1201` 仅在 `msg.Metadata["model_id"/"reasoning_effort"]` 存在时填 `cmd.Model/ReasoningEffort`;全仓唯一写这两个 metadata 键的是 `local_channel.go:849–861`(web 的旧 REST handler)。IM 平台适配器不写,故渠道请求"从不携带对"是结构事实而非约定。
- 否决的备选:在 `ChatRequest` 加 `json:"-"` 入口标志并在 handler 置位(先例 `SkipTitleGeneration/ForceFreshRuntime`,`contract.go:56–71`)。可行,但在"请求携带对才写"之下是冗余的第二个门,且每新增一个 web 入口都要记得置位。

**C. 写回位置(A.2 第 2 行)**
- 证据:`buildBaseRunConfig`(`service.go:750`)有两个调用方:`resolve()`(`service.go:386`)和 `ResolveRunConfig`(`service.go:1170`);后者服务 discuss(`turn_discuss.go:461`)、ask_user 恢复(`service_user_input.go:363`)、工具审批恢复(`service_tool_approval.go:372`),且不带请求模型/强度。原稿"全仓唯一调用点"不成立。函数已把 `chatModel` 作为返回值交给调用方,写回放在 `resolve()` 里零成本。
- 否决的备选:仿 workspace target 在轮末写(`persistSessionWorkspaceTarget`,`service_store.go:405`,调用点 `step_commit.go:172`/`service_stream.go:730`)。否决理由:P7′ 要求"请求被接收即记、不论当轮成败",轮末写会让失败轮丢对;且该先例是整 `metadata` JSONB 读改写,有丢更新风险,两列 UPDATE 不需要继承这个形态。

**D. 首发路径(A.2 第 3 行)**
- 证据:前端草稿升格走 `acp-sessions.ts ensureChatViewSession` → REST `createSession`(≈264)→ `promoteDraftView`,然后才 WS 发送;`local_channel.go:2117–2141` 的 WS 内建 `createWSChatSession` 只在 `sessionID == ""` 时触发,真实前端流程不走这里。实现分支 `b1113cb24` 靠"`activeSession` 行未加载则保留刚发的对"特判(chat-pane 新增 watch 中 `if (!sess || sess.id !== sessionId) return`)维持 P9′。
- 校验缺口:模型合法性在 `fetchChatModel`/`validateSelectedChatModel`(`service_model_selection.go` ≈113–126)首轮才跑;INSERT 随写请求原值会让 session 记住坏对。实现分支已有 `PatchSessionModelPreference` 的 reconcile,REST `CreateSession` 复用它即可。
- 广播时序(支持"行诞生即有值"的必要性):`thread.Service.Create` 单条 INSERT 后**同步**调 `publishThreadCreated`(`thread/service.go:672–693`),hub 同步遍历订阅者(`event/hub.go:121–136`);WS 端 `createWSChatSession` 返回后立即 `SendJSON(session_created)`(2141),都早于首轮 `resolve`。任何"首轮再补写"的方案都赶不上广播。

**E. subagent pin 优先级(A.2 第 4 行)**
- 证据:`resolve()` 先 `applySubagentThreadDefaults`(`service.go:384` → `subagent_thread.go:27–58`),它在 `req.Model` 为空时从 `subagent_configs` 填入 pin;之后 `buildBaseRunConfig` 里 `selectChatModel` 看到 `req.Model` 非空直接用。实现分支的 `sessionPrefModelID` 只在 `modelID == ""` 分支生效,故有 pin 的 session 永远查不到记忆。

**F. effort 记忆的位置(A.2 第 5 行)**
- 证据:实现分支把 `sessionPrefEffort` 赋给 `requestedEffort` 再进 `resolveReasoningConfig`;同一值经 `RunConfig.ReasoningRequestedEffort`(`service.go` ≈836,`native/types.go` ≈97–102)传给 spawn 子代理(`spawn_adapter.go` ≈173),子代理按自身模型把它当"用户本轮显式选择"重解析。`reasoning/resolve.go ResolveConfig` 已有 stored/requested 两级,记忆应是 `pickEffort` 里 stored 之上的独立一级。

**G. 删 history effort 列(A.2 第 6 行)**
- 证据:`bot_history_messages` 现有 `model_id`(`0001_init.up.sql:617`)无 effort;修复前从未有任何路径记录 effort,存量数据不可能有值可兜。模型侧兜底 `GetLatestSessionModelID`(`session_info.sql:17–23`)已存在。原稿 §3.6 已把"逐条投影"列为非目标,该列失去唯一的向前用途。顺带发现:该查询无 team_id 过滤,独立问题。

**H. ACP(A.2 第 7 行)**
- P5 根因证据链:`acp/profile/profile.go` 中 codex `DefaultReasoningEffort: "medium"`(≈213)、claude-code `"high"`(≈251);`session_pool.go:1617` 把它放进 `StartRequest`;`acp/client/session.go:403` 在**每次 spawn** 后无条件 `SetReasoningEffort(默认)`。picker 的选择只经 `PATCH acp-runtime/reasoning` 打到活体进程,Memoh 侧无任何持久化;进程重建即回退。
- 不能进两列的证据:ACP 模型 id 是 agent 命名空间字符串(`acpRuntimeModelRequest.ModelID`,`acp_runtime.go:55`;schedule 亦区分 `ModelID` 与 `ACPModelID`,`schedule/execution.go:51,58`),`preferred_chat_model_id` 是 `models(id)` UUID 外键。
- 选 `runtime_metadata` 的依据:ACP 会话配置(`acp_agent_id`/`project_path`/`acp_project_mode`/`runtime_owner_account_id`)已全部集中在此(`thread/service.go:1602–1604 mergeACPMetadata`),读侧 `service_acp.go:76,115` 已按此取值。否决 `acp_session_states`:它是 JSONL 转录快照的索引表(迁移 0138),按 run 版本化,不是会话配置的家。

**I. schedule/heartbeat(A.2 第 8 行)**
- 证据:`service_trigger.go:66–76` 触发轮以 `payload.ModelID/ReasoningEffort` 进 `ChatRequest`,payload 为空时今天落到 bot 默认;插入"记忆"一级后,曾被 web 续聊过的 schedule session 会改用记忆值。`SessionType` 在 `ChatRequest` 上可判(`sessionmode.Schedule`),排除成本一行。

**J. 前端结构选 ChatViewEntry(A.3 前端)**
- 现状证据:对是 chat-pane 组件三个 ref(≈1008–1012);pane 身份是 dockview panelId;repoint 只清 `userPickedModel` 不清 override(≈2138–2140)——这同时是 P2 串值的根因和 P9′ 不闪回的依赖,同一机制正反面;store 层 `chat-list.ts:185–186` 同名 ref 仅在 reset 处被清空,无读方,是死代码。
- 同构先例:workspace target 已是 per-view 状态(`view-registry.ts:28–30`),带 `unset/default/session/user` 来源标记与 `initializeWorkspaceTargetSelection` 的"user 不被 default 覆盖"规则(`views.ts:236–283`),promoteDraft 时随 view 迁移。对需要的正是"来源"这个维度(决定携带/省略),搬进去可复用整套规则并删掉 chat-pane 里的播种/重置 watch 群。
- 代价与 P8′ 的关系:同一 session 的多个 dockview tab 共享一个 ChatViewEntry,故同浏览器内会同步显示,与原稿"各显示各的"不同;跨浏览器/设备仍是最后发送者赢。这是有意的边界变化,已在 A.1 标注需确认。

**K. 种子按用户(A.1 第 4 行)**
- 证据:实现分支 `GetLatestSessionModelPreference` 只有 `bot_id` 条件;`bot_sessions.created_by_user_id` 已存在并被 `canAccessSession`(`handlers/session.go:882–891`)用于访问控制,加条件零结构成本。

### A.3 最终架构

**存储。** `bot_sessions.preferred_chat_model_id`(UUID,FK models,`ON DELETE SET NULL`)+ `preferred_reasoning_effort`(TEXT),成对写、成对清;NULL = 无记忆。ACP session 两列恒 NULL,其对在 `runtime_metadata`。不加 history effort 列。

**读链(每轮,native)。**
- 模型:请求携带 > session 记忆 > subagent pin > bot 默认 > 历史消息最新 `model_id`。
- 强度:请求携带 > session 记忆 > bot 存储值 > 模型默认档;经 `reasoning.ResolveConfig` 对当前模型 reconcile,非法档落模型默认档。
- schedule/heartbeat 轮跳过"session 记忆"一级。ACP 轮不经此链。

**写点(三个,全部经 reconcile,DB 永不存非法对)。**
1. picker 切换 → `PATCH /bots/:bot/sessions/:id` 带对,乐观显示,失败静默。
2. 首发 → 前端 `createSession` 请求体带对(有显式来源时),INSERT 随行;行诞生即有值,`session_created` 广播携带的行已完整,P9′ 无窗口。
3. 每轮 → `resolve()` 内,条件 = 请求携带对 **且** 与库中不同。渠道请求从不携带,结构上永不命中。
4. 渠道 `/model` `/reasoning` → 命中有记忆的 session 时清空两列。

**前端。** 对从 chat-pane 组件的三个 ref 搬到 `ChatViewEntry`,与 workspace target 同构:`pairModelId` / `pairEffort` / `pairSource ∈ {unset, default, session, user}`。
- 播种:打开 session → `session`(有记忆)或 `default`(bot 默认 / subagent pin);welcome → 草稿 > 种子端点 > `default`。
- 发送携带规则:`source ∈ {user, session}` 携带;`default` **省略**。这一条决定了服务端能否区分"选过"和"没选"。
- 换模型:整个对换新,强度落新模型默认档,`source = user`。
- promoteDraft 时随 view 迁移(现有机制),首发即清 localStorage 草稿(键按 bot)。
- 同 session 多 tab 共享 view → 同步显示(A.1 P8′ 变更)。
- 删除:store 层 `chat-list.ts` 同名 `overrideModelId/overrideReasoningEffort` 死代码;chat-pane 内原播种/重置 watch 群。
- 列表刷新竞速:PATCH 在飞期间 `patchSessionInList` 更新本地行,`replaceSessions/rememberSession` 整体覆盖前先合并两字段。

**ACP。** picker 切换 → `PATCH acp-runtime/model|reasoning`:活体进程 `session/set_*` + `runtime_metadata` 双写;spawn/resume 完成后读 `runtime_metadata` 覆盖 profile 默认;agent 在线自报为真相并回填。

**welcome 种子端点。** `GET /bots/:bot/sessions/model-preference-seed`:该 bot、该用户、native(`runtime_type='model'`, `type='chat'`, `visibility='user'`)、最近一条两列非 NULL 的 session。

### A.4 行为变化清单(QA 基线需标注)

- P6′:换模型不再保留强度(现状保留)。
- 显式选过的 session 脱离 bot 默认;从未选过的 session 继续跟随 bot 默认(与现状一致)。
- 渠道 `/model` `/reasoning` 在有 web 记忆的 session 上多一个"清空"动作,渠道侧确认文案不变。
- 同一浏览器内同 session 多 tab 的 picker 同步显示。
- retry/edit 也参与写回(实现分支未覆盖)。

### A.5 对实现分支的调整点(已并入正文 §6,此处保留原始记录)

后端 `21917f949`:删 `PersistModelPreference` 标志与 history effort 列;写回移出 `buildBaseRunConfig` 到 `resolve()`,加"携带且不同才写";`applySubagentThreadDefaults` 前先查记忆;effort 记忆改进 `pickEffort` 新一级;schedule/heartbeat 排除;REST `CreateSession` 收对并 reconcile;种子查询加用户条件;渠道命令清空。
前端 `b1113cb24`:对搬入 `ChatViewEntry` 并带来源;发送按来源决定携带/省略;删"行未加载则保留"特判;删 store 死代码。
新增:ACP `runtime_metadata` 双写与 spawn 覆盖。

### A.6 未验证

本节全部基于静态阅读,未跑代码、未复现 §1.1 任何一条。§4 三层验证按 v2 正文重跑。
