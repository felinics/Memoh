# Agent Team设计总览

## 1. 文档目的

Memoh目前允许创建多个Bot，但Bot之间彼此不可见、不能协作。本系列文档描述如何把「多个孤立的Bot」变成「一个可以协同工作的Agent团队」。

本文是总览，负责说明范围、术语、已定与待定的决策，以及阶段之间的依赖关系。具体设计在分阶段文档中：

| 阶段 | 文档 | 内容 |
| --- | --- | --- |
| Phase 1 | [01-group.md](./01-group.md) | Group模型：成员关系、权限、迁移 |
| Phase 2 | [02-agent-to-agent.md](./02-agent-to-agent.md) | Agent之间的一对一通信 |
| Phase 3 | [03-wiki.md](./03-wiki.md) | 共享知识库：多Wiki＋每Wiki一份ACL |
| Phase 4 | [04-inbox.md](./04-inbox.md) | Inbox投递与事件驱动触发 |

本系列文档描述的是设计决策与验收标准，不是逐步骤的执行计划。文中「必须」表示合入条件，「可以」表示实现自行选择但选择后行为必须可观察、可测试。

## 2. 阶段依赖

四个阶段构成两条彼此独立的链：

```
Phase 1  Group模型  ──▶  Phase 2  A2A通信
              提供成员关系 → 同事发现与授权

Phase 3  Wiki      ──▶  Phase 4  Inbox
              提供@提及事件
```

- 两条链之间没有依赖，可以完全并行推进。
- Phase 2依赖Phase 1的`group_bot_members`。
- Phase 4依赖Phase 3：Inbox在本方案中的唯一事件源是Wiki提及。
- **Wiki与Group解耦**（决策D3）：Wiki是与Group平行的独立实体，权限由自身ACL决定，因此Phase 3不依赖Phase 1。唯一的交叉是「Group作为ACL主体」，属于可后置的增量，两条链都落地后再加即可。

## 3. 术语

| 术语 | 含义 |
| --- | --- |
| Team | 租户与隔离边界。承载RLS、计费与数据隔离。开源自部署版本永远只有`internal/team/id.go`里的`DefaultTeamID`一个Team。 |
| Group | Team之下的协作与授权分组。决定「谁能看到哪些Bot」与「哪些Bot之间可以互相联系」。**不拥有Wiki，也不是隔离边界。** |
| Wiki | Team之下的独立一等实体，可以有多个。**它自身就是权限边界**，通过ACL授予user/bot/group/team。与Group平行，互不从属。 |
| Bot | 平台上的一个AI Agent实体。拥有独立的workspace、模型配置、人格、长期记忆与渠道绑定。 |
| Subagent | 由某个Bot在自己会话内派生的从属执行体。共享父Bot的workspace与凭据，生命周期绑定父会话。见`internal/agent/tool/subagent.go`。 |
| Teammate | 同一Group内的另一个Bot。它是独立实体，不是Subagent。 |
| A2A | Agent to Agent，指Bot之间的一对一通信。 |
| Session/Thread | 一次会话。对内称Thread（`internal/chat/thread/`），对外兼容字段仍为`session_id`。 |

**Subagent与Teammate的区别是本设计的核心分界**，两者不可混用：

| | Subagent | Teammate |
| --- | --- | --- |
| 归属 | 父Bot | 独立Bot |
| workspace | 父Bot的容器 | 自己的容器 |
| 模型 | 父会话指定，可被调用方选择 | 自己的配置，调用方无权指定 |
| system prompt | 通用subagent提示词，无人格 | 自己的人格 |
| 长期记忆 | 父Bot的 | 自己的 |
| 生命周期 | 绑定父会话 | 独立 |
| 执行路径 | `SpawnProvider`直接调用`SpawnAgent.Generate` | 正常Bot的turn路径 |

## 4. 已定决策

以下决策来自设计讨论，实现时不需要再确认。

| # | 决策 | 出处 |
| --- | --- | --- |
| D1 | Group位于Team之下，不替换Team。Team继续作为RLS与隔离边界。 | Phase 1 |
| D2 | 一个Bot可以属于多个Group。 | Phase 1 |
| D3 | **Wiki与Group解耦**：Wiki是独立的一等实体，一个Team内可以有多个，各自带ACL。Group不拥有Wiki，只能作为ACL的一种授予主体。 | Phase 1 / Phase 3 |
| D4 | Group只约束「谁能看到哪些Bot」与「哪些Bot之间可以互相联系」，不决定知识的可见性。 | Phase 1 |
| D5 | 长期记忆保持per-bot，不按Group分区。 | Phase 1 |
| D6 | Session不携带`group_id`。chat、channel inbound、heartbeat、schedule等入口只需要`bot_id`。 | Phase 1 |
| D7 | A2A的调用方是工具（句柄式，体感与Subagent一致），被调方走**正常Bot的turn路径**，不复用`SpawnProvider`的执行链。 | Phase 2 |
| D8 | A2A产生的Session挂在**被调Bot**名下，`parent`指向调用方会话。 | Phase 2 |
| D9 | A2A默认异步，同时支持同步等待。 | Phase 2 |
| D10 | A2A使用独立的session mode与system prompt，与Subagent分开。 | Phase 2 |
| D11 | Wiki面向人和Agent双方使用，不是仅供Agent读取的内部存储。 | Phase 3 |
| D12 | Wiki的结构化数据存Postgres，正文与附件blob走storage provider抽象；S3是新增provider，默认仍为localfs。 | Phase 3 |
| D13 | Wiki的权限边界**只到Wiki一级**，不做节点级ACL。ACL主体为user/bot/group/team，级别为read/write/manage。 | Phase 3 |
| D14 | Inbox不承载A2A。A2A走工具直连，Inbox只处理事件驱动的投递（Wiki提及、人类通知）。 | Phase 4 |
| D15 | 保留现有discuss模式（`internal/channel/discuss/`），它是面向渠道群聊的特性，与Agent Team定位不同，不合并、不移除。 | 全局 |

