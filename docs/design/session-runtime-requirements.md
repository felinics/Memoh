# Session Runtime 正确性要求

## 1. 文档目的

为什么要写这份文档？

1. 在继续实现前，我们要先明确什么结果才算正确。
2. 避免为了测试去设计代码，为 `internal/agent/runtime/session/acceptance/` 下的黑盒验收提供最终验收依据。
3. 区分对外正确性与包内算法正确性，避免仅凭单元测试通过就判断运行时已经接入完成。

本文中的「必须」表示合入条件。「可以」表示实现可自行选择，但选择后的行为必须可观察、可测试。

## 2. 问题边界

一次用户输入会经过接收、准入、执行、流式输出、持久化和终止。现实情况下可能遇到连接中断、Server 进程退出，同一请求也可能被发送到不同实例。Session Runtime 必须在这些条件下维持同一个执行事实。

它需要解决以下问题：

- 系统是否接受这次输入？
- 同一个请求是否已经有对应执行？
- 当前由哪个 `Server` 执行？
- 客户端断线后如何知道当前状态？
- abort、审批和用户补充输入应该发给哪个 `Server` 执行？
- `Server` 退出后，已接受的输入和执行状态如何处理？
- 哪些消息属于同一个 conversation turn？

## 3. 身份与状态

正确性首先依赖明确的身份。不同身份不能由文本、附件、时间或 external ID 的相似性推断。

| 身份 | 产生方 | 作用 |
| --- | --- | --- |
| `invocation_id` | 调用方 | 标识一次可重试的提交。重复提交必须落到同一准入结果。 |
| `run_id` | Server | 标识一次已准入的执行。名称可以不同，但必须有等价的服务端权威身份。 |
| `turn_id` | Server | 标识 conversation timeline 中一个规范化 turn，并把用户输入、执行和最终输出关联起来。 |
| `decision_id` | Server | 标识一次审批或用户输入请求，保证回答不会应用到错误执行。 |
| `session_id` | Server | 标识会话边界，但不能单独标识一次执行或一次 turn。 |

一次 run 至少具有以下可区分状态：

- `accepted`：输入和准入结果已持久化，调用方可以安全停止重试。
- `running`：某个有效 owner 正在执行。
- `waiting_decision`：执行等待一个明确的 `decision_id`。
- `finishing`：最终输出已经持久化，owner 已提交带 fencing 的终态提案，但持久化终态与 live 投影尚未全部收敛；该状态仍占用 active slot 并续租。
- `completed`：执行正常结束，最终结果已持久化。
- `aborted`：终止请求已生效。
- `failed`：执行失败，失败结果已持久化。
- `lost`：owner 消失，系统尚未恢复执行，但已接受的输入没有丢失。

终态为 `completed`、`aborted`、`failed` 或 `lost`。`lost` 不是成功结果；系统必须允许查询，并通过明确策略重试或开始新的 run。

## 4. 正确性要求

### SR-BASE-001：基础执行循环

一次合法输入必须满足：

- Server 通过公开接口接收输入。
- 模型执行至多对应一个已准入 run。
- 用户输入与最终 Agent 输出被持久化。
- 客户端收到可识别的开始、增量和终止事件。
- HTTP 历史查询与 WebSocket 最终结果一致。

### SR-ADM-001：重复提交幂等

相同 `session_id + invocation_id` 的重复提交必须返回同一个准入结果，不能启动第二次模型执行，也不能重复写入用户消息。

如果相同 `invocation_id` 携带不同的规范化输入，Server 必须返回稳定、可识别的冲突错误。不能把它当作新的 run。

准入结果必须先持久化，再向调用方确认 `accepted`。如果 Server 在确认前退出，调用方可以安全重试。

### SR-OWN-001：单会话执行归一

同一个 session 同一时刻只能有一个有效 owner 执行 Agent turn。

第二个不同 invocation 到达时，实现可以选择：

- 持久化排队；或
- 返回稳定、可重试的 busy 结果。

实现不能在不同 Server 实例上静默并发执行同一 session 的多个 turn。

### SR-OWN-002：owner 租约与 fencing

owner 身份必须有跨实例可验证的租约或等价机制。owner 更换后，旧 owner 的后续输出、决策和终态写入必须被拒绝。

进程内互斥只能优化单实例执行，不能作为多实例正确性的唯一依据。

### SR-OBS-001：重连后的权威快照

客户端连接或重连到任意 Server 实例后，必须能够获得该 session 当前 run 的权威快照。快照至少包含：

- run 身份与当前状态；
- 已确认的输出位置或版本；
- 当前是否可以 abort；
- 若在等待决策，对应的 `decision_id` 和决策类型；
- 若 owner 已丢失，明确的 `lost` 状态。

