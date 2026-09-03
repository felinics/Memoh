# 会话输入队列设计

分支 `codex/steer-followup-queue-20260827`，2026-09-02。

## 1. 背景

Memoh 的每次模型调用是一个 run，归属于一个会话，由 `session_runs` 记录状态、owner 与 fencing token。原有交互中用户在 run 进行时无法追加输入。队列功能补上两种情况：

- **steer**：绑定到接受时的活跃 run，在下一个 step 边界作为 user 消息注入模型上下文。run 结束后未消费的 steer 被拒绝。
- **follow-up**：在 run 活跃时接受，当前 run 到达最终边界后按 FIFO 取出第一条创建 continuation run。多条 follow-up 形成 R0 → R1 → R2 的链。

两类输入的所有权和交接规则不同，因此在 Go 类型、SQL 表和 API 上保持分离，没有混合的队列条目类型。

### 1.1 约束

| 约束 | 内容 |
|---|---|
| 多实例 | Memoh-cloud 多进程部署。run 的 owner lease、live snapshot、跨实例控制命令由 Redis 会话运行时承担；任何持久化状态必须能在 owner 失联后由另一实例恢复 |
| 租户模型 | 用户与 team 多对多，每个用户至少一个 team，小 team 占多数。所有业务表带 `team_id` 并启用 FORCE RLS |
| 崩溃一致性 | 每个模型 step 的历史写入与队列状态变更在同一事务提交，重放不得二次消费 |
| 输入速率 | 两类输入均由人在 run 进行中手动提交，单会话速率受输入速度限制 |

### 1.2 Redis 在本功能中的角色

队列的读、写、claim、continuation 全部在 PostgreSQL。Redis 只服务会话运行时：

| 职责 | 作用 |
|---|---|
| Live snapshot | run 进行中的实时投影，供 WebSocket 重连和跨实例 attach |
| Owner lease | run 归属租约，reaper 据此判定 owner 失联 |
| Pub/Sub | 运行时事件推送（含 steer 的 claimed/applied 状态）与跨实例控制命令 |
| History reset fence | 历史重置期间的跨实例互斥 |

## 2. PR 改动

原分支在 PostgreSQL 队列之前放置了一层 Redis 准入分批：请求先写 Redis stream，worker 按会话分组合并进 PostgreSQL 事务，结果经 Pub/Sub 返回。该层约 2000 行生产代码、1300 行测试、15 个配置项，且只要会话运行时 backend 为 Redis 就强制启用。本次 PR 移除该层，入队统一为一次 PostgreSQL 事务。

### 2.1 调整后的三条路径

```
写路径   Web ──(invocation_id 重试)──► Handler ──► Service ──(InTx, 1 条 INSERT)──► PostgreSQL
                                        JWT / 会话行 / 权限                          会话行锁 + 唯一索引

读路径   Web ──(事件驱动, 10s 兜底)──► Handler ──► Service ──(1 条 UNION 查询)────► PostgreSQL
                                        3 条鉴权 SQL                                 2 个部分索引

运行时   每个模型 step ──► CommitStep 事务：锁会话 → 读 step 日志 → 锁 run → apply/claim → 写 step 日志
```

### 2.2 提交列表

| 提交 | 范围 |
|---|---|
| `e6d1d2a19` refactor(queue) | 移除 Redis 准入分层与 15 个配置项；入队改为单行语句；handler 只保留鉴权与映射，事务下沉到 application service；continuation 等待会话 live slot；steer 历史行按投递路径判定归属；单次往返读取两队列；鉴权收敛为一次 bot 行查找；删除无调用方的原语与迁移残留 |
| `780b736d0` feat(web) | 队列面板从每秒轮询改为响应运行时事件刷新，兜底 10 秒；reorder 保留另一队列类型；使用生成的 SDK 类型；steer turn 前缀收敛为常量 |
| `806251554` test(queue) | store 层读写 benchmark、CommitStep benchmark、读/写闭环压测、HTTP 端到端压测，默认跳过 |

### 2.3 两处正确性修正

