# Agent Team设计总览

## 1. 文档目的

Memoh目前允许创建多个Bot，但Bot之间彼此不可见、不能协作。本系列文档描述如何把「多个孤立的Bot」变成「一个可以协同工作的Agent团队」。

本文是总览，负责说明范围、术语、已定的决策，以及阶段之间的依赖关系。设计层面已无待定项，具体设计在分阶段文档中：

| 阶段 | 文档 | 内容 |
| --- | --- | --- |
| Phase 1 | [01-group.md](./01-group.md) | Group模型：可选成员关系、增量权限、兼容迁移 |
| Phase 2 | [02-agent-to-agent.md](./02-agent-to-agent.md) | Agent之间的一对一通信 |
| Phase 3 | [03-project.md](./03-project.md) | Project：共享知识与协作空间，内含Wiki／Issues／Resources三个视图，每个Project一份ACL |
| Phase 4 | [04-inbox.md](./04-inbox.md) | Inbox投递与事件驱动触发 |

本系列文档描述的是设计决策与验收标准，不是逐步骤的执行计划。文中「必须」表示合入条件，「可以」表示实现自行选择但选择后行为必须可观察、可测试。

## 2. 阶段依赖

四个阶段构成两条彼此独立的链：

```
Phase 1  Group模型  ──▶  Phase 2  A2A通信
              提供成员关系 → 同事发现与授权

Phase 3  Project   ──▶  Phase 4  Inbox
              提供@提及事件
```

- 两条链之间没有依赖，可以完全并行推进。
- Phase 2依赖Phase 1的`group_bot_members`。
- Phase 4依赖Phase 3：Inbox在本方案中的唯一事件源是Project内的@提及。
- **Project与Group解耦**（决策D3）：Project是与Group平行的独立实体，权限由自身ACL决定，因此Phase 3不依赖Phase 1。唯一的交叉是「Group作为ACL主体」，属于可后置的增量，两条链都落地后再加即可。

## 3. 术语

| 术语 | 含义 |
| --- | --- |
| Team | 租户与隔离边界。承载RLS、计费与数据隔离。开源自部署版本永远只有`internal/team/id.go`里的`DefaultTeamID`一个Team。 |
| Group | Team之下可选的协作与授权分组。为人类增加Bot访问，为Bot增加同事发现与A2A授权。Bot可以不属于任何Group；Group不拥有Project，也不是隔离边界。 |
| Project | Team之下的独立一等实体，可以有多个。**它自身就是权限边界**，通过ACL授予user/bot/group/team。与Group平行，互不从属。内部有Wiki、Issues、Resources三个视图。 |
| Wiki / Issues / Resources | 一个Project内部的三个视图，不是独立实体：Wiki是`type=doc`的节点树，Issues是`type=issue`节点的看板投影，Resources是节点附件。三者共享同一棵树、同一套版本与同一份ACL。 |
| Workdir | **与本设计无关，仅用于消歧。** Bot名下的`(workspace target, 绝对目录)`绑定，会话创建时不可变绑定（PR #942）。该概念原名`Project`，为给本设计的Project让路已改名。 |
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
| D2 | 一个Bot可以不属于任何Group，也可以属于多个Group；不存在Default Group。 | Phase 1 |
| D3 | **Project与Group解耦**：Project是独立的一等实体，一个Team内可以有多个，各自带ACL。Group不拥有Project，只能作为ACL的一种授予主体。 | Phase 1 / Phase 3 |
| D4 | Group对人类只增量授予Bot可见性与`chat`，不授予workspace或manage；对Bot提供A2A授权。它不替换直接授权，也不决定知识可见性。 | Phase 1 |
| D5 | 长期记忆保持per-bot，不按Group分区。 | Phase 1 |
| D6 | Session不携带`group_id`。chat、channel inbound、heartbeat、schedule等入口只需要`bot_id`。 | Phase 1 |
| D7 | A2A的调用方是工具（句柄式，体感与Subagent一致），被调方走**正常Bot的turn路径**，不复用`SpawnProvider`的执行链。 | Phase 2 |
| D8 | A2A产生的Session挂在**被调Bot**名下，`parent`指向调用方会话。 | Phase 2 |
| D9 | A2A默认异步，同时支持同步等待。 | Phase 2 |
| D10 | A2A使用独立的session mode与system prompt，与Subagent分开。 | Phase 2 |
| D11 | Project面向人和Agent双方使用，不是仅供Agent读取的内部存储。 | Phase 3 |
| D12 | Project结构、Markdown正文与不可变版本存Postgres；只有Resources二进制走storage provider。S3是新增provider，默认仍为localfs。 | Phase 3 |
| D13 | Project权限边界只到Project一级，不做节点级也不做按视图的访问控制。user/bot/group/team分别使用四张ACL关系表，每类只有`can_read`/`can_write`；owner与Team admin负责管理。 | Phase 3 |
| D14 | Inbox不承载A2A。A2A走工具直连，Inbox只处理事件驱动的投递（Project内@提及、人类通知）。 | Phase 4 |
| D15 | 保留现有discuss模式（`internal/channel/discuss/`），它是面向渠道群聊的特性，与Agent Team定位不同，不合并、不移除。 | 全局 |
| D16 | A2A会话中被调Bot调用`ask_user`时，请求**路由回调用方Agent**，由调用方作答。 | Phase 2 |
| D17 | A2A会话中被调Bot触发工具审批时，由**被调Bot的归属人类**（`bots.owner_user_id`）审批。 | Phase 2 |
| D18 | 异步A2A的结果在发起它的`caller_run_id`已终止时不再自动投递，也不进入Inbox；委托与被调方执行记录仍保留。 | Phase 2 / Phase 4 |
| D19 | A2A是明确的能力委托：被调Bot按自己的工具、Project与workspace权限执行，不与原始发起人的权限取交集；on-behalf-of只用于来源与审计。 | Phase 2 |
| D20 | Inbox采用事务Outbox、持久化delivery、至少一次投递与幂等效果；worker通过可过期lease恢复，多次失败进入可查询死信。 | Phase 4 |
| D21 | Wiki、Issues、Resources是一个Project内的三个视图，共享同一棵节点树、同一套版本与同一份ACL；Issue是node的一个`type`，不是独立子系统。 | Phase 3 |

