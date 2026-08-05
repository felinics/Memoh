# Phase 2：Agent之间的通信

> 前置阅读：[README.md](./README.md)、[01-group.md](./01-group.md)
> 依赖：Phase 1的`group_bot_members`（同事发现与授权）。

## 1. 目标与形态

让一个Bot能够找同一Group内的另一个Bot帮忙，并与对方来回沟通。

形态是**一对一**，不是群聊。理由：上下文干净、责任明确、成本可控。需要让全组知晓的信息由Project承担（Phase 3），它本身就是更好的公共异步空间。

核心语义（决策D7）：

> **调用方是工具，被调方走正常Bot的turn路径。**

调用方的体感与现有Subagent一致——发出请求、拿到句柄、可等待可转后台。被调方则完全是一个被叫醒的正常Bot：自己的人格、工具、记忆、workspace、hooks、审批策略、compaction策略。

## 2. 为什么不复用Subagent的执行链

`internal/agent/tool/subagent.go`的工具外形（`spawn_agent`/`send_message`/`list_agents`）很适合A2A，但它的**执行链不能复用**。

原因在`runSubagentTask`构造`SpawnIdentity`的位置（`internal/agent/tool/subagent.go:908`）：整个identity是从父会话原样拷贝的，只替换了`SessionID`并置`IsSubagent=true`。

```go
Identity: SpawnIdentity{
    BotID:             req.parentSession.BotID,
    ChannelIdentityID: req.parentSession.ChannelIdentityID,
    CurrentPlatform:   req.parentSession.CurrentPlatform,
    ReplyTarget:       req.parentSession.ReplyTarget,
    WorkspaceTargetID: req.parentSession.WorkspaceTargetID,
    SessionToken:      req.parentSession.SessionToken,
    ...
}
```

这对同Bot的Subagent完全正确——它本来就应当跑在父Bot的容器里、用父Bot的凭据。但跨Bot时每个字段都是错的，其中两个有实际危害：

- `WorkspaceTargetID/Kind/Name`不替换，被调Bot会跑在调用方的workspace容器里，根本碰不到自己的文件。
- `ChannelIdentityID`/`CurrentPlatform`/`ReplyTarget`不替换，被调Bot拿到的是**调用方的对外身份**。它一旦调用`send_message`，消息会以调用方的身份发进调用方的渠道会话。这是直接的越权路径。

此外还有两处语义差异：`SetSystemPromptFunc`提供的是无人格的通用subagent提示词；`modelResolver`按调用方会话解析模型目录。

结论：**A2A是一条独立链路，`SpawnProvider`的执行链一行都不需要改动**，只是新工具的外形长得像它。两条链路并存，互不影响。

## 3. 工具设计

### 3.1 工具集

| 工具 | 作用 |
| --- | --- |
| `contact_agent(bot, task, run_in_background)` | 向同Group的Bot发起委托，返回句柄 |
| `send_message(id, message)` | 向已有句柄追加消息（与Subagent共用） |
| `list_agents()` | 列出当前会话持有的句柄（与Subagent共用，返回项带`kind`） |
| `list_teammates()` | 列出可联系的同事及其职责说明 |

设计要点：

- **不在`spawn_agent`上重载`bot`参数。** `fork`、`model_id`、`provider`对Teammate无意义，参数集不相交，description也完全不同，重载会让模型用错。`contact_agent`与`spawn_agent`并列。
- **句柄命名空间共用。** `send_message`与`list_agents`对两种句柄一视同仁，`list_agents`的返回项增加`kind: subagent | teammate`字段。这样调用方的后续交互体感完全一致。
- **`model_id`/`provider`/`fork`对`contact_agent`必须拒绝。** 调用方无权指定同事用什么模型，更无权把自己的上下文fork进对方。
- **不需要group参数。** 调用方与目标Bot共享任意一个Group即允许，无歧义需要消解。
- `list_teammates`的数据来自`group_bot_members.description`，跨调用方所属全部Group取并集，标注来源组。`allow_inbound_contact=false`的成员不出现在列表中。

### 3.2 条件注册

Bot可以不属于任何Group。Bot不属于任何Group、或所属Group内没有其他可联系的Bot时，这两个工具**不注册**；这不影响该Bot的普通chat、channel、heartbeat、schedule或Subagent能力。按项目约定，静态prompt模板不得提及它们（`internal/agent/runtime/native/prompt_test.go`守卫）。

## 4. Session归属

A2A产生的Session挂在**被调Bot**名下（决策D8）：