1. **continuation 等待目标。** 原实现等待 `EnqueuedDuringRunID` 对应的 run 释放。该字段是准入来源而非交接父 run：在 R0 期间入队的第二条 follow-up 由 R1 交接，等待 R0 会立即返回，此时 R1 可能仍占用 live slot。改为 `WaitSessionSlotFor`，等待会话 slot 空闲或已被自身占用。同一 continuation 的并发启动去重。
2. **steer 历史行归属。** 原实现用文本相等判断 steer 是否已被普通 step 回调持久化，两条内容相同的 steer 会误判。改为按投递路径判定：经 InjectCh 注入的由 step 捕获持久化，经 NextModelInputs 重开 run 的由 apply 回调持久化。

## 3. 迁移

迁移 `0146_session_input_queues` 新增三张表和一列，`0001_init.up.sql` 同步更新。本次 PR 删除了原分支为 Redis 路径引入的 `ingress_sequence` 列，以及 0145 至 0147 合并进来后残留的 DROP INDEX 语句。

### 3.1 对象

| 对象 | 说明 |
|---|---|
| `session_steer_queue` | 主键 item_id；`target_run_id` 外键指向 session_runs，永久绑定接受时的活跃 run；status 六态；claim 三列受 CHECK 约束，status 为 claimed 时三列必须同时非空 |
| `session_follow_up_queue` | 同上结构，另有 `enqueued_during_run_id`（准入来源，不可变）与 `assigned_run_id`（最终交接时写入，指向 continuation run） |
| `session_queue_step_commits` | 主键 (team_id, run_id, step_index)；记录 commit_hash、action 与结果条目 id；CHECK 约束 action 与结果列的组合 |
| `session_runs.source_follow_up_item_id` | continuation run 的来源条目；部分唯一索引保证一条 follow-up 只产生一个 run |

### 3.2 索引

两张队列表各有 (team_id, session_id, invocation_id) 唯一索引承担幂等，以及按 status 过滤的部分索引覆盖 FIFO 读取与 claim 路径。读路径命中两个 `pending_order` 部分索引，条件分别为 `status = 'accepted'` 与 `status = 'accepted' AND assigned_run_id IS NULL`，EXPLAIN 确认为 Index Scan。

### 3.3 RLS 与 NOT VALID 外键

三张表启用 FORCE ROW LEVEL SECURITY，策略为 `team_id = memoh_current_team_id()`。该函数在 `memoh.team_id` 未设置时抛错。`session_runs` 到 `session_follow_up_queue` 的外键以 `NOT VALID` 创建：

```sql
ALTER TABLE public.session_runs
    ADD CONSTRAINT session_runs_source_follow_up_item_fkey
    FOREIGN KEY (team_id, source_follow_up_item_id)
    REFERENCES public.session_follow_up_queue(team_id, item_id)
    ON DELETE RESTRICT
    NOT VALID;
```

原因：普通外键创建时扫描已有数据，扫描 FORCE RLS 表会调用 `memoh_current_team_id()`，迁移角色没有 team context 时失败。NOT VALID 跳过历史扫描，新增和更新的行仍受约束。该列在本迁移内新建，历史值全为 NULL，验证结果恒为真。仓库中 0129 的 `bot_sessions_workdir_id_fkey` 使用相同写法，属于既定处理方式。已在 dev 库以超级用户执行 `VALIDATE CONSTRAINT` 成功。

迁移末尾以 DO 块尝试 `VALIDATE CONSTRAINT`，捕获 `insufficient_privilege` 后跳过。有 team context 的角色（超级用户、开发环境）在迁移内直接得到已验证状态；受限角色保持 NOT VALID，迁移不失败。已在 dev 库分别以受限角色与超级用户验证：前者输出 NOTICE 且 `convalidated` 为 false，后者为 true。

### 3.4 回滚

down 迁移按逆序删除外键、索引、列与表。已在 dev 库执行 down 再 up 往返，无残留列。

## 4. 查询

### 4.1 入队

单条语句完成锁定、校验、分配位置与插入。会话行 `FOR UPDATE` 让同会话并发入队串行化，position 由锁内 `max + 1` 分配。活跃 run 在同一语句内重读，bot 处于 deleting 状态时不接受。

service 层以自动提交执行这条语句，不再包裹显式事务。单条语句本身原子，去掉 BEGIN 与 COMMIT 两次往返不改变语义。service 层另有进程级准入门限：同时执行中的入队语句上限 64（约连接池的 2 倍），超出立即返回 `ErrAdmissionOverloaded`，HTTP 映射为 429，客户端用同一 invocation_id 重试。

