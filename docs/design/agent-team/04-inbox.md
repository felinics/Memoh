# Phase 4：Inbox

> 前置阅读：[README.md](./README.md)、[03-project.md](./03-project.md)
> 依赖：Phase 3的Project内@提及事件。

## 1. 职责范围

Inbox是**事件驱动的投递层**。当共享空间中发生了与某个成员相关的事件时，把它送到该成员手上。

本设计中Inbox的事件源只有一个：**Project中的@提及**——Wiki文档正文、Issue描述与任意节点上的评论，三处走同一条路径。

由于Project有多个且各自带ACL（见[03-project.md](./03-project.md)第4节），提及必须受权限约束：**只能@对该Project有read授权的用户或Bot**，自动补全也只列出这些主体。否则会产生一条指向对方打不开的内容的通知。权限判定只在Project一级发生，因此@提及出现在哪个视图里不影响可达性。

**A2A不走Inbox**（决策D14）。分工规则：

| 场景 | 机制 |
| --- | --- |
| 有人在等结果 | `contact_agent`工具（Phase 2），调用方持有句柄 |
| 事件驱动，没人在等 | Inbox |

这条切分让Inbox的复杂度大幅下降——去重、攒批、防风暴这些机制只需服务Project内@提及这一个场景，量级远小于承载全部Agent间通信。

## 2. 投递模型

Inbox对两类成员的落地方式不同：

| 收件人 | 投递结果 |
| --- | --- |
| Bot | 触发一次Session处理 |
| 人类 | Web通知中心＋按`user_channel_bindings`推送到既有渠道 |

**不为人类新建一套独立收件箱**（README第5节）。人类已经有Telegram、邮件、Web等渠道绑定，Inbox只是统一的投递模型，不是新的UI概念。

### 2.1 事务Outbox与持久化Delivery

Project内的正文或评论写入时，mention记录与`project_outbox_events`必须在同一个Postgres事务中提交。不得先提交mention再向进程内队列发消息，否则Server在两步之间退出会永久丢通知。

dispatcher以至少一次方式消费outbox，并为每个收件人创建持久化delivery。实现可以使用一张带`recipient_user_id`和`recipient_bot_id`两列且exactly-one CHECK的表，也可以拆成两张表；两类引用都必须有真实复合外键，不能使用无外键的多态ID。

每条delivery至少包含：

- `mention_id`、收件人、`thread_key`与幂等键
- `pending / leased / delivered / retry / dead / cancelled`状态
- `available_at`、lease owner/expiry、尝试次数与最后稳定错误code
- 触发链root、parent delivery与传播深度
- 对Bot收件人的目标Session/run，对人类收件人的Web通知与渠道投递状态

数据库唯一约束保证同一`mention_id + recipient`只产生一条逻辑delivery。worker通过有过期时间的lease领取；进程退出后其他实例可以重领。

## 3. Bot侧的触发控制

「收到消息就新建Session」如果不加约束，会立刻失控。以下四项都是必需的。

### 3.1 幂等

同一条提及只能触发一次。需要幂等键（提及ID＋收件人）并持久化，重复投递必须被吞掉。

### 3.2 攒批

不是每条消息都值得单独起一个Session。一篇活跃文档会在几分钟内产生大量提及。

必须支持攒批：短时间内的多条Inbox消息合并成一次处理，像人查收邮件一样。可配置的模式：

- 即时
- 攒批（N分钟或M条）
- 仅在heartbeat时查收

第三种直接复用现有heartbeat run——`internal/heartbeat/service.go`已经按cron创建heartbeat Session并触发执行，「定时查收Inbox」是它的输入来源之一，不再额外创建Inbox Session。选择该模式前必须确认Bot已启用heartbeat；未启用时配置保存应失败，而不是让消息无限滞留。

攒批窗口与条数阈值必须落在持久化delivery上，由可恢复worker领取；不得依赖进程内timer。一个批次被同一run接纳后，批内delivery共同记录该run ID，重复领取不得再创建第二个run。

### 3.3 会话复用

同一话题的后续消息必须进入**同一个Session**，否则Bot每次都失忆。

做法：Inbox消息携带`thread_key`（例如`project:issue:123`），通过持久化的Inbox thread route按`bot_id + thread_key`路由到已存在的Session；超时或超长后才开新的。

这与`bot_channel_routes`（外部会话到内部Thread的路由）是同一个模式，应当复用其思路而不是另造一套。

### 3.4 风暴控制

一条评论@了三个Bot，每个Bot起一个Session，每个Session又在Project上回复并@别人——这是指数爆炸。必须具备：

- 提及传播的深度上限
- 每个Bot的并发上限与队列
- 冷却窗口

