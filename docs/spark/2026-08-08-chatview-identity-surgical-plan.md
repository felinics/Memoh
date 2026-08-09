# ChatView 身份贯通 · 外科手术方案

> 日期：2026-08-08
> 前身：《ChatView 同步层重构》（2026-07-14，PR #830，已 closed 未执行）。旧文档的愿景 §0 与状态机预演 §9a 仍然有效，本文是它的**外科手术版**：只切病灶，不碰健康组织。
> 现状基线：`main@6b2db3f40`（#865 durable session runtime 已合并之后）。

## 病根（一句话）

一条消息在它的一生里被换了三次名字：发送占位时前端起一个、直播（runtime snapshot）里另一个、落库后数据库 UUID 又一个。**没有任何字段记录"这三个名字是同一条消息"**。前端靠名字认消息，名字对不上只有两种结果：当成两条（双份显示），或当成不存在（prompt 消失又复现）。答完瞬间名牌全换导致组件重挂载（"抖一下"）。

旧 Plan 的药方没变：**终身身份证 + 座次号，诞生时发，一路不换，落库照抄；前端按号点名。**

## 与旧 Plan 的关系：哪些已落地、哪些过时、哪些仍欠

#865 之后现状评估（详细取证见三路 scout 报告，2026-08-08）：

已落地（**不做**）：
- 后端双权威 + fenced 原子移交（`internal/agent/runtime/session/`：admit claim 后激活 fence，finalize 带 token 落库，先存档再撤直播）。
- turn_id 终身稳定（admission 时发放，全程不变）；turn_position admission 时由 Postgres 计数器预留（`session_runs.sql:38-45`）。
- legacy WS 流事件分支、`isSameLogicalTurn` 文本+5s 猜号、`reconcilePersistedRuntimeReplacement` 位置猜测：已全部删除。
- 前端 god-file 已拆（chat-list.ts 2913→600 行），事实同步层存在（runtime-layer / runtime-projection / runtime-integration / decisions）。
- 交接的后端时序（先存档再撤直播）：正确，**一行不改**。

已过时（旧 Plan 表述需修订）：
- "Redis Stream"：重写后无 Streams，是 snapshot JSON blob + lease ZSET + pub/sub。
- `packages/sdk/session-runtime/` 纯 reducer 从未进 main；等价物是 Go state machine + `acceptance/` 黑盒套件。
- 旧 Plan §10.9 "轮内 runtimeRowTracker"：**不存在**，轮内 seq 仍是落库时 SQL `COALESCE(MAX+1)` 现场编号。

仍欠（本方案的手术范围）：
- 消息/块级终身号：运行态只有 int 流内下标（`view/uimessage.go:41`），落库才换 UUID。
- 座次号零暴露：REST / WS / SDK grep `turn_position|turn_message_seq` 零命中。
- 交接实质：前端终态刷新仍是 `replaceMessages` 整屏替换、名牌全换；保名牌的桥 `adoptRenderIdentity`（`transcript-history.ts:125-137`）是死代码（认 `serverId`，无生产写入者）。

## 目标与症状对应

| 症状 | 机制 | 手术切口 |
|---|---|---|
| 双 text | snapshot 与 run_accepted 两路无序，占位轮未盖 turn_id → 投影追加第二份（`transcript.ts:461-473`） | 切口 3 |
| prompt 消失又复现 | 初始/终态 `replaceMessages` 全量覆盖丢弃未落库占位；user 轮无复活路径（`runtime-integration.ts:406-424`） | 切口 2 |
| 答完抖一下 | 落库名单用行 UUID 当渲染 key（`transcript-history.ts:94-97`），key 变 → 组件重挂载 | 切口 1 |

## 第一期：止血（前端为主，后端只加一个字段）

**不动落库路径、不动发号逻辑、不动 DB。**

### 切口 1：名牌终身稳定

- 每个"轮"组件的渲染 key 在出生时由前端生成，终身不换；turn_id 到达后挂到该 key 名下当身份证号，名牌不动。
- 落点：`transcript-history.ts:94-97`（落库名单不再用行 UUID 当 key）、`transcript.ts:407-421`（bindRuntimeTurn 只盖章不换牌）、修活 `adoptRenderIdentity`（认 turn_id 对号，保旧名牌）。
- 不做：不碰组件内部结构；滚动层赔偿 watch（`useChatScroll.ts:1046-1078`）先留着，验证不抖后再拆（第三期）。

### 切口 2：交接改按 turn_id 逐条点名