## 5. 明确排除的范围

以下内容经讨论后决定**不做**，实现时不要顺手加上：

| 排除项 | 原因 |
| --- | --- |
| 共享工作空间（多Bot挂载同一个卷） | 多个Agent并发写同一文件没有锁与冲突检测，会静默损坏；三种容器backend语义不一致；remote runtime下不成立；配额与快照语义无法定义。收益小于复杂度。**连带要求见Phase 3第6节：Wiki必须承担Bot之间的文件交换。** |
| Agent之间的群聊 | 一对一是默认形态。公共异步空间由Wiki承担。 |
| Wiki的block级富文本编辑器与实时协同（CRDT） | 工作量以月计，且与Agent协同主线无关。Phase 3收敛到节点＋Markdown正文＋评论＋提及。 |
| Wiki按Group划分 | 每建一篇文档都要先选组，跨组引用被禁又造成困惑。改为Wiki自身即权限边界（D3）。 |
| Wiki的节点级访问控制 | 查询要逐节点判权、树上出现空洞、用户无法解释自己为什么打不开某页。需要不同权限就再建一个Wiki。见`03-wiki.md`第4.1节。 |
| Wiki有效权限取Bot与对话人的交集 | heartbeat、schedule会话没有人类，A2A会话对面是Bot，交集算不出来。改为治理层措施，见`03-wiki.md`第4.3节。 |
| 记忆按Group分区 | 见D5。Group不决定知识可见性，单独限制记忆没有意义。 |
| 为人类新建一套独立收件箱 | 人类已有`user_channel_bindings`。Inbox对人落成Web通知与既有渠道推送。 |

## 6. 待定决策

以下三项在动工前必须有答案，但都不影响表结构，可以在Phase 2实现阶段收尾时确定。

| # | 待定项 | 影响 |
| --- | --- | --- |
| O1 | A2A会话中被调Bot调用`ask_user`时，是否路由回调用方Agent？ | 决定A2A是「单次委托」还是「可来回对话」。影响`mode_agent.md`措辞与`internal/agent/decision/input/`的路由。 |
| O2 | A2A会话中被调Bot触发工具审批时，由谁审批？ | 候选：被调Bot的归属人类／限制A2A工具集／直接拒绝。影响`internal/toolapproval/`。 |
| O3 | 异步A2A的迟到结果，在调用方会话已结束时落在哪？ | 候选：丢弃（符合同步委托语义）／投递到调用方Inbox。这是Phase 2与Phase 4之间唯一需要连接的接缝。 |

## 7. 会话生命周期

A2A会话的生命周期管理（实时输出状态、run状态机、断线恢复、abort传播）由**另一条独立工作线**负责，参见`docs/design/session-runtime-requirements.md`。本系列文档不重复定义这部分内容，Phase 2只描述A2A特有的部分，并假定生命周期能力由该工作线提供。

需要注意：调用链的环检测（A→B→A）**不属于**生命周期问题，它是路由安全问题，归Phase 2负责。默认异步已经把这个问题从「死锁」降级为「洪泛」，但深度与调用链限制仍然必须实现。

## 8. 跨阶段的横切约束

以下约束适用于全部四个阶段。

### 8.1 Prompt injection

共享Wiki与Inbox自动触发意味着：**任何能向共享空间写入内容的实体，都能影响其他Bot的行为。** 典型攻击是向Wiki写入一段伪装成指令的文本，等待其他Agent读到后被劫持。

约束：共享内容进入模型上下文时必须带来源标注，并以数据形式呈现，不得作为指令。A2A消息同理。

### 8.2 工具注册与prompt

遵循项目既有约定：per-tool用法写在`sdk.Tool.Description`，跨工具工作流写在`Usage()`。静态prompt模板**不得**提及任何条件注册的工具——`internal/agent/runtime/native/prompt_test.go`会对此做守卫。

本设计中Wiki工具与`list_teammates`都是条件注册（Bot不属于任何Group时不注册），因此不能出现在静态prompt里。

### 8.3 部署边界

Agent运行在Server进程内，Channel只做外部渠道适配。A2A与Inbox都是Server内部事件，必须使用进程内turn端口，不得绕道Channel的gRPC传输。`internal/arch`的边界守卫测试会强制这一点。

### 8.4 数据库约定

- 新表遵循现有模式：`(team_id, ...)`复合主键、`REFERENCES public.teams(id)`、启用并`FORCE ROW LEVEL SECURITY`、`team_id`默认值取`public.memoh_current_team_id()`。参照`db/postgres/migrations/0112_team_core`与`0124_connect_it`。
- 外键使用复合形式（如`REFERENCES public.bots(team_id, id) ON DELETE CASCADE`），不使用多态关联。
- 每次schema变更同时更新`db/postgres/migrations/0001_init.up.sql`，并提供配对的`.down.sql`。
- 变更后运行`mise run sqlc-generate`。
- 迁移序号从合入时的下一个可用序号开始（当前最新为`0128`）。

### 8.5 成本闸门

Agent Team引入了两条自动消耗模型额度的路径：A2A委托与Inbox触发。两者都必须受团队级速率与预算限制约束，并提供一个「暂停全部自动触发」的紧急开关。这不是可选项。
