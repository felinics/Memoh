# Phase 3：Project——共享知识与协作空间

> 前置阅读：[README.md](./README.md)
> 依赖：无。**Project与Group解耦**（决策D3），本阶段不依赖Phase 1，可与之完全并行。

## 1. 目标

提供共享的知识与协作空间，**人和Agent都是一等使用者**（决策D11）。它承担三件事：

1. 团队的结构化知识沉淀与任务跟踪
2. Bot之间产物交接的载体（见第6节）
3. Inbox的事件源（Phase 4）

这块投入不小，但它是Agent Team中唯一的共享底座——共享工作空间已被排除（README第5节），Project必须补上它留下的缺口。

### 1.1 一个Project内含三类内容

Project是协作单元，内部有三个一等视图：

| 视图 | 内容 | 底层 |
| --- | --- | --- |
| **Wiki** | 层级文档树，Markdown正文与不可变版本 | `type=doc`的节点 |
| **Issues** | 任务与状态跟踪，看板视图 | `type=issue`的节点 |
| **Resources** | 附件与产物，Bot之间的文件交换载体 | 节点附件＋Team级内容寻址 |

三者共享同一棵节点树、同一套版本与审计机制、同一份ACL。**Issues不是独立子系统**，Resources也不是独立的文件系统——它们是同一个数据模型上的三个投影。这样做的收益很具体：一次权限判定覆盖三类内容；一条@提及无论出现在文档、Issue还是评论里都走同一条Inbox路径；产物交接不需要另建一套存储与授权。

> 命名提示：`Project`这个词在代码库中曾被PR #942用于「Bot的工作目录绑定」，该概念已改名为`Workdir`。实现前先确认`internal/project/`已经腾空，并确认根`AGENTS.md`中Team / Group / Project / Workdir四个词的定义已经写入。

### 1.2 Project是一等实体

**一个Team内可以有多个Project，每个Project自带访问控制列表**（决策D3、D13）。

Project**不归属于Group**。两者都是Team之下的独立概念：Group管「谁能看到哪些Bot」，Project管「哪些知识与任务给谁看」。它们唯一的交集是Group可以作为Project ACL的一种授予对象（第4.2节），那是批量授权的便利写法，不构成结构上的从属关系。

这个形状是同类产品的通行做法（Notion teamspace、Confluence space、Linear project），用户的认知成本低：Project是一个有名字、自己建出来的东西，而不是组织结构的投影。

设计演进中曾考虑过另外两种方案，均被否决，记录于此以免反复：

| 曾考虑 | 否决原因 |
| --- | --- |
| 每个Group一个Project | 每建一篇文档都要先选组；跨组引用被禁又造成困惑；Bot多组归属时产生跨组传导风险 |
| 整个Team只有一个Project | 解耦过头，无法表达「这部分知识只给一部分人」 |

「整个Team一个公开Project」在本方案下仍然可以配置出来——建一个Project并授予整个Team即可，能力没有丢失。

## 2. 存储分层

**结构、Markdown正文与版本存Postgres；只有Resources二进制走storage provider抽象**（决策D12）。

文档树、Issue状态、评论、提及、正文版本、引用关系都需要事务、并发控制、审计与回滚。把Markdown正文放进对象存储会让一次编辑跨越Postgres与blob store，无法原子提交；正文体量也不值得引入这层复杂度。因此每个版本的Markdown快照直接存Postgres，只有附件二进制进入对象存储。

| 层 | 内容 | 落点 |
| --- | --- | --- |
| 关系与文本 | Project、ACL、节点树、Issue字段、Markdown版本、评论、提及、引用、附件元数据 | Postgres |
| 二进制 | 附件内容 | storage provider |

现有`internal/storage/providers/`只有`localfs`、`containerfs`、`fallback`。**S3是新增的provider实现，默认仍为localfs**，不得把S3写死进Project领域模型——自部署用户不一定愿意运行MinIO。

### 2.1 Resources内容寻址与一致性

附件按`team_id + content_hash`寻址，在同一Team的多个Project之间去重，但绝不跨Team去重。现有`internal/media/`以`bot_id`作为routing key，不能不加适配就宣称具备跨Project去重；Project需要独立的Team级namespace或对media service做显式扩展。

对象存储与Postgres无法组成同一事务，写入顺序固定为：

1. 流式计算hash并写入临时/最终blob key。
2. 在Postgres事务中插入附件元数据与节点引用。
3. 第2步失败时，blob成为可回收孤儿；后台GC按宽限期清理无引用对象。

删除附件只删除Postgres引用。仅当同一Team内没有任何附件元数据引用该hash时，GC才删除blob。下载必须先通过Project read授权，再由受控handler流式返回或生成短期签名URL；不得把localfs路径或永久对象URL直接暴露给调用方。

## 3. 数据模型

核心表族如下：