```sql
WITH locked_session AS MATERIALIZED (
  SELECT s.id FROM bot_sessions s
  JOIN bots bot ON bot.team_id = s.team_id AND bot.id = s.bot_id
  WHERE s.team_id = memoh_current_team_id() AND s.id = $session_id
    AND s.bot_id = $bot_id AND s.deleted_at IS NULL AND bot.status <> 'deleting'
  FOR UPDATE OF s
), active_run AS MATERIALIZED (
  SELECT run.run_id FROM session_runs run
  JOIN locked_session s ON s.id = run.session_id
  WHERE run.state IN ('accepted','running','waiting_decision') LIMIT 1
), base AS MATERIALIZED (
  SELECT COALESCE(max(position), 0) + 1 AS position
  FROM session_steer_queue WHERE session_id = $session_id
)
INSERT INTO session_steer_queue (item_id, team_id, bot_id, session_id,
  target_run_id, invocation_id, payload, position)
SELECT $item_id, memoh_current_team_id(), $bot_id, $session_id,
  run.run_id, $invocation_id, $payload::jsonb, base.position
FROM active_run run CROSS JOIN base
ON CONFLICT (team_id, session_id, invocation_id) DO NOTHING
RETURNING *;
```

无返回行有两种含义，由 Go 侧区分：按 invocation_id 查到已有条目且 payload 相同，返回该条目（重试幂等）；payload 不同返回 `ErrInvocationConflict`；查不到则没有活跃 run，返回 `ErrNoActiveRun`。pg_stat_statements 记录该语句平均执行 0.32 毫秒。

### 4.2 列表读取

UI 面板一次刷新只发一条 SQL。两个 CTE 各走部分索引并限制 200 条，UNION ALL 后由 `queue` 列区分行形状，Go 侧拆回两个独立类型。同一语句快照，两队列之间无时间差。

```sql
WITH steer AS (
  SELECT 'steer' AS queue, item_id, bot_id, session_id,
         target_run_id AS run_id, payload, status, position, created_at
  FROM session_steer_queue
  WHERE session_id = $sid AND status = 'accepted'
  ORDER BY position, item_id LIMIT $max
), follow_up AS (
  SELECT 'follow_up', item_id, bot_id, session_id,
         enqueued_during_run_id, payload, status, position, created_at
  FROM session_follow_up_queue
  WHERE session_id = $sid AND status = 'accepted' AND assigned_run_id IS NULL
  ORDER BY position, item_id LIMIT $max
)
SELECT * FROM steer UNION ALL SELECT * FROM follow_up;
```

### 4.3 Claim 与 apply

Claim 是单语句 CAS：取 target_run_id 匹配、status 为 accepted 的最早一条，校验 run 的 owner 与 fencing token 仍然有效，并要求该 run 当前没有其他 claimed 条目，然后写入 claim 三列。Apply 以 claim 三列与 run 状态为条件把 status 置为 applied。任一条件不满足返回零行，调用方得到 `ErrNotPending` 或 `ErrInvalidReference`。

### 4.4 CommitStep 事务

每个模型 step 结束后，协调器在一个事务内完成历史写入与队列状态推进：

| # | 语句 | 目的 |
|---|---|---|
| 1 | LockSessionForCommitReconciliation | 会话行锁，与入队、promotion 串行 |
| 2 | GetSessionQueueStepCommit | 命中则校验 commit_hash 后重放结果，不再执行下列语句 |
| 3 | LockSessionRunForAgentStepCommit | 按 owner 与 fencing token 锁 run，失配返回 ErrRunOwnershipLost |
| 4 | ApplySteerQueueItem（有待 apply 的 claim 时） | 把上一 step 注入的 steer 标为 applied，并在同事务写其历史行 |
| 5 | Persist 回调 | 写本 step 的 assistant 与 tool 消息 |
| 6 | ClaimNextSteerQueueItem | 命中则返回 ContinueWithSteer |
| 7 | FinalizeSessionRun、GetNextPendingFollowUpQueueItem、CreateContinuationFromFollowUp（仅最终边界） | 终结当前 run，取 FIFO 首条 follow-up 创建无主 continuation run |
| 8 | CreateSessionQueueStepCommit | 记录动作与结果条目，主键冲突时校验 hash |