| 字段 | 值 |
| --- | --- |
| `bot_id` | 被调Bot |
| `parent` | 调用方的会话ID |
| `type` | 新增类型，不复用`TypeSubagent` |

挂在被调Bot名下是必须的——否则它的历史、记忆、ACL、Web会话列表、计费归属全都是错的，而且与Subagent就没有区别了。

**不能复用`TypeSubagent`**：`SpawnProvider.Tools()`第一行就是`if session.IsSubagent { return nil, nil }`（`internal/agent/tool/subagent.go:302`），Teammate会被剥光工具。需要在`internal/chat/thread/service.go:61`起的类型常量组中新增一个类型。

同时`IsSubagent`天然实现的「深度1」限制不适用：被调Bot在服务期间仍应能使用自己的Subagent。深度控制改用第7节的显式机制。

## 5. Session mode与返回值

### 5.1 需要新的session mode

正常Bot在chat模式下通过`send_message`往渠道回话，最终文本可能是空的。A2A会话若沿用该行为，被调Bot会试图向一个不存在的reply target发送消息，调用方什么也拿不到。

因此需要在`internal/agent/sessionmode/`新增一个模式，并在`internal/agent/runtime/native/prompts/`增加`mode_agent.md`，与`mode_chat.md`、`mode_discuss.md`并列。它必须说明：

- 你的最终文本就是给请求方的回复。
- 产出的文件或链接必须写进文本，否则请求方看不见（第5.2节）。
- messaging工具面向的是你自己的渠道，不是请求方。
- 回答你提问的是另一个Agent而非人类，因此不要提出只有人类能回答的问题（第8.1节）。

### 5.2 返回值语义

调用方拿到的是被调Bot本轮的**可见助手输出**（`internal/agent/turn/assistant_output.go`的`ExtractAssistantOutputs`是现成实现），**不是它的行为**。

推论：被调Bot如果产出了文件或链接，必须写进文本里，否则调用方看不见。这一点必须在`mode_agent.md`中明示。

## 6. 同步、异步与持久化委托

默认异步（决策D9），同时支持同步等待。

工具交互复用现有后台任务的外形：后台调用返回稳定句柄，调用方用`wait_until`等待状态变化，再用`get_background_status`读取状态与结果。**复用的是工具协议与UI体感，不是`internal/agent/background.Manager`的进程内map。** A2A委托可能跨越澄清、审批、Server重启与多实例切换，权威状态不得只存在于内存。

每次`contact_agent`必须持久化一条委托记录（实现可命名为`agent_delegations`），至少关联：

- 调用方Bot、Session与`caller_run_id`
- 被调Bot、Session与`callee_run_id`
- 对外稳定的句柄ID
- 状态、澄清轮次、最终结果或结构化失败原因
- 创建、更新时间与超时边界

状态机至少包含：

```
queued → running → waiting_caller / waiting_approval → running
                 ↘ completed / failed / cancelled / expired
```

`wait_until`遇到终态、`waiting_caller`或`waiting_approval`都必须返回，而不是只等待最终完成。`get_background_status`和`list_agents`从持久化记录读取权威状态；进程内Manager只能作为当前owner上的执行缓存与事件加速层。Server重启后，句柄仍可查询，运行状态按Session Runtime的恢复规则进入恢复、失败或`lost`，不得悄悄消失。

**同步模式即「创建同一条持久化委托＋立即等待」**，异步模式只省略立即等待。两种模式不得维护两套执行路径。

## 7. 路由安全

### 7.1 调用链与深度

调用链上必须携带：

- **深度计数**，超限拒绝。
- **已访问Bot列表**，出现重复即判定为环并拒绝。

默认异步已经把A→B→A从「真死锁」降级为「洪泛」，但两项限制仍然必须实现。这不属于会话生命周期的范畴（见README第7节），归本阶段负责。

### 7.2 授权

Group成员关系即主授权机制：调用方与目标Bot共享至少一个Group，且目标的`allow_inbound_contact`为真。

现有`bot_acl_rules`继续只负责人与渠道的`chat.trigger`，不参与A2A判定，也不得用于跨Group授予A2A权限。A2A的边界由Group拓扑和目标Bot的入站开关表达。

### 7.3 消息来源标注

被调Bot收到的用户消息头必须标明来源：`identity_type=bot`、发起Bot、以及**原始发起人**（on-behalf-of）。`internal/agent/turn/user_header.go`是现成的落点。

### 7.4 能力委托是明确语义（D19）

**A2A按被调Bot自身的权限与能力执行，不与原始发起人的权限取交集。** 这是有意的能力委托，而不是需要阻止的越权：调用方Bot可以请同事使用其模型、workspace、MCP和工具完成自己做不到的事情。