| 实体 | 说明 |
| --- | --- |
| project | 协作空间本身；包含`owner_user_id`，是唯一权限边界 |
| 四类ACL关系 | `project_user_acl`、`project_bot_acl`、`project_group_acl`、`project_team_acl`，见第4节 |
| node | 节点树与当前状态；`type`区分`doc`（Wiki）与`issue`（Issues） |
| node_version | 不可变Markdown版本、编辑来源与版本号 |
| comment | 挂在节点上的评论 |
| mention | 从正文或评论中解析出的@提及，分别指向用户或Bot |
| reference | 节点间引用，包括跨Project引用 |
| attachment | 节点的附件元数据，指向Team级内容hash（Resources） |

要点：

- 每个node归属于恰好一个project，`project_id`不可为空。
- 节点树用父子引用表达，支持移动子树。**跨project移动子树等同于变更归属**，必须重新校验源Project与目标Project的write权限。
- `project_nodes`**不携带`group_id`**（决策D3）。权限来自所属Project的ACL，节点本身不带任何权限字段。
- **Wiki与Issues是同一棵树上的两个`type`**，不是两套结构。看板视图是对`type=issue`节点按status的投影，文档树视图是对`type=doc`节点按父子关系的投影。
- `project_node_versions`以`(project_id, node_id, version)`唯一；每次写入插入不可变快照，并在同一Postgres事务中更新node的当前版本指针。
- 每个版本记录编辑者user或Bot、来源Session与run；回滚通过创建一个内容等于历史版本的新版本完成，不修改历史行。
- 所有“user或Bot”引用都使用两列真实外键加exactly-one CHECK，例如`editor_user_id`/`editor_bot_id`、`mentioned_user_id`/`mentioned_bot_id`；不得退化成`actor_type + actor_id`多态关联。人类编辑时Session与run可以为空，Bot编辑时必须填写。
- 全部表遵循README第8.4节的约定，携带`team_id`并启用RLS。

## 4. 权限

### 4.1 权限边界只到Project一级

**不做节点级访问控制，也不做按视图的访问控制。** Project本身就是权限边界；需要不同权限就再建一个Project。

这是本节最重要的一条约束。节点级ACL会让查询逐节点判权、树上出现空洞，并迫使面包屑与搜索结果增加特殊处理。按视图授权（「只给Issues不给Wiki」）看起来诱人，但它会让一条@提及的可达性取决于它出现在哪个视图里，Inbox的权限校验随之变成三份，收益不抵复杂度。一次权限判定只发生在Project层，判定通过后Wiki、Issues、Resources三个视图一致可访问。

### 4.2 四类ACL关系与read/write位

不使用`principal_type + principal_id`多态外键。四类主体分别使用四张关系表，每张表都有`can_read`与`can_write`两个布尔位：

| 表 | 主体 | 关键外键 |
| --- | --- | --- |
| `project_user_acl` | 单个Team成员 | `(team_id, user_id) → team_members` |
| `project_bot_acl` | 单个Bot | `(team_id, bot_id) → bots` |
| `project_group_acl` | Group的全部人类成员 | `(team_id, group_id) → groups` |
| `project_team_acl` | 当前整个Team | `team_id → teams`；每个Project至多一行 |

共同约束：

- 每个Project与主体最多一行。
- `can_write=true`时`can_read`也必须为true。
- 两个位都为false的行没有意义，必须删除而不是保留。
- 有效权限取所有匹配关系的并集；任一关系给write即有write，任一关系给read即有read。首版没有deny规则。
- `group`只展开该Group的人类成员，不展开Bot；Bot必须通过`project_bot_acl`显式授权。
- 删除Group只级联删除`project_group_acl`，不影响Project及内容。

Phase 3核心可以先实现user、bot与team三类关系；`project_group_acl`在Phase 1可用后作为薄集成层加入。Project的结构和其他查询路径仍不依赖Group。

### 4.3 所有权与管理

read/write ACL不承载管理权限。每个Project有一个非空`owner_user_id`，创建者默认成为owner；该字段通过`(team_id, owner_user_id)`引用`team_members`并使用`ON DELETE RESTRICT`，移除owner前必须先转移Project。

只有Project owner和Team admin可以修改ACL、转移owner、重命名或删除Project。Team admin是恢复入口，因此无需在ACL中增加`manage`级别，也不存在“删除最后一个manager”的状态。

### 4.4 已知且已接受的传导语义

Bot拥有独立于人的Project权限。Project P授权给Bot B、但没有授权给Alice时，Alice与B对话仍可能从B的回答中得到P的内容。**这是Bot作为独立能力主体的既定语义，不取Bot与当前对话人的权限交集。** heartbeat、schedule和A2A也始终按Bot自己的ACL执行。