传播深度不能靠解析文本猜测。人类直接产生的mention深度为0；由Inbox触发run写回Project时，新的mention必须携带来源delivery，沿`root_delivery_id / parent_delivery_id / depth`递增。缺少有效来源时fail closed为新的人工根事件，不能继承模型声称的深度。

## 4. 送达语义（D20）

采用**至少一次投递＋幂等效果**，不承诺跨数据库、模型调用和外部渠道的端到端恰好一次。

- outbox与delivery都可重复领取；唯一键和稳定`invocation_id`保证同一delivery不会创建两个逻辑run。
- 临时失败进入`retry`，按指数退避与抖动更新`available_at`；重试上限可配置。
- 超过上限进入`dead`，保留稳定错误code、诊断摘要与全部尝试记录，可在管理界面查询并人工重试。
- worker使用lease而不是永久`processing`标记；lease过期后可被其他实例接管。
- ACL在消费前再次校验。授权已撤销时delivery进入`cancelled`，不得启动Bot run或继续渠道推送。
- 对外渠道发送使用delivery ID派生幂等键；渠道本身不支持幂等时，状态必须明确为“可能重复”，不能声称恰好一次。

## 5. 可见性与刹车

自动触发意味着Bot会在没有人盯着的时候自己开始工作并消耗额度。因此：

### 5.1 跨Bot的团队活动视图

人类必须能看到「某个Bot因为一条Inbox消息自己开了个Session做了些事」，并且能够审阅与中止。

这要求Web侧有一个**跨Bot的活动视图**，而不只是每个Bot各自的会话列表。

### 5.2 成本闸门

按README第8.5节，必须提供：

- 团队级的速率与预算限制
- 「暂停全部自动触发」的紧急开关

这不是可选项。

## 6. 与Phase 2没有接缝（D18）

设计过程中曾存在一个可能的连接点：异步A2A调用的结果在发起它的`caller_run_id`已经终止后落在哪里。候选方案之一是投递到调用方的Inbox。

**结论是直接丢弃**，因此这个接缝不存在。理由：

- 投递到Inbox会让Inbox多出一个事件源，与D14确立的分工规则（有人等结果走工具、事件驱动走Inbox）冲突。
- 委托结果只属于发起它的run；该run不再接收结果时，不应自动唤醒新的run。
- 丢弃的是投递，不是执行记录：被调Bot的会话与输出仍然完整持久化，人类可以在它的会话列表中查看。

**因此本阶段的事件源仍然只有Project内@提及一个**，第1节的分工规则不需要修订。任何实现都不得为A2A结果新增Inbox投递路径。

## 7. 验收要求

### INBOX-001：幂等

- 同一条提及重复投递必须只触发一次处理。
- 该保证必须跨进程重启有效。
- mention与outbox事件必须在同一Postgres事务中提交；故障注入不得出现有mention无event的状态。
- 重复消费同一outbox事件必须命中同一条delivery，不得重复创建记录。

### INBOX-002：攒批

- 攒批模式下，窗口内的多条提及必须合并为一次Session处理。
- heartbeat模式下，提及必须在下一次heartbeat时被查收，且不产生额外的Session。
- Bot未启用heartbeat时，保存heartbeat收取模式必须失败并返回可读原因。
- Server在攒批窗口中重启后，窗口内delivery必须仍能被领取。

### INBOX-003：会话复用

- 同一`thread_key`的后续提及必须进入同一个Session。
- 超时或超长后开新Session的行为必须可配置且可观察。
- 双Server并发处理同一`bot_id + thread_key`时，不得各自创建一个active Session。

### INBOX-004：风暴控制

- 构造一条@多个Bot、且Bot之间互相@的提及链，系统必须在深度上限处停止，不得无限扩散。
- 单个Bot的并发处理数必须不超过配置上限。
- Bot写回产生的新mention必须保留root、parent与depth；重试同一delivery不得增加传播深度。

### INBOX-005：送达

- 处理失败必须按配置重试，超过次数后进入死信并可查询。
- 死信不得静默丢弃。
- worker持有lease时退出，lease过期后其他实例必须能接管。
- 同一delivery被重复领取时，稳定`invocation_id`必须阻止重复模型执行。

### INBOX-006：可见性与刹车

- 由Inbox自动触发的Session必须在跨Bot活动视图中可见，并标明触发来源。
- 人类必须能中止一个自动触发的Session。
- 紧急开关开启后，必须不再产生任何新的自动触发Session；已在执行的Session的处理方式必须明确定义。

### INBOX-007：提及受权限约束

- @对目标Project无read授权的用户或Bot必须被拒绝，自动补全也不得列出他们。
- 授权在提及产生之后被撤销时，对应的Inbox条目必须不再可打开。

### INBOX-008：人类侧

- 人类收到的提及必须出现在Web通知中心。
- 配置了渠道绑定的用户必须同时收到渠道推送。
- 不得为人类创建新的独立收件箱页面。