因此，第7.3节的原始发起人字段只用于来源展示、审计、审批上下文和事后追溯，**不是后端授权输入**。例如用户Alice只能直接访问A，A与B共享另一个Group时，A仍可以联系B并把结果带回给Alice；这个跨主体能力传导是by design。

治理边界是显式的Group拓扑、`allow_inbound_contact`、被调Bot自己的工具审批策略，以及完整审计记录。实现不得暗中增加“原始人类也必须有权直接访问B”之类的交集校验，否则会改变本设计的委托语义。

### 7.5 Prompt injection

调用方的task文本会进入被调Bot的输入，被调Bot会用自己的工具去执行它。该文本必须以带来源标注的数据形式呈现，不得作为指令处理。参见README第8.1节。

## 8. 中断处理：谁来回答，谁来批准

被调Bot是正常Bot，因此会走到两条需要外部回应才能继续的路径：`ask_user`与工具审批。A2A会话中没有人类坐在对面，两条路径的落点必须显式定义。

### 8.1 `ask_user`路由回调用方（D16）

**被调Bot调用`ask_user`时，请求路由给调用方Agent，由调用方作答。** 调用方成为该会话事实上的「用户」。

流程：

1. B调用`ask_user`，请求写入`user_input_requests`（`internal/agent/decision/input/`），并把持久化委托切到`waiting_caller`，记录问题与当前澄清轮次。
2. 同步调用内部的等待、或异步调用方之后显式执行的`wait_until`，在看到`waiting_caller`时返回问题与句柄。**已经返回的异步`contact_agent`不会被追溯性地改写结果。**
3. A通过`send_message(id, ...)`在同一句柄上作答。服务端必须校验该句柄正等待调用方回答，持久化答案后恢复B的run。
4. B消费答案并继续执行，委托状态回到`running`。

这让A2A成为可来回对话的委托，而不只是单次调用——正是本阶段「与对方互相通信」的目标。

配套要求：

- **`mode_agent.md`必须告知被调Bot：回答你的是另一个Agent，不是人类。** 因此不要提出只有人类能回答的问题（是否批准一笔支出、主观偏好、需要现实世界确认的事实）。需要人类判断的事项应当作为结论的一部分交回，由调用方链路上的人类处理。
- **澄清轮次必须有上限。** A与B都是模型，存在互相追问而不收敛的可能。超过上限后强制结束，并把已有进展作为结果返回。
- **回答方消失时快速失败。** `caller_run_id`进入终态后不会再有Agent回答；B应当在等待超时后以`expired`或明确失败结束本轮，而不是无限挂起。不会新建调用方run，也不会借Inbox唤醒A。
- **链式传播是允许的。** 若A本身也运行在A2A会话中，A的`ask_user`会继续向上一级传递。第7.1节的深度与调用链限制同样适用于这条路径。

### 8.2 审批路由到被调Bot的归属人类（D17）

**被调Bot触发`internal/toolapproval/`的审批流时，审批请求发给它自己的归属人类**，即`bots.owner_user_id`。

选择理由：另外两个候选——让A2A会话跑在不含需审批工具的受限集合上、或直接拒绝——都会让被调Bot在被Agent叫醒时的能力与被人类叫醒时不一致，违背D7「被调方表现得完全像一个正常Bot」。审批策略是Bot自身的属性，理应由它自己的归属人类行使。

配套要求：

- **调用方必须看到可读的等待状态**，例如「对方正在等待其归属人类审批某个工具调用」。静默挂起会让A无法做出合理决策（继续等、改问别人、还是放弃）。
- `bots.owner_user_id`当前由数据库保证非空；若owner成员关系失效或查询失败，审批创建必须fail closed，并把原因回传给A，不得默认放行。
- **审批超时后委托失败**，失败原因必须明确指出是审批未完成，而不是笼统的超时。
- 审批请求必须能追溯到发起委托的A及其原始发起人（第7.3节），否则审批人无法判断这次调用的来由。

### 8.3 调用方run结束后的结果不再投递（D18）

`caller_run_id`已经进入终态、而异步委托之后才完成时，结果**不再自动投递给调用方，也不投递到Inbox**。这里判断的是发起委托的run，不是长期存在的Session；不得用Session是否被删除来近似判断。

需要明确的是：**丢弃的是投递，不是委托记录或执行记录。** 持久化委托保留终态，B的这次Session及其全部输出也完整保存在B名下，人类可以追溯。丢弃只意味着没有自动路径重新唤醒A或把结果塞进A的后续run。

