# 父 Turn Abort 后 Subagent Heartbeat 崩溃修复设计

日期：2026-08-21  
分支：`fix/subagent-heartbeat-after-abort`  
基线：`origin/main`，提交 `820582bec`  
状态：内部 review/spec，仅供项目内部使用，不提交实现

## 结论摘要

当前分析的根因仍然成立：foreground subagent 的实际任务被设计为脱离父
turn 继续运行，但它的 heartbeat goroutine 错误地同时脱离了父 Native stream
的生命周期，并保留了父 stream 的 `Emitter`。父 turn abort 后，Native stream
正常结束并由 `Agent.Stream` 的 owner goroutine 关闭 channel；下一次 heartbeat
仍调用该 emitter，最终在 `sendEvent` 中向已关闭 channel 发送，触发：

```text
panic: send on closed channel
```

推荐采用此前的第四种方案：

1. heartbeat 只绑定 foreground 等待的 context；父 turn abort 后 heartbeat 停止，
   但 managed child task 继续运行。
2. Native 为所有可能逃逸的 stream emitter 增加关闭屏障，在 channel 关闭前拒绝
   新的 emitter 调用，并等待已经进入的调用返回。

这是比“只改 heartbeat context”更好的方案，因为它同时修复了错误的 producer
生命周期和 channel ownership 的安全边界。

## 源码复核

### Web Stop 到父 run abort

Web Stop 按钮位于：

```text
apps/web/src/pages/home/components/chat-pane.vue:800
  streaming ? chatStore.abort(paneTarget) : handleSend()
```

调用链为：

```text
chatStore.abort
  -> runtime-integration.abort
  -> realtime.abortWebSocketRun
  -> LocalChannelHandler.abortWSRun
  -> Service.AbortRuntimeRun
  -> session.Manager.AbortControl / abortLocal
  -> ctrl.cancel()
```

`abortLocal` 先记录 abort intent，再取消拥有父 run 的 context。这个 context
最终传入 Native：`Agent.Stream(ctx, cfg) -> runStream(ctx, cfg, ch)`。

`Agent.Stream` 创建 `ch`，并由它启动的 goroutine 执行唯一的 `close(ch)`。父
context 被取消后，Native 生成 `agent_abort` terminal event，`runStream` 返回，
owner goroutine 随后关闭 `ch`。这部分行为本身符合当前 stream contract。

### Foreground subagent 的实际生命周期

foreground 和显式 `run_in_background=true` 的 subagent 都先通过
`background.Manager.StartAgentTask` 创建 managed task。foreground 路径使用：

```go
taskID, taskCtx, err := p.bgManager.StartAgentTask(
    context.WithoutCancel(ctx), ...,
)
result := p.runAgentRequest(taskCtx, key, req)
```

`taskCtx` 由 background manager 提供独立 timeout 和 cancel；child task 的执行
生命周期从创建开始就属于 background manager，而不属于父 Native stream。父
turn abort 不应自动杀掉已经开始的 child task，否则会违背现有 task status、wait、
kill、child thread admission 和独立 timeout 语义。

### Heartbeat 的错误绑定

当前 heartbeat 使用：

```go
heartbeatCtx, heartbeatCancel := context.WithCancel(context.WithoutCancel(ctx))
defer heartbeatCancel()
p.startSpawnHeartbeat(heartbeatCtx, session, 1)
```

heartbeat 保存 `session.Emitter`，每 30 秒调用
`StreamEventSpawnHeartbeat`。Native emitter 又调用：
`sendEvent(ctx, ch, toolStreamEventToAgentEvent(evt))`。

这里有两个独立问题：

1. heartbeat 使用 `WithoutCancel`，父 abort 后仍然继续运行；
2. emitter 没有 stream close gate，无法拒绝已结束 stream 的 late emit。

`sendEvent` 中的 cancellation select 不能解决第二个问题：当 context 已取消且
channel 已关闭时，向已关闭 channel 发送仍可能被 select 选中并直接 panic。

因此，之前的错误原因判断是正确的，现场时间线与源码吻合：父 run abort，
Native `runStream` 发送 terminal abort 并返回，owner `close(ch)`，detached
heartbeat 下一次 tick 通过 emitter 调用 `sendEvent`，触发 panic。

## 预期产品语义

foreground 只描述“当前 tool call 是否同步等待结果”，不描述 child task 的
所有权。父 turn abort 后：

- 父 turn：进入 aborted，Native stream 正常关闭；
- foreground heartbeat：停止，不再向父 stream 发 progress；
- child managed task：继续 running；
- child run：继续使用自己的 session、admission、timeout 和 task ID；
- 用户：可通过现有 background status/wait/kill 能力观察或停止 child task。

不需要新增“foreground 转 background”的数据库状态。它从创建时就是
manager-owned task，父 abort 只是结束同步等待方。

## 方案比较

### 方案一：父 abort 时取消 child task

不采用。它与当前 managed task 的 detached context、独立 child session、
background manager 状态和显式 kill 语义冲突，会让父 turn 的 UI 操作无端丢弃
已经开始的子任务。

### 方案二：只让 heartbeat 继承父 context

可以解决本次通常路径，但不能形成 channel ownership 保证。heartbeat tick 与
parent cancellation 可能同时 ready，仍可能进入 emitter；未来任何其他 detached
producer 也会复现同类 panic。

### 方案三：只在 Native emitter 外加防护