首版 WebSocket 验收协议使用以下消息：

- 客户端发送 `runtime_subscribe`，携带 `session_id`，并可携带最后确认的事件位置。
- Server 返回 `runtime_snapshot`，然后再发送该 run 的增量事件。

如果生产协议采用不同名称，必须同时修改本文和验收测试，不能只在实现内部建立订阅。

快照之后的增量事件必须能够判定顺序。客户端不能依赖“重连恰好落到原实例”才能恢复视图。

### SR-OBS-002：事件可去重、可续接

同一个 run 的可观察事件必须带有单调位置、版本或等价标识。客户端重复收到事件时可以去重，发生空洞时可以请求快照或续接。

仅发送无位置的文本 delta 不能作为断线恢复协议。

### SR-OBS-003：多订阅者一致性

同一个 session 可以同时被多个已授权客户端订阅。典型情况是同一用户在多个网页中打开同一个 session。这些客户端可能连接到同一个 Server，也可能连接到不同 Server。

run 与 session 关联，不能绑定到发起它的 WebSocket。发起连接断开后，run 必须继续按照既定状态执行，其他订阅者仍能观察它；只有明确的 abort 或运行时终止条件可以停止执行。

对于同一个 run，必须满足：

- 已经在线的订阅者能够收到准入后的状态变化和后续增量事件。
- 中途加入或重新连接的订阅者先获得权威快照，再接收快照之后的增量事件。
- 所有订阅者观察到相同的 run 身份、`turn_id`、状态和待处理决策。
- 事件使用同一个 epoch 和单调位置。每个订阅者独立去重、检查空洞，并在需要时通过快照恢复。
- 慢客户端或断开的客户端不能阻塞 run，也不能影响其他订阅者。订阅缓冲区溢出时，Server 必须发送可识别的丢失信号，要求该客户端重新获取快照。
- 新增订阅不能创建、重放、取得或释放 run 的执行权。
- 每条订阅连接必须独立校验该用户对 bot 和 session 的读取权限，不能仅凭 `session_id` 建立订阅。

这里的一致性不要求不同连接在同一时刻收到完全相同的网络帧。客户端处理到同一个已确认位置，或者应用同一份权威快照后，得到的 session runtime 投影必须相同。

### SR-CTL-001：跨实例控制

abort、审批回答和用户补充输入发送到任意健康 Server 实例时，都必须路由到当前 run，或返回稳定、明确的不可执行结果。

控制请求必须有确认语义。调用方不能仅根据 WebSocket 写入成功判断控制已经生效。

首版验收使用 `control_ack` 确认控制结果。确认必须包含 session、run 或 stream 身份、控制类型和是否生效。

重复发送相同控制请求必须幂等。

### SR-DUR-001：已接受输入不因进程退出而丢失

Server 在返回 `accepted` 后退出，重启后的系统必须仍能查询到：

- 原始用户输入；
- `invocation_id`、run 身份和 `turn_id`；
- 最后一个已持久化状态；
- 已持久化的输出；
- 当前是恢复、失败还是 `lost`。

不能只把已接受输入保存在 goroutine、WebSocket handler 或进程内 Manager 中。

### SR-DUR-002：终态提交的一致性

run 进入终态时，终态、最终输出和 turn 投影必须形成一个一致的持久化结果。

如果无法在一个数据库事务中完成，协议必须允许幂等重放，并保证重放不会生成第二个 turn 或第二份最终消息。

最终输出持久化之后、释放 owner lease 之前，owner 必须先用当前 fencing token 持久化不可变的终态提案，再把 live 投影切换到 `finishing`。在最终 ledger 写入成功前：

- Native、ACP、schedule 和 subagent 的终态事件都必须位于各自最终输出持久化屏障之后；屏障失败时不得发布成功终态；
- `finishing` 必须继续占用 session 的 active slot 并续租；
- 新的输入、abort、steer 和 decision response 不能覆盖已接受的终态提案；
- 第一次终态提案胜出，重试只能重放同一个结果；
- owner 对持久化终态失败的本地重试必须有总预算；预算耗尽后，Redis/Valkey 部署停止续租并由 lease reaper 收敛，memory 部署把同一 fenced handle 交给本地 reaper 的共享重试队列，不能为每个 run 永久保留重试 goroutine；
- owner 进程退出、租约到期、live backend 更换或优雅关机时，reaper/关闭流程必须把提案收敛为其原始 `completed`、`aborted` 或 `failed`，不能改写为 `lost`；
- 只有从未跨过终态提案边界的 active run 才能因 owner 消失进入 `lost`。

ledger 成为终态后，系统必须以该 durable outcome 修复可能滞后的 live 投影；修复必须校验 live run ref 中保存的同一个 fencing token，不能覆盖后继 run。