工具循环 step 通常执行 1、2、3、5、6、8 共 6 条，实测单次 1.34 毫秒。Continuation 的 run id 与 turn id 由 follow-up item id 经 UUIDv5 确定性派生，重放最终边界不会产生第二个 run。

### 4.5 Promotion

把一条 follow-up 提升为当前 run 的 steer。它跨两张表读、插、取消，因此先显式取会话锁，再以 follow-up 的 item id 作为新 steer 的 id 插入并取消原条目。重试时按该 id 查到已存在的 steer 直接返回。

## 5. 返回

### 5.1 端点

| 方法与路径 | 成功响应 | 说明 |
|---|---|---|
| `POST …/steer-queue` | 202 steerQueueItemResponse | 请求体含 invocation_id 与 text |
| `POST …/follow-up-queue` | 202 followUpQueueItemResponse | 同上 |
| `GET …/queue` | 200 sessionQueueResponse | UI 使用的合并读取，每队列最多 200 条 |
| `GET …/steer-queue`、`GET …/follow-up-queue` | 200 items | 单队列读取，内部同一条 SQL |
| `PUT …/{queue}/reorder` | 200 items | item 与 before 两个类型化引用，before 为空表示移到末尾 |
| `PATCH …/{queue}/:item_id` | 200 item | 仅 accepted 状态可编辑 |
| `DELETE …/{queue}/:item_id` | 204 | 仅 accepted 状态可取消 |
| `POST …/follow-up-queue/:item_id/steer` | 202 steerQueueItemResponse | promotion |

### 5.2 响应形状

两种条目响应字段不同，没有混合的 kind 判别字段。text 由 payload 的 JSON 解出。

```json
// steer
{ "item_id": "uuid", "status": "accepted", "position": 3,
  "text": "…", "target_run_id": "uuid" }

// follow_up
{ "item_id": "uuid", "status": "accepted", "position": 1,
  "text": "…", "enqueued_during_run_id": "uuid" }

// GET …/queue
{ "steer": [ … ], "follow_up": [ … ] }
```

3 条 steer 加 3 条 follow-up 的合并响应约 980 字节。

### 5.3 错误码

| code | HTTP | 触发条件 |
|---|---|---|
| `queue_no_active_run` | 409 | 入队时会话没有活跃 run |
| `session_runtime.invocation_conflict` | 409 | 同一 invocation_id 携带不同 payload 重试 |
| `queue_item_not_pending` | 409 | 编辑、取消、重排、promotion 的目标已不是 accepted |
| `queue_admission_overloaded` | 429 | 进程内同时执行的入队语句超过上限，客户端以同一 invocation_id 重试 |
| `queue_request_invalid` | 400 | 参数缺失或 id 格式错误 |
| `queue_admission_unavailable` | 503 | service 未配置 |

原分支为 Redis 路径定义的 `queue_target_run_not_active` 与 `queue_continuation_lost` 已删除。

### 5.4 运行时投影与前端刷新

steer 被 claim 与 apply 时，协调器在事务提交后向会话运行时发布 `SteerTurnUpserts`，客户端通过 WebSocket 收到，无需请求列表接口即可显示状态变化。前端队列面板据此刷新：steer 状态变化、run 离开活跃状态、本地入队三者任一发生时拉取一次，另有 10 秒兜底定时器，队列为空时停止。

鉴权：manage 权限可操作 bot 的所有会话；chat 权限只能操作自己创建的会话，否则返回 404。权限从一次不带 runtime check summary 的 bot 行读取解析，同时判定两个级别。

## 6. 瓶颈分析

所有数据在同一环境测得：PostgreSQL 18 运行于 Docker，shared_buffers 128MB，synchronous_commit 为 on，宿主机 10 核，Go 客户端与数据库同机，无网络往返。压测为闭环，固定在途请求数，每级持续 5 到 15 秒。

### 6.1 读路径

store 层单独测量，200 会话各 3 加 3 条待处理，连接池 32：

| 版本 | 并发 8 | 并发 32 | 并发 128 | 单次耗时 |
|---|---|---|---|---|
| 两条查询 | 15545 req/s | 25233 req/s | 23389 req/s | 152 µs |
| 单条 UNION 查询 | 28415 req/s | 45257 req/s | 45452 req/s | 87 µs |