可以避免本次 panic，但 heartbeat goroutine 仍会在 child task 整个生命周期中
持续尝试向已结束的父 stream 发事件，生命周期语义仍然错误。

### 方案四：producer 修正 + Native owner 关闭屏障（推荐）

同时修正两层问题：producer 层 heartbeat 跟随 foreground context 取消，child
task 仍用 detached manager context；owner 层 Native stream 在关闭 channel 前关闭
emitter gate，拒绝新的 callback 并等待已经进入的 callback 返回。

这是当前最好的方案，因为它既修正预期生命周期，也为 channel close 建立可验证
的并发边界，不改变 child task、数据库、API 或 Web 协议。

### 方案五：`recover` 捕获 panic

不采用。它只能掩盖已经发生的 ownership 错误，不能保证 terminal event 顺序，
也不能保证其他 producer 安全。

## 第四种方案的精确实现边界

### Heartbeat producer

foreground heartbeat context 直接继承父 tool execution context；child 执行仍
使用 background manager 返回的 detached `taskCtx`。heartbeat loop 抽成接受
receive-only tick channel 的内部函数；生产代码使用 30 秒 ticker，测试使用手动
tick，避免真实等待 30 秒。收到 tick 后再次检查 context；父 context 已取消时不
再调用 emitter。

### Native emitter gate

`Agent.Stream` 仍是 channel 的唯一创建者和 closer。gate 只保护二级 producer，
至少包含 mutex、closing 状态、in-flight callback 计数和关闭等待机制。

一次 emit：

1. 加锁；
2. 若 closing，解锁并返回，不接触 `ch`；
3. 在锁内增加 in-flight 计数；
4. 解锁并执行 `sendEvent`；
5. 使用 defer 减少 in-flight 计数。

关闭顺序必须是：

1. Native 确定本轮不会再接受 secondary emitter；gate 标记 closing；
2. gate 等待已经进入的 emitter callback 返回；
3. Native 发送 terminal `agent_end` 或 `agent_abort`；
4. `runStream` 返回；
5. `Agent.Stream` owner goroutine 执行 `close(ch)`。

这样可以保证 terminal event 之后不会再出现 secondary event。绝不能先
`close(ch)` 再关闭 gate，也不能让 gate 等待整个 child task。gate 只等已经进入的
callback；heartbeat goroutine 的结束由 foreground context 负责。

gate 至少覆盖 `tools.StreamEmitter`（attachment、reaction、speech/image、
subagent heartbeat）和 tool execution metadata update callback。

## Channel ownership 审查结论

当前 channel owner 是 `Agent.Stream` wrapper goroutine。`runStream` 是主
producer；工具 emitter 和 metadata callback 是可能由工具/SDK goroutine 触发的
二级 producer。此前实现缺少“关闭前停止新增 producer、等待已进入 producer”的
同步协议。因此，即使 heartbeat context 改正确，只依赖 cancellation select 仍
不足以证明不会 panic。第四种方案的 owner gate 是必要的安全边界。

## 最小改动清单

### `internal/agent/tool/subagent.go`

- heartbeat context 改为继承父 tool context；
- child task 保持现有 detached manager context；
- 抽取可注入 tick 的 heartbeat loop；
- 注释明确 heartbeat 属于 foreground stream，不属于 child task。

### `internal/agent/runtime/native/agent.go`

- 增加 Native-private stream emitter gate；
- tool side-effect emitter 和 metadata callback 通过 gate；
- terminal delivery 前关闭 gate并等待 in-flight callback；
- 保留 `Agent.Stream` 为唯一 channel closer。

### 测试文件

- `internal/agent/tool/subagent_bg_test.go`：父 abort、child 阻塞、heartbeat 停止、
  child 仍 running；
- `internal/agent/runtime/native/stream_test.go`：stream abort/close 后 late emitter
  被拒绝，且不 panic；
- 增加 in-flight emitter 与 gate shutdown 的竞态测试，并在 `-race` 下运行。

## 确定性回归测试

### Foreground heartbeat

1. fake child 在 barrier 上阻塞，并等待 manager 报告 task 为 `running`；
2. 手动 tick，断言收到一个 `spawn_heartbeat`；
3. 取消父 context；
4. 等待 heartbeat loop 退出；
5. 再次 tick，断言无事件且无 panic；
6. 断言 child task context 仍未取消、manager snapshot 仍为 `running`；
7. 显式 release/kill child 并等待清理。

### Native stream close

1. fake tool 捕获 Native 注入的 emitter，并阻塞 tool execution；
2. 取消父 stream；
3. 断言收到唯一 terminal `agent_abort`，随后 channel 正常关闭；
4. channel 关闭后调用 captured emitter，断言返回且不产生事件；
5. 增加 emitter 恰好在 gate shutdown 前进入的测试，断言 owner 等待它退出后才
   关闭 channel。

测试使用 barrier/channel，不使用真实 30 秒 sleep；重复运行并使用 `-race`。

## 数据库与 API 影响

不需要修改数据库 schema、migration、SQL query、OpenAPI、SDK、Web API 或 wire
event。现有 `spawn_running`、task status、abort 和 terminal event contract 保持
不变。

## 现有验证信息

之前已验证：`internal/agent/tool` 和 `internal/agent/background` 整包测试通过；
Native cancellation focused tests 通过。Native 整包存在一个与本问题无关的既有
失败：`TestRunMidStreamRetrySuppressesRawErrorAfterStepBudgetCancellation`。实现
阶段应先运行新增 focused tests，再区分该基线失败与本修复结果。