### SR-TURN-001：turn 必须是显式身份

每个已准入 run 必须显式关联一个服务端生成的 `turn_id`。用户消息、Agent 输出、工具事件和决策都通过该身份归属到同一个 turn。

实现不能根据以下字段是否相似来决定两条消息属于同一 turn：

- 文本；
- 附件；
- 时间；
- external message ID。

这些字段可以用于来源追踪和冲突诊断，但不能代替规范化身份。

### SR-DEC-001：决策归属与持久化

审批或用户输入请求必须有 `decision_id`，并关联到 run 和 `turn_id`。创建决策、回答决策和消费答案都必须可重试。

owner 退出后，未回答决策必须仍可查询。新 owner 恢复执行前，必须先确认该决策是否已经回答，避免重复执行受审批保护的动作。

### SR-DEP-001：单实例与分布式部署边界

单实例部署可以使用进程内 live-state backend，但准入记录、turn 身份和终态仍必须持久化。

多实例部署必须启用共享的分布式 live-state backend，例如 Redis 或 Valkey，并在启动时验证配置。缺少该依赖时，Server 必须拒绝进入多实例模式，不能退化为多个互不知情的进程内 Manager。

Redis 或 Valkey 不应成为单实例 OSS 部署的强制依赖。

## 5. 验收层级

### 5.1 黑盒验收

位置：`internal/agent/runtime/session/acceptance/`

黑盒验收必须使用：

- 真实 `cmd/agent` Server 进程；
- 真实 PostgreSQL；
- 公开 HTTP 与 WebSocket 接口；
- 两个共享数据库的 Server 实例，用于分布式场景；
- 可控的假模型服务，用于观察调用次数、并发和取消。

验收测试不能通过直接构造 `session.Manager`、WebSocket handler 或数据库内部 adapter。否则它证明的是内部协作模式。

### 5.2 包内算法测试

位置：`internal/agent/runtime/session/*_test.go`

包内测试负责：

- Manager 状态转换；
- 队列与容量边界；
- backend 原子操作；
- Redis 脚本与 TTL；
- fencing token 比较；
- 并发与竞态条件。

这些测试可以使用 fake clock、内存 backend 和直接函数调用，但不能替代黑盒验收。

## 6. 验收场景

| 场景 | 部署 | 主要断言 | 对应要求 |
| --- | --- | --- | --- |
| baseline | 单 Server | 完成一次 turn，历史与流式结果一致 | SR-BASE-001 |
| reconnect snapshot | 双 Server | 在 B 重连后看到 A 上 run 的权威状态 | SR-OBS-001、SR-OBS-002 |
| concurrent subscribers | 单/双 Server | A、B 同时订阅同一 session；A 发起 run 后双方状态收敛，A 断线不影响 B | SR-OBS-002、SR-OBS-003 |
| reconnect abort | 双 Server | abort 经 B 到达 A 上的 owner，并收到确认 | SR-CTL-001 |
| duplicate invocation | 双 Server | 同一 invocation 只执行、只持久化一次 | SR-ADM-001 |
| same-session concurrency | 双 Server | 不出现两个有效 owner 并发执行 | SR-OWN-001、SR-OWN-002 |
| owner crash | Server 重启 | 已接受输入与 run 状态仍可查询 | SR-DUR-001 |
| terminal retry | 故障注入 | 重放终态不产生重复 turn 或输出 | SR-DUR-002、SR-TURN-001 |
| terminal proposal crash | 故障注入/Server 重启 | `finishing` 在 owner 消失后收敛到原提案，不能变成 `lost` | SR-DUR-002、SR-OWN-002 |
| decision restart | Server 重启 | decision 可恢复，回答只消费一次 | SR-DEC-001 |

当前黑盒验收代码覆盖 `baseline`、`reconnect snapshot`、`reconnect abort`、`duplicate invocation`、`same-session concurrency` 和 `owner crash`。包内测试使用故障注入覆盖终态提案重试、owner 租约到期、live backend 更换和优雅关机；`concurrent subscribers`、完整进程级 `terminal proposal crash` 与 `decision restart` 仍需补充黑盒用例。

## 7. 通过标准

Session Runtime 可以进入生产调用路径，至少需要同时满足：

1. 本文所有“必须”项有对应实现或明确排除的部署边界。
2. `internal/agent/runtime/session/*_test.go` 验证内部算法并通过 race 检查。
3. feature-local 黑盒验收在单实例和双实例配置中通过。
4. crash 场景证明 `accepted` 之后的数据不会只存在于进程内。
5. 默认 OSS 单实例配置不强制依赖 Redis 或 Valkey。