去掉一次往返后 store 层吞吐接近翻倍，饱和点在并发 32，由连接池决定。

HTTP 端到端，经过 Echo 路由、JWT 中间件、鉴权、service 与 JSON 序列化，100 会话，连接池 32：

| 并发 | 优化前 req/s | 优化后 req/s | 优化前 p50 | 优化后 p50 | 优化后 p99 |
|---|---|---|---|---|---|
| 8 | 4523 | 7045 | 1.37 ms | 1.04 ms | 2.61 ms |
| 32 | 7007 | 9719 | 3.35 ms | 2.31 ms | 13.8 ms |
| 128 | 7512 | 10514 | 15.1 ms | 11.3 ms | 24.6 ms |

"优化前"为单次往返查询与鉴权收敛之前的代码，两组之间只有这两项差别。

会话数放大到 2000、并发放大到 2048、连接池 64、每级 15 秒：

| 并发 | req/s | p50 | p95 | p99 | 最大 |
|---|---|---|---|---|---|
| 64 | 10321 | 2.5 ms | 8.0 ms | 203 ms | 1.01 s |
| 512 | 9507 | 49 ms | 74 ms | 249 ms | 855 ms |
| 2048 | 10235 | 192 ms | 269 ms | 378 ms | 962 ms |

会话数增加 20 倍，吞吐不变；并发增加只增加排队延迟。

每次请求执行 4 条 SQL：会话行、账户行、bot 行、队列列表。pg_stat_statements 记录四条平均执行时间之和约 0.17 毫秒，请求 p50 约 1 毫秒，数据库执行时间占请求时间约 17%，其余为往返、Go 处理与序列化。SQL 数从优化前的 6 条减少到 4 条，原因是鉴权不再对 manage 与 chat 各做一次完整授权，也不再读取 bot 的 runtime check summary。

### 6.2 写路径

10 万次入队，100 会话，连接池 32。原分支代码从 HEAD 检出到临时 worktree，与新代码在同一台机器、同一个 PostgreSQL 实例上对照：

| 方案 | 在途 | req/s | p50 | p95 | p99 | 错误 |
|---|---|---|---|---|---|---|
| 直连 PostgreSQL | 64 | 7266 | 7.9 ms | 12.9 ms | 22 ms | 0 |
| 直连 PostgreSQL | 256 | 6806 | 34 ms | 54 ms | 90 ms | 0 |
| 直连 PostgreSQL | 2048 | 6508 | 293 ms | 433 ms | 529 ms | 0 |
| Redis 分批（原分支） | 64 | 5295 | 9.6 ms | 18.5 ms | 49 ms | 0 |
| Redis 分批（原分支） | 256 | 3488 | 45 ms | 260 ms | 288 ms | 0 |
| Redis 分批（原分支） | 2048 | 未完成 | | | | 6 次 Redis i/o timeout |
| Redis 分批，batch 512 / 5ms / 并行 32 | 64 | 2324 | 23 ms | 41 ms | 215 ms | 0 |

Redis 分批层在返回回执前仍等待 PostgreSQL 提交，数据库提交速率仍是上限，而它自身增加了 Redis 往返、结果键写入和 Pub/Sub 等待。在同一 PostgreSQL 前放置该层使吞吐下降 27% 到 49%，在途请求增大时先于直连路径失败。它默认把最多 8 个会话合并进一个事务，扩大了会话行锁的持有范围，会阻塞这些会话的 CommitStep。

直连路径的上限构成：入队语句本身平均 0.32 毫秒；关闭 synchronous_commit 后吞吐从 7266 升到 10481 req/s，WAL fsync 约占上限的 30%；其余为 BEGIN 与 COMMIT 的两次往返和单机 CPU 争用。

去掉显式事务后的对照，同一进程内交替运行 3 轮，每轮 5 万次入队，在途 64：

| 轮 | 显式事务 req/s | 自动提交 req/s | 显式事务 p50 | 自动提交 p50 |
|---|---|---|---|---|
| 1 | 5855 | 8726 | 9.3 ms | 6.7 ms |
| 2 | 6601 | 7346 | 9.2 ms | 7.9 ms |
| 3 | 6546 | 4104 | 9.1 ms | 6.5 ms |