选择理由见`04-inbox.md`第6节。

## 9. 不在本阶段范围内

- **通用会话生命周期**：实时输出状态、run状态机、断线恢复、abort传播由独立工作线负责，见README第7节。本阶段直接依赖其`run_id`、决策持久化与恢复语义；A2A自身仍负责第6节的委托记录和调用方/被调方关联。
- **超时与忙碌处理**：属于生命周期范畴。但需要提出两项要求供该工作线参考：
  - 被调Bot是共享资源，可能正在服务人类或其他Bot，因此「对方忙碌」应当快速返回结构化的排队结果，而不是让调用方长时间前台等待。
  - 第8节的两条等待路径（等调用方回答澄清、等归属人类审批）都需要各自的超时，且超时原因必须可区分——调用方需要据此决定是重试、改问别人还是放弃。

## 10. 验收要求

### A2A-001：独立执行

- 被调Bot必须在自己的workspace容器中执行。
- 被调Bot的system prompt必须是它自己的人格，不是subagent通用提示词。
- 被调Bot使用的模型必须来自它自己的配置；调用方传入`model_id`或`provider`必须被拒绝。

### A2A-002：身份隔离

- 被调Bot调用`send_message`时，消息必须发往它自己的渠道，**不得**发往调用方的渠道会话。
- 该项必须有针对性的回归测试，它对应第2节指出的越权路径。

### A2A-003：Session归属

- 产生的Session的`bot_id`必须是被调Bot，`parent`必须指向调用方会话。
- 该Session必须出现在被调Bot的会话列表中。
- 该Session必须能获得完整工具集（不受`IsSubagent`门禁影响）。

### A2A-004：返回值

- 调用方拿到的必须是被调Bot本轮的可见助手输出。
- 被调Bot只调用messaging工具而未产出最终文本时，调用方必须收到明确的空结果说明，而不是静默的空字符串。

### A2A-005：路由安全

- 与调用方不共享任何Group的Bot必须无法被contact。
- `allow_inbound_contact=false`的Bot必须不出现在`list_teammates`中，且直接指定也必须被拒绝。
- 调用链超过深度上限必须被拒绝。
- 调用链中出现重复Bot必须被判定为环并拒绝。

### A2A-006：来源可见

- 被调Bot的消息头必须包含发起Bot与原始发起人。
- Web侧必须能从被调Bot的会话追溯到发起它的调用方会话。
- 原始发起人不得被用于收窄被调Bot的工具、Project或workspace权限；能力委托按第7.4节执行。

### A2A-007：Subagent不受影响

- 现有Subagent的全部行为与测试必须不变。本阶段不修改`SpawnProvider`的执行链。

### A2A-008：`ask_user`路由

- 被调Bot在A2A会话中调用`ask_user`时，请求必须路由给调用方，**不得**触达任何人类。
- 同步等待或调用方执行`wait_until`时，必须在`waiting_caller`状态返回问题原文与句柄，而不是空结果或伪造最终答案。
- 异步`contact_agent`已经返回后，不得假设可以追溯性修改其工具结果；状态只能通过持久化句柄观察。
- 调用方通过`send_message`作答后，被调Bot必须继续本轮执行。
- 澄清轮次达到上限后必须强制结束，并返回已有进展。
- 调用方run已结束时，等待必须在超时后结束，不得新建run或无限挂起。

### A2A-009：审批路由

- 被调Bot触发审批时，审批请求必须发给`bots.owner_user_id`对应的用户，**不得**发给调用方或调用链上的其他人类。
- 调用方必须收到可读的等待状态，能够区分「对方忙碌」与「对方在等审批」。
- owner成员关系失效或查询失败时，该工具调用必须被拒绝，并把原因回传给调用方。
- 审批请求必须携带发起委托的Bot与原始发起人。

### A2A-010：调用方run终态后的结果

- `caller_run_id`进入终态后，异步委托的结果必须不产生任何自动投递，包括不进入任何Inbox；不得以Session删除状态代替run终态。
- 持久化委托、被调Bot的Session与输出必须仍然完整保留并可追溯。

### A2A-011：持久化与恢复

- 创建成功的委托必须有稳定句柄，并持久化关联调用方run与被调方run。
- Server在`running`、`waiting_caller`、`waiting_approval`任一状态重启后，句柄与最后状态必须仍可查询。
- 双Server部署中，`wait_until`、`get_background_status`与`send_message`发送到任意健康实例都必须观察或控制同一条委托。
- 重复提交同一个调用方tool call不得创建第二条委托或重复执行被调Bot。