## 5. 明确排除的范围

以下内容经讨论后决定**不做**，实现时不要顺手加上：

| 排除项 | 原因 |
| --- | --- |
| 共享工作空间（多Bot挂载同一个卷） | 多个Agent并发写同一文件没有锁与冲突检测，会静默损坏；三种容器backend语义不一致；remote runtime下不成立；配额与快照语义无法定义。收益小于复杂度。**连带要求见Phase 3第6节：Project的Resources必须承担Bot之间的文件交换。** |
| Agent之间的群聊 | 一对一是默认形态。公共异步空间由Project承担。 |
| block级富文本编辑器与实时协同（CRDT） | 工作量以月计，且与Agent协同主线无关。Phase 3收敛到节点＋Markdown正文＋评论＋提及。 |
| Project按Group划分 | 每建一篇文档都要先选组，跨组引用被禁又造成困惑。改为Project自身即权限边界（D3）。 |
| Project的节点级访问控制 | 查询要逐节点判权、树上出现空洞、用户无法解释自己为什么打不开某页。需要不同权限就再建一个Project。见`03-project.md`第4.1节。 |
| 按视图授权（只给Issues不给Wiki） | 会让一条@提及的可达性取决于它出现在哪个视图里，Inbox的权限校验随之变成三份。权限边界只到Project一级（D13、D21）。 |
| Project有效权限取Bot与对话人的交集 | Bot是独立能力主体，heartbeat、schedule、A2A与人类对话都按Bot自己的ACL执行。授予界面明确披露可达范围，见`03-project.md`第4.4节。 |
| Issue的工作流引擎、自定义字段、依赖图 | 首版收敛到status与看板投影。做深之前先确认Agent真的在用。 |
| 记忆按Group分区 | 见D5。Group不决定知识可见性，单独限制记忆没有意义。 |
| 为人类新建一套独立收件箱 | 人类已有`user_channel_bindings`。Inbox对人落成Web通知与既有渠道推送。 |

## 6. 已解决的待定项

设计过程中曾有三项待定，现已全部确定为D16–D18。保留本节记录候选方案与取舍理由，以免实现阶段重新讨论。

| 原编号 | 问题 | 结论 | 落点 |
| --- | --- | --- | --- |
| O1 | 被调Bot调用`ask_user`时路由给谁？ | **回调用方Agent**（D16）。A2A因此是可来回对话的，而不只是单次委托。 | `02-agent-to-agent.md`第8.1节 |
| O2 | 被调Bot触发工具审批时谁来批？ | **被调Bot的归属人类**（D17）。另两个候选——限制A2A工具集、直接拒绝——都会让被调Bot的能力与它被人类叫醒时不一致，违背D7。 | `02-agent-to-agent.md`第8.2节 |
| O3 | 迟到的异步结果落在哪？ | `caller_run_id`终止后不再投递（D18）。持久化委托与被调执行记录保留，但不唤醒新run、不进入Inbox。 | `04-inbox.md`第6节 |