p50 稳定下降约 25% 到 30%，与省去两次往返一致。吞吐在前两轮提升 11% 到 49%，第三轮自动提交出现一次 1.2 秒的尾部样本拉低了整体吞吐；本机同配置多次运行的吞吐波动本身约 20%。

上表在累积了 270MB 数据、未清理的表上测得，尾部受表膨胀与 checkpoint 时机干扰。以下为受控复测：每轮前清空两张队列表并执行 CHECKPOINT 与 VACUUM FULL，压测期间每 500 毫秒采样 `pg_stat_checkpointer`、`pg_stat_progress_vacuum` 与后端 wait_event，记录每个超过 100 毫秒的请求时间戳并与采样对齐。每轮 5 万次入队，在途 64，连接池 32：

| 轮 | 方案 | req/s | p50 | p95 | p99 | p99.9 | 最大 | >100ms 请求数 |
|---|---|---|---|---|---|---|---|---|
| 1 | 显式事务 | 6881 | 7.9 ms | 14.0 ms | 31.6 ms | 190 ms | 222 ms | 87 |
| 1 | 自动提交 | 8562 | 7.0 ms | 10.5 ms | 17.5 ms | 37 ms | 47 ms | 0 |
| 2 | 显式事务 | 6474 | 9.0 ms | 14.4 ms | 25.8 ms | 140 ms | 258 ms | 65 |
| 2 | 自动提交 | 8521 | 6.9 ms | 11.5 ms | 19.1 ms | 31 ms | 208 ms | 3 |
| 3 | 显式事务 | 6346 | 8.9 ms | 15.0 ms | 25.1 ms | 211 ms | 351 ms | 116 |
| 3 | 自动提交 | 7187 | 7.9 ms | 16.1 ms | 26.3 ms | 41 ms | 212 ms | 3 |

受控条件下自动提交的 p99.9 从 140 到 211 毫秒降到 31 到 41 毫秒，超过 100 毫秒的请求数从每轮 65 到 116 个降到 0 到 3 个。前一组第三轮的 1.2 秒尾部样本在受控条件下未复现，归因于累积表膨胀期间的 autovacuum 或 checkpoint，未逐一确认。

**约 208 毫秒台阶的来源。** 所有慢请求的延迟集中在 206 到 222 毫秒，采样期间无 checkpoint 与 vacuum 活动，后端等待事件以 `LWLock:WALWrite` 为主，另有恒定 1 个 `IO:WalSync`。`wal_writer_delay` 默认值恰为 200 毫秒。两组对照：

| 配置 | 显式事务 >100ms | 自动提交 >100ms | 显式事务 p99.9 | 自动提交 p99.9 |
|---|---|---|---|---|
| 默认（sync on，wal_writer_delay 200ms） | 65 到 116 | 0 到 3 | 140 到 211 ms | 31 到 41 ms |
| synchronous_commit off | 131 到 282 | 2 到 9 | 175 到 198 ms | 21 到 22 ms |
| wal_writer_delay 10ms | 10 到 50 | 0 到 7 | 57 到 90 ms | 32 到 38 ms |

关闭 synchronous_commit 后台阶不消失，说明它不是单次 fsync 的耗时。把 wal_writer_delay 调到 10 毫秒后显式事务的慢请求数下降 50% 到 90%，p99.9 从约 200 毫秒降到 57 到 90 毫秒。结论：台阶来自 WAL 写入路径上等待 WAL writer 周期性唤醒的排队，与 200 毫秒的 `wal_writer_delay` 对应；自动提交因为每请求只产生一次 WAL 刷写请求而很少落入该等待。该结论基于等待事件分布与参数对照，未在 PostgreSQL 源码层面逐行确认。测试环境为 OrbStack 上的 Docker 卷，overlayfs 驱动；生产环境的 WAL 磁盘特性不同，绝对数字不可直接迁移。

### 6.3 延迟与在途数的关系

三组直连数据中 p50 与"在途数 ÷ 吞吐"的计算值一致：

| 在途 | 计算值 | 实测 p50 |
|---|---|---|
| 64 | 8.8 ms | 7.9 ms |
| 256 | 37.6 ms | 34 ms |
| 2048 | 315 ms | 293 ms |

延迟随在途数线性增长的原因是排队，数据库单事务耗时始终约 5 毫秒。在同一数据库前增加中间层不能改变这个关系，只有提高吞吐或限制排队长度可以。