授予Bot Project权限的界面必须明确提示“该Bot可以在其正常工作中使用并转述这些内容”，并展示其可达范围。可枚举的owner、直接授权与Group成员应列出；若Bot的渠道ACL允许任意访客或未绑定渠道身份，必须显示“任意渠道访客”等范围描述，不能伪装成一份完整的人名列表。

### 4.5 工具注册

Project工具的注册条件是「该Bot至少对一个Project有read授权」，与Group成员关系无关。Bot不属于任何Group时，只要有Project授权，工具**照常注册**。

Bot侧不再需要独立的「是否允许写共享空间」开关——该能力完全由ACL表达，不授权即不能写。

### 4.6 跨Project引用

**允许**跨Project的链接与引用，渲染时按读者的权限决定是否可见：无权限时只显示通用的“不可访问引用”，不得泄漏目标标题、路径、类型或内容。

这一点与早期per-group方案相反——那一版禁止跨组引用，而该禁令正是当时困惑的主要来源。Project是显式的、有名字的实体，跨Project引用对用户是可理解的。

## 5. Agent工具集

不要提供「读取整个Project」的工具。工具集应为：

| 工具 | 说明 |
| --- | --- |
| 列出可访问的Project | 返回该Bot有授权的Project及`can_read`/`can_write` |
| 搜索 | **跨全部有read授权的Project**，同时覆盖Wiki与Issues，结果标注来源Project与节点type。可复用`internal/memory/`已有的Qdrant与BM25基础设施 |
| 按路径读取 | 读单个节点 |
| 局部编辑 | 见第5.2节 |
| 创建节点 | 在指定父节点下新建`doc`或`issue`；新建顶层节点时需指定Project，缺省用默认Project（第5.1节） |
| 更新Issue状态 | 改`type=issue`节点的status等字段。走与正文编辑同一条写路径与同一把乐观锁，不是旁路 |
| 评论 | 在节点上发表评论，可@他人 |
| Resources发布与拉取 | 见第6节 |

工具集按能力切分，不按视图切分：不要出现`wiki_read`与`issue_read`两个只差一个`type`过滤的工具，模型会在选择上浪费token并选错。

### 5.1 默认Project

Bot需要一个默认Project设置，否则「把这个记一下」每次都要先做一次选择。

- 该设置是`bots`上的一个字段，取值必须是该Bot有write授权的Project之一。
- 授权被撤销导致默认值失效时，行为必须明确：降级为「必须显式指定」，而不是静默写入别处。

### 5.2 并发写必须有冲突检测

Agent是会并行的。所有编辑接口在读取时返回`version`，写入时必须携带`expected_version`。服务端在同一Postgres事务中锁定node、比较版本、插入不可变`project_node_versions`行并更新当前版本指针；版本不一致时返回稳定的冲突结果，让Agent重读再改。

编辑API可以支持Anchor级patch以减少传输和冲突面，但patch仍必须带`expected_version`，不能绕过乐观锁。Issue的状态变更同样走这条路径——两个Agent同时改同一个Issue的status必须有一方收到冲突。

这一点的重要性高于表面观感——它是Project能否被多个Agent同时使用的前提。

## 6. Resources：共享工作空间的替代

共享工作空间被排除后，「Bot A产出一个文件给Bot B使用」这个需求失去了载体。Resources必须补上，因此需要一对工具：

- 把workspace中的文件发布到Project（作为节点附件）
- 把Project附件拉取进自己的workspace

没有这两个，Bot之间就只能靠A2A传纯文本，稍大的产物无法交接。**这是排除共享工作空间之后必须补齐的最小闭环**，不是可选增强。

多Project带来一个新的前提条件：**交接双方必须对同一个Project有授权**（发送方需write，接收方需read）。这是配置问题而非设计缺陷，但产品上需要有可读的失败反馈——Bot尝试向对方无权访问的Project投放产物时，错误信息必须指出缺少的是哪一侧的授权，而不是笼统的「没有权限」。

## 7. 与长期记忆的边界

这是最容易职责重叠的地方，必须划死：

| | Project | 长期记忆（`internal/memory/`） |
| --- | --- | --- |
| 可见性 | 显式、人可读 | 隐式 |
| 编辑 | 人和Agent都能改 | 自动抽取 |
| 归属 | Project内共享，按ACL授权 | per-bot（决策D5） |
| 审计 | 有 | 无 |

规则：

- **记忆不自动写入Project。** 否则Bot会把私有记忆泄漏到共享空间。
- Project可以作为记忆检索的一个可选来源。

不划清楚的后果是：同一份事实在多个Bot的私有记忆中各存一份互相矛盾的副本。

## 8. 审计与回滚

Agent修改Project必须留痕：谁改的、哪个Session与run改的、版本前后是什么。`project_node_versions`保存不可变Markdown快照；diff在两个版本之间计算，人类回滚时创建一个内容等于目标历史版本的新版本，不覆盖或删除既有审计记录。Issue的状态变更同样进入版本历史，否则「谁把这个任务关掉的」无法追溯。