三项共同决定`mode_agent.md`与持久化委托状态机；D16、D18要求委托记录关联双端run并保存等待/终态，不能只写在prompt里。

## 7. 会话生命周期

A2A会话的通用生命周期管理（实时输出状态、run状态机、断线恢复、abort传播）由Session Runtime负责，参见`docs/design/session-runtime-requirements.md`。Phase 2直接依赖其`run_id`、决策持久化与恢复能力，同时自己持久化A2A委托、双端run关联、澄清轮次与最终结果；现有进程内background Manager不能作为权威状态源。

需要注意：调用链的环检测（A→B→A）**不属于**生命周期问题，它是路由安全问题，归Phase 2负责。默认异步已经把这个问题从「死锁」降级为「洪泛」，但深度与调用链限制仍然必须实现。

## 8. 跨阶段的横切约束

以下约束适用于全部四个阶段。

### 8.1 Prompt injection

共享Project与Inbox自动触发意味着：**任何能向共享空间写入内容的实体，都能影响其他Bot的行为。** 典型攻击是向某个Project写入一段伪装成指令的文本（文档正文、Issue描述或评论都可以），等待其他Agent读到后被劫持。

约束：共享内容进入模型上下文时必须带来源标注，并以数据形式呈现，不得作为指令。A2A消息同理。

### 8.2 工具注册与prompt

遵循项目既有约定：per-tool用法写在`sdk.Tool.Description`，跨工具工作流写在`Usage()`。静态prompt模板**不得**提及任何条件注册的工具——`internal/agent/runtime/native/prompt_test.go`会对此做守卫。

本设计中Project工具与`list_teammates`都是条件注册：前者取决于Bot是否至少拥有一个Project的read权限，后者取决于是否存在共享Group的可联系Bot。因此它们都不能出现在静态prompt里；无Group Bot仍可能正常注册Project工具。

### 8.3 部署边界

Agent运行在Server进程内，Channel只做外部渠道适配。A2A与Inbox都是Server内部事件，必须使用进程内turn端口，不得绕道Channel的gRPC传输。`internal/arch`的边界守卫测试会强制这一点。

### 8.4 数据库约定

本方案新增的表不少，务必先读这一节。以下规则由`internal/db/team_schema_guard_integration_test.go`（`-tags=integration`）机械强制，**普通单测与lint都不会报错**。

- 新表模板直接抄`db/postgres/migrations/0121_session_runs.up.sql`：`(team_id, ...)`复合主键、`REFERENCES public.teams(id)`、启用并`FORCE ROW LEVEL SECURITY`、`team_id`默认值取`public.memoh_current_team_id()`。
- 外键使用复合形式（如`REFERENCES public.bots(team_id, id) ON DELETE CASCADE`），不使用多态关联。
- **用户引用不指向`users(id)`**，而是`FOREIGN KEY (team_id, col) REFERENCES team_members(team_id, user_id)`。若用`ON DELETE SET NULL`必须带列名单（`SET NULL (col)`）——守卫会把任何`confdelsetcols IS NULL`的SET NULL外键判为不安全。
- **给已启用FORCE RLS的既有表（如`bot_sessions`）ADD CONSTRAINT外键时，增量迁移必须加`NOT VALID`**：校验扫描会评估RLS策略，而迁移角色没有设置`memoh.team_id` GUC，会直接报`memoh.team_id is not set`。迁移中确需读写RLS表时，参照`0120`的`DO`块`set_config('memoh.team_id', ...)`模式。
- **给既有表加列时，`0001_init.up.sql`的同步不能内联进`CREATE TABLE`**，要以`ALTER`形式追加到文件末尾，否则全新安装与增量升级的物理列序不一致，sqlc的`SELECT *`位置扫描会错位。
- 每次schema变更同时更新`0001_init.up.sql`，并提供配对的`.down.sql`。
- 变更后运行`mise run sqlc-generate`。
- 迁移序号从合入时的下一个可用序号开始（当前最新为`0128`）。

验证：

```bash
TEST_POSTGRES_DSN="postgres://memoh:memoh123@localhost:15432/memoh?sslmode=disable" \
  go test -tags=integration ./internal/db/
```

### 8.5 成本闸门

Agent Team引入了两条自动消耗模型额度的路径：A2A委托与Inbox触发。两者都必须受团队级速率与预算限制约束，并提供一个「暂停全部自动触发」的紧急开关。这不是可选项。