### 6.4 CommitStep

工具循环 step、无待处理 steer：1.34 毫秒。一次模型调用以秒计，该事务的占比可以忽略。

### 6.5 为什么瓶颈不在这里

1. **入队速率由人工输入限定。** steer 与 follow-up 只能在 run 进行中由用户手动提交。按 10 万在线用户折算：每用户每 60 秒一次为每秒 1667 次，每 10 秒一次为每秒 1 万次。单进程直连路径在笔记本上的上限约每秒 7000 次，前者留有 4 倍余量。
2. **读路径不随规模增长。** 面板改为事件驱动后，每用户刷新频率远低于每 10 秒一次；即使按每 10 秒一次计算，10 万用户为每秒 1 万次，单进程在笔记本上已达到该数字。每次读取为 4 条索引点查询，会话数从 100 到 2000 吞吐不变。
3. **同一数据库上有更重的写入。** 主消息的 run 准入、每个模型 step 的 CommitStep、历史消息写入都是每次一个事务，且每条比入队重。10 万用户下先触顶的是这些路径，队列入队只占总写入的一小部分。
4. **在同一 PostgreSQL 前分批不能提高上限。** A/B 数据说明等待提交的分批层只增加开销。能提高上限的只有减少每事务往返、异步持久化，或按 team_id 分片数据库。前两项收益有限或改变语义，最后一项才是 SaaS 规模的容量答案，且不属于本 PR 范围。

### 6.6 测量的局限

- 客户端与数据库同机，无网络往返。生产环境每条 SQL 加一次 RTT，4 条 SQL 的读请求至少加 4 个 RTT。
- 只测了单一路径。生产中 CommitStep、入队与历史写入与读同时发生，写会持有会话行锁并产生 WAL，并与读争用连接池。
- 数据量 270MB，全部在 PostgreSQL 缓存内。几十万会话时索引页不再全部命中。
- 单进程 httptest，绕过了 TCP 与 HTTP 解析，也没有多个应用实例争用同一数据库。
- 会话数从 100 增至 2000 且连接池调到 64 时，直连写吞吐降到 3826 到 4823 req/s，原因未查明。同配置多次运行吞吐波动约 20%。
- HTTP 读压测的 p99 在并发 64 时三次运行均落在 203 毫秒附近，pg_stat_statements 中四条语句最大执行时间为 39.5 毫秒，该台阶位于 PostgreSQL 执行之外，原因未查明。需在非 Docker Desktop 环境重测后判断。

## 7. 未决事项

| 事项 | 状态 | 说明 |
|---|---|---|
| p99 约 203 毫秒台阶 | 已定位 | 写路径：WAL 写入等待 WAL writer 周期唤醒，与 `wal_writer_delay` 200ms 对应，自动提交路径基本不受影响。读路径出现的同值台阶尚未用同一方法复测 |
| NOT VALID 外键补验 | 已完成 | 迁移末尾 DO 块尝试 VALIDATE，受限角色捕获 insufficient_privilege 跳过 |
| 入队背压 | 已完成 | 进程级有界门限 64，超出返回 429 `queue_admission_overloaded` |
| 去掉入队显式事务 | 已完成 | 入队改为自动提交单语句，p50 下降约 25% 到 30% |
| 按 team_id 分片 | Memoh-cloud 规划 | 用户与 team 多对多，小 team 占多数，需多租户共享实例加目录库映射；另开分支 |
| 人工 QA | 待验证 | run 进行中提交 follow-up 与 steer、拖动重排、promotion、run 结束后 continuation 自动接续 |

压测入口保留在仓库中，默认跳过，通过环境变量启用：

```
MEMOH_RUN_QUEUE_READ_LOAD=1   go test ./internal/agent/runtime/session/queue -run TestQueueReadLoad -v
MEMOH_RUN_QUEUE_WRITE_LOAD=1  go test ./internal/agent/runtime/session/queue -run TestQueueWriteLoad -v
MEMOH_RUN_QUEUE_HTTP_LOAD=1   go test ./internal/handlers -run TestSessionQueueListHTTPLoad -v
```

均需设置 `TEST_POSTGRES_DSN`。换到接近生产的环境可直接复现本文数据。