这是信任的前提。没有它，人类无法放心让Agent写入共享空间。

## 9. 明确不做的事

| 不做 | 原因 |
| --- | --- |
| block级富文本编辑器 | 工作量以月计，与Agent协同主线无关 |
| 实时协同编辑（CRDT） | 同上。并发问题由第5.2节的乐观锁解决 |
| 独立的Issue子系统 | Issue是node的一个type，与Wiki共享树、版本与ACL |
| 按视图授权（只给Issues不给Wiki） | 第4.1节。权限边界只到Project一级 |
| 按Group划分Project | Project与Group解耦（D3）。Group只能作为ACL主体出现 |
| **节点级访问控制** | 第4.1节。需要不同权限就再建一个Project |
| 有效权限取Bot与对话人的交集 | 第4.4节。Bot是独立能力主体，始终按自己的ACL执行 |
| 把结构存进对象存储 | 第2节 |
| Issue的工作流引擎、自定义字段、依赖图 | 首版收敛到status与看板投影。做深之前先确认Agent真的在用 |

## 10. 验收要求

### PROJ-001：结构与内容分层

- 列目录、移动子树、改标题必须是Postgres上的操作，不得触发对象存储的遍历。
- Markdown正文及其不可变版本必须存Postgres，并与当前版本指针原子提交。
- 对象存储只能承载Resources二进制，不得承载Project结构或Markdown正文。
- 更换storage provider（localfs↔S3）不得影响结构层行为。
- 默认配置下不依赖S3。

### PROJ-001a：三个视图共用一套底层

- Wiki与Issues必须是同一张节点表上`type`不同的行；不得存在第二套节点树或第二套版本表。
- Issue的状态变更必须进入`project_node_versions`，可被审计与回滚。
- 搜索必须一次覆盖Wiki与Issues，结果标注来源Project与节点type。

### PROJ-002：权限

- 无read授权的用户或Bot访问Project内任意节点必须被拒绝，不得泄漏节点标题、路径、类型或内容。
- 仅有read授权者发起写操作必须被拒绝。
- ACL必须分别落在user、bot、group、team四张关系表；不得使用无外键保障的多态`principal_id`。
- `can_write=true`必须蕴含`can_read=true`，两者都为false的记录必须被拒绝或删除。
- 仅有write授权者修改ACL、转移owner或删除Project必须被拒绝；只有Project owner或Team admin可以执行管理操作。
- 通过`group`主体获得的授权，必须在该用户退出Group后立即失效。
- 删除Group必须只移除对应的授权条目，Project本身及其内容必须不受影响。
- `group`授权不得隐式扩展到Group中的Bot。

### PROJ-002a：权限边界只到Project一级

- 同一Project内不得存在任何节点级权限差异：有read授权者必须能看到该Project的全部节点。
- **三个视图必须共用同一次ACL判定**：不得出现对Wiki有权限而对Issues或Resources没有的状态。
- Project的查询路径除ACL解析外不得引用Group表。
- 该项需要有测试守卫，防止后续实现引入节点级ACL、按视图授权或按Group分区。

### PROJ-002b：跨Project引用

- 跨Project链接必须允许创建。
- 读者对目标Project无授权时，引用必须渲染为通用不可访问状态，不得泄漏目标标题、路径、类型或内容。

### PROJ-003：并发写

- 两个Agent基于同一版本并发修改同一节点时，后提交者必须收到冲突而不是静默覆盖。
- 冲突返回给Agent的信息必须足以让它重读并重试。
- Anchor patch与Issue状态变更也必须校验`expected_version`，不得绕过版本冲突检测。

### PROJ-004：Resources交换

- Bot必须能把自己workspace中的文件发布为Project附件。
- 另一个有权限的Bot必须能把该附件拉取进自己的workspace。
- 下载必须经过Project read授权，不得泄漏localfs路径或永久对象URL。
- 同一Team内相同hash可以去重，不同Team之间必须使用隔离namespace。

### PROJ-005：审计

- 每次由Agent发起的修改必须记录发起Bot、Session与run。
- 必须能查看任意两个不可变版本之间的diff。
- 回滚必须创建新版本，不得重写或删除历史版本。

### PROJ-006：记忆边界

- 记忆抽取流程必须不会写入Project。
- 该项需要有测试守卫，而不仅是约定。

### PROJ-007：Resources一致性与回收

- blob写入成功但Postgres事务失败时不得产生可访问附件；孤儿blob必须在宽限期后由GC回收。
- 删除一个Project或附件引用时，不得删除仍被同一Team其他Project引用的blob。
- GC删除前必须在Postgres中再次确认该Team内引用数为零，并可安全重试。