- `replaceMessages` 整屏覆盖（`runtime-integration.ts:334/398`、`transcript.ts:161-268`）改为三方对账：
  - 档案有、屏幕也有（turn_id 对上）→ 换内容，名牌不换；
  - 档案有、屏幕没有 → 按 turn_position 插到正确位置；
  - 屏幕有、档案没有（正在生成的轮 / 未落库占位）→ 保留，不许擦；确认整轮未落库（可见输出前停止）→ 移除并把用户输入回填草稿框。
- 删除特例 `reattachTurnToSession`（只复活 assistant 不复活 user）：对账上线后由"保留"规则天然覆盖。
- **后端配套（唯一一处）**：REST 历史响应每个 turn 加 `turn_position`（列、视图现成，只是不返回）。纯增量字段，无兼容风险。

### 切口 3：赛跑收进缓冲区

- `applyRuntimeTranscript` 按 turn_id 找不到绑定时不再直接追加：进按 turn_id 排队的缓冲区，等 run_accepted 的 invocation→turn_id 映射到达再合并；短超时未到（另一台设备发起的轮，无本地占位）→ 以 turn_id 为名直接渲染。
- 落点：`runtime-integration.ts`、`runtime-projection.ts`。
- 不做：不改 WS 通道结构，不要求后端保序——前端消化乱序，确定且无文本猜测。

### 第一期验收

1. 慢网/注错制造 run_accepted 晚于 snapshot → 全程无双份；
2. 发送瞬间刷新/切会话 → prompt 不消失；
3. 答完瞬间 DOM 断言：turn 组件 key 不变、无重挂载；
4. 回归：retry/edit/fork、审批/ask_user 续轮、ACP、分页加载；
5. vitest 全绿。

## 第二期：除根（后端发号贯通到块级）

1. 运行态每条消息诞生时发 UUID + 轮内序号（替代 int 下标）；落库原样照抄进行主键与 `turn_message_seq`，删 runtime 路径落库 SQL 的 `MAX+1` 现场编号（`sqlc/messages.sql.go:44-45`）。现有 `(team_id, turn_id, turn_message_seq)` 唯一索引升格为发号错误的最后防线。
2. 直播帧与档案清单每条 message 带终身号 + 轮内序号；SDK 重新生成；WS 契约收进 swagger/SDK 单源（治 Go struct + 前端手写双拷贝的漂移隐患）。
3. 前端维护行级账本，渲染块保留行 provenance；交接对账细化到行级（retry/edit 精确锚定、跨设备逐行去重、部分轮交接不重摆）。

## 第三期：清创（观察一个迭代后）

- 删 `useChatScroll` 残余赔偿、无调用者的死代码（如 `assistant-streams.ts` 的 `mapAssistantStreamMessage`）。
- 明确不碰：`next_turn_position` 计数器、fence、`session_runs`、`sync/` 目录更名、ESLint 守卫、god-store 拆分。

## 合并顺序与兼容

- 第一期：后端 REST 加 `turn_position` 先合（纯增量）；前端三个切口可并行。
- 第二期：发号 → wire/SDK → 前端账本，严格按序，契约先行。
- 所有 wire/DB 改动均为增量字段，无 flag、无双写窗口，新老前后端互连不炸。

## 最大风险点

切口 2 的对账规则是唯一新引入的判断逻辑。"保留 vs 移除"的边界（生成中 / 已落库 / 整轮未落库）必须写成状态机表并配齐测试——否则只是新增一个写入者。旧 Plan §9a 已预演该状态机，直接搬。

## 实施补记（2026-08-09，第一期落地）

与原方案的三处偏差，记录备查：

1. **`reattachTurnToSession` 保留，未删除。** 原计划认为对账"保留"规则上线后它成为死路。实施中保留作安全网：`runtime-integration.ts` 的 user 轮复活路径仍调它，防御"对账判定移除、但轮实际仍存活"的边界漏判。待第一期在生产观察一个迭代、确认对账无漏后，第三期清创时再删。
2. **草稿回填延后。** 切口 2 中"确认整轮未落库 → 移除并把用户输入回填草稿框"的回填半句本期未做：对账只管"保留 vs 移除"，不回写 composer。回填涉及 composer 状态所有权，单独开任务，不掺进止血。
3. **保留判定不认 `streaming` 标志。** 实测发现失败轮的 `streaming:true` 会永久残留（僵尸轮），整屏替换时把它当活轮保留、顶掉落库真身。最终判定收敛为：无 turnId 只看 `__optimistic === true`；有 turnId 只看 `isTurnLive(turnId)`（runtime 运行态是否仍持有该轮）。

另：切口 3 的赛跑缓冲区落在独立模块 `apps/web/src/store/chat/runtime-slice-buffer.ts`（transcript.ts 触 max-lines 红线抽出），语义与方案一致：按 turnId 排队、750ms 绑定宽限、超时 standalone 落屏。
