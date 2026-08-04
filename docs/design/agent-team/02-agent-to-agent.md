# Phase 2：Agent之间的通信

> 前置阅读：[README.md](./README.md)、[01-group.md](./01-group.md)
> 依赖：Phase 1的`group_bot_members`（同事发现与授权）。

## 1. 目标与形态

让一个Bot能够找同一Group内的另一个Bot帮忙，并与对方来回沟通。

形态是**一对一**，不是群聊。理由：上下文干净、责任明确、成本可控。需要让全组知晓的信息由Wiki承担（Phase 3），它本身就是更好的公共异步空间。

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

Bot不属于任何Group、或所属Group内没有其他可联系的Bot时，这两个工具**不注册**。按项目约定，静态prompt模板不得提及它们（`internal/agent/runtime/native/prompt_test.go`守卫）。

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
- messaging工具面向的是你自己的渠道，不是请求方。

### 5.2 返回值语义

调用方拿到的是被调Bot本轮的**可见助手输出**（`internal/agent/turn/assistant_output.go`的`ExtractAssistantOutputs`是现成实现），**不是它的行为**。

推论：被调Bot如果产出了文件或链接，必须写进文本里，否则调用方看不见。这一点必须在`mode_agent.md`中明示。

## 6. 同步与异步

默认异步（决策D9），同时支持同步等待。

实现上复用`internal/agent/background/`已有的任务句柄机制（`wait_until`、`get_background_status`）：把一次A2A调用包装成调用方会话中的一个后台任务，该任务内部驱动被调Bot的一次turn。**同步模式即「异步＋立即等待」**，两种模式共用同一条实现路径。

## 7. 路由安全

### 7.1 调用链与深度

调用链上必须携带：

- **深度计数**，超限拒绝。
- **已访问Bot列表**，出现重复即判定为环并拒绝。

默认异步已经把A→B→A从「真死锁」降级为「洪泛」，但两项限制仍然必须实现。这不属于会话生命周期的范畴（见README第7节），归本阶段负责。

### 7.2 授权

Group成员关系即主授权机制：调用方与目标Bot共享至少一个Group，且目标的`allow_inbound_contact`为真。

现有的`bot_acl_rules`退化为组内的细化限制，**不得用于跨Group授予权限**。

### 7.3 消息来源标注

被调Bot收到的用户消息头必须标明来源：`identity_type=bot`、发起Bot、以及**原始发起人**（on-behalf-of）。`internal/agent/turn/user_header.go`是现成的落点。

携带原始发起人而不只是直接调用者，是为了让被调Bot的策略能基于真正的责任主体做判断。缺这个字段会形成confused deputy：调用方没有某个工具或MCP，但同事有，于是借同事之手完成自己无权做的事。这可以是有意的能力委托，但必须是可见的。

### 7.4 Prompt injection

调用方的task文本会进入被调Bot的输入，被调Bot会用自己的工具去执行它。该文本必须以带来源标注的数据形式呈现，不得作为指令处理。参见README第8.1节。

## 8. 待定项

以下两项影响`mode_agent.md`的写法，动prompt之前必须有答案（README第6节O1、O2）：

### O1：`ask_user`是否路由回调用方

被调Bot是正常Bot，可能调用`ask_user`，但A2A会话中没有人类。

建议方案：路由回调用方，让调用方成为该会话的「用户」。调用方的工具调用返回「对方需要澄清：……」，回答后继续。这样A2A就是双向、有来有回的对话，而不只是单次委托——这更符合本阶段「与对方互相通信」的目标。

### O2：工具审批由谁批

被调Bot可能触发`internal/toolapproval/`的审批流。候选方案：

1. 审批请求发给被调Bot的归属人类，调用方挂起或超时（倾向此项）
2. A2A会话运行在不含需审批工具的受限集合上
3. 直接拒绝

## 9. 不在本阶段范围内

- **会话生命周期**：实时输出状态、run状态机、断线恢复、abort传播由独立工作线负责，见README第7节。本阶段假定该能力可用。
- **超时与忙碌处理**：属于生命周期范畴。但需要提出一项要求供该工作线参考——被调Bot是共享资源，可能正在服务人类或其他Bot，因此「对方忙碌」应当快速返回结构化的排队结果，而不是让调用方长时间前台等待。
- **迟到结果的归宿**：见README第6节O3。

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

### A2A-007：Subagent不受影响

- 现有Subagent的全部行为与测试必须不变。本阶段不修改`SpawnProvider`的执行链。
