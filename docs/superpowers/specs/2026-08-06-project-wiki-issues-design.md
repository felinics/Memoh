# Project 首版设计：Wiki 与 Issues

> 状态：设计已确认，待实现
> 范围：Team 级 Project 实体，内含 Wiki（树状文档）与 Issues（扁平看板）两个视图
> 关联：[PR #937 Agent Team 设计](https://github.com/memohai/Memoh/pull/937) 的 Phase 3。本文是其**首版落地子集**，刻意收窄了范围（无权限、无 Resources），其余决策与 #937 保持一致。

## 1. 范围

### 1.1 做什么

一个 Team 级的共享协作空间。一个 Team 内可以有多个 Project，每个 Project 内部有两个并列的视图：

```
Project
├── Wiki    → 树状文档，一个 doc 既可以是文档也可以是父节点
└── Issues  → 一个扁平看板，四列状态
```

**Wiki 与 Issues 是 Project 下并列的两块**，树只存在于 Wiki 内部。一个 Project 有且只有一个 issue 集合，不存在「多个看板」。

首版的成功标准是**人在 Web 上能用起来**：建 Project、写文档树、管看板。

### 1.2 明确不做

| 不做 | 原因 |
| --- | --- |
| 权限 / ACL | 首版所有 user 和 bot 默认可见可写。表结构不预留 ACL 列，将来按 #937 的四张关系表另行增量 |
| Resources（附件二进制） | 连带：**首版文档内不支持贴图**，只能写外链图片 URL |
| Agent 工具 | 首版无 Bot 读写路径。数据模型为其预留了字段（见 3.6） |
| 所见即所得编辑器 | 用 Markdown 源码 + 预览，复用已有的 `monaco-editor` 与 `markstream-vue` |
| 版本 diff 与回滚 UI | 快照照存，首版 UI 只做历史列表与查看旧版 |
| 跨 Project 拖拽移动子树 | 见 6.3 |
| Issue 子任务 / 工作流引擎 / 自定义字段 | 首版收敛到状态与看板投影 |
| @提及与 Inbox | #937 的 Phase 4，本版不涉及 |
| 全文搜索的分词优化 | 用 `pg_trgm` 子串匹配，见 3.3 |
| 回收站 UI | 软删除数据可恢复，但界面上没有入口 |

## 2. 架构与包边界

沿用仓库既有的领域包模式（对标 `internal/bots/`、`internal/workdir/`）：

| 层 | 落点 | 职责 |
| --- | --- | --- |
| SQL | `db/postgres/queries/projects.sql` | sqlc 输入，生成到 `internal/db/postgres/sqlc/` |
| 领域 | `internal/project/` | Project / 节点树 / Issue / 评论的业务规则，不认识 HTTP |
| HTTP | `internal/handlers/project.go` | REST + swaggo 注解 |
| 前端 | `apps/web/src/components/project-panel/` 与 `pages/home/components/dockview/` | 导航面板与标签页 |
| SDK | `packages/sdk/` | 由 `mise run swagger-generate && mise run sdk-generate` 产出 |

`internal/project/` 内部按实体切文件，避免单文件膨胀：`project.go` / `node.go`（树与移动）/ `issue.go` / `comment.go` / `rank.go` / `types.go`。FX 注册加在 `cmd/internal/core/`。

**Project 是 Team 级实体，不挂在任何 bot 下。** 这是它与 `internal/memory/`（per-bot）的根本区别。

> 命名提示：`internal/project/` 目录当前为空——原先占用该名字的「Bot 工作目录绑定」概念已在 PR #942 中更名为 `Workdir`（`internal/workdir/`）。实现前确认该目录仍未被占用。
>
> 另有一个既存的同名概念：`internal/memory/wikistore/` 是 `memory_nodes` / `memory_edges` 上的 **per-bot 记忆图谱**，其注释里自称 "memory wiki tables"。它与本设计的 Wiki 视图无关，两者会在代码库中并存，不要混淆或试图合并。

## 3. 数据模型

九张新表（projects、project_nodes、project_node_versions、project_issue_details、project_issue_activity、project_comments、project_node_links、project_labels、project_node_labels），迁移编号从 `0130` 起。全部按 `0129_bot_workdirs.up.sql` 的模板：`team_id` 默认取 `public.memoh_current_team_id()`、`UNIQUE (team_id, id)`、复合外键、`ENABLE` 与 `FORCE ROW LEVEL SECURITY` 加四条 team 策略。

### 3.1 核心结构选择：窄节点表 + Issue 扩展表

Wiki 与 Issues **共用同一张 `project_nodes`**，用 `type` 区分；Issue 独有的结构化字段放在一对一扩展表 `project_issue_details`。

设计过程中比较过三种形状，记录取舍以免反复：

| 方案 | 否决 / 采纳原因 |
| --- | --- |
| 完全共用一张宽表（#937 的 D21 原样） | 一半列对 doc 恒为 NULL，`type=doc 时 status 必须为空` 这类 CHECK 会持续堆积 |
| `project_docs` + `project_issues` 两张独立表 | 评论、版本快照、以后的提及与搜索要么写两份要么退化成多态外键，而多态关联是 `internal/db/team_schema_guard_integration_test.go` 明确拦截的 |
| **窄节点表 + Issue 扩展表（采纳）** | 拿到共用的全部结构收益（一张版本表、一张评论表、一次搜索、一条未来的提及路径），同时避开宽表问题。读 issue 多一次 join，但看板本来就是一次范围查询批量取回 |

**扁平的 issue 仍然使用 `parent_id`，只是恒为 NULL。** 这样「以后要不要子任务」是增删一条约束的区别，不是加一张表的区别。

### 3.2 表定义要点

**`projects`**

```
id, team_id, name, description, created_by_user_id, deleted_at, created_at, updated_at
```

首版不做图标、封面、成员。`created_by_user_id` 按平台约定用复合外键指向 `team_members(team_id, user_id)`，`ON DELETE SET NULL (created_by_user_id)` 必须带列名单。

**`project_nodes`** —— 树与内容的唯一载体

```
id, team_id
project_id       UUID NOT NULL  → projects(team_id, id) ON DELETE CASCADE
type             TEXT NOT NULL  CHECK (type IN ('doc','issue'))
parent_id        UUID           → project_nodes(team_id, id) ON DELETE CASCADE
rank             TEXT NOT NULL                       -- lexorank，见 3.4
title            TEXT NOT NULL
body             TEXT NOT NULL DEFAULT ''            -- 当前正文
version          INT  NOT NULL DEFAULT 1             -- 正文乐观锁，见 6.1
created_by_user_id / created_by_bot_id               -- 见 3.6
updated_by_user_id / updated_by_bot_id
deleted_at, created_at, updated_at
CHECK (type <> 'issue' OR parent_id IS NULL)         -- issue 恒扁平
```

索引：

- 文档树 —— `(team_id, project_id, parent_id, rank) WHERE deleted_at IS NULL`
- 看板 —— `(team_id, project_id, type) WHERE deleted_at IS NULL`
- 搜索 —— `title` 与 `body` 各一个 `gin_trgm_ops` GIN 索引

**`project_issue_details`** —— 一对一扩展，`PRIMARY KEY (team_id, node_id)`

```
node_id          → project_nodes(team_id, id) ON DELETE CASCADE
status           TEXT NOT NULL DEFAULT 'todo'
                 CHECK (status IN ('todo','in_progress','done','cancelled'))
assignee_user_id / assignee_bot_id    CHECK (num_nonnulls(...) <= 1)
priority         TEXT CHECK (priority IN ('low','medium','high','urgent'))
due_at           TIMESTAMPTZ
revision         INT NOT NULL DEFAULT 1              -- issue 字段乐观锁，见 6.1
```

看板内的卡片排序复用 `project_nodes.rank`，语义是「在自己所属状态列内的位置」。**拖动一张卡片同时改变 `status` 与 `rank`，必须是一次原子更新**——因此 `PATCH …/issue` 端点同时接收这两个字段，而不是让前端先调 `/issue` 再调 `/move`（那样中途失败会留下「卡片跑到新列的错误位置」的中间态）。`POST …/move` 只服务文档树。

**`project_node_versions`** —— `PRIMARY KEY (team_id, node_id, version)`

存 `title + body` 的不可变快照与 `editor_user_id / editor_bot_id`。写路径见 6.1、6.2。

**`project_issue_activity`** —— issue 结构化字段的留痕

```
id, team_id, node_id, actor_user_id / actor_bot_id, field, old_value, new_value, created_at
```

`status` / `assignee` / `priority` / `due_at` 的每次变更写一行。它同时是 issue 详情页「活动流」的数据源，与评论混排成一条时间线。**这张表的存在理由**：版本快照只覆盖 `title + body`，没有它「谁把这张卡拖到已完成」无从追溯。

**`project_comments`** —— `node_id` 外键 + `author_user_id / author_bot_id`（exactly-one）+ `body` + `deleted_at`。doc 与 issue 共用。

**`project_node_links`** —— `PRIMARY KEY (team_id, source_node_id, target_node_id)`，两侧 CASCADE。表结构是通用的 node→node，首版 UI 只暴露「issue 关联 doc」这一种用法。

**`project_labels` + `project_node_labels`** —— 标签在 Project 内唯一（`UNIQUE (team_id, project_id, name)`）并带颜色；关联表 `PRIMARY KEY (team_id, node_id, label_id)`。

### 3.3 存储：不引入对象存储

**结构、Markdown 正文与版本快照全部存 Postgres，首版完全不接 `internal/storage/`。**

理由：

- 正文体量不构成问题。单篇 Markdown 通常几 KB，Postgres 单值上限 1GB（TOAST）；一万篇 × 20KB 也只有 200MB。
- 需要的能力对象存储都给不了：版本快照与「当前版本指针」必须原子提交、并发编辑要乐观锁、看板筛选与树查询要索引、搜索要 GIN 索引。放对象存储意味着一次保存跨两个系统，中间失败即脏数据。
- 首版不做贴图，唯一真正需要对象存储的路径（附件二进制）不存在。

以后做 Resources 时按 #937 的 D12 接 `internal/storage/`，且**只有二进制**走它，结构与正文永远留在 Postgres。

**搜索用 `pg_trgm` 而非 `tsvector`。** 仓库目前没有任何 `tsvector` 用法，而 Postgres 自带分词器不切中文——`to_tsvector('simple','项目管理')` 会得到一个整词，搜「项目」搜不到。首版用 `ILIKE '%kw%'` 配 `pg_trgm` 的 GIN 索引，中文子串匹配天然可用。需要新增 `CREATE EXTENSION IF NOT EXISTS pg_trgm`（标准 contrib，devenv 的 postgres 镜像自带）。

### 3.4 排序：lexorank 字符串，不用整数 position

文档树拖拽与看板列内拖卡片是同一个问题。整数 position 每拖一次要 UPDATE 同层全部兄弟，并发下还会互相打架；lexorank 风格的字符串排序键让每次拖拽只 UPDATE 一行。两处共用 `internal/project/rank.go` 一套实现。

配套要求见 6.4（rank 耗尽与 rebalance）。

### 3.5 标识：URL 用 UUID，不做 slug

标题随时可改且不影响链接，也不必处理重名。

### 3.6 现在就为 Agent 预留 `_bot_id` 空列

所有「user 或 bot」字段都建成两列 + `CHECK (num_nonnulls(user_col, bot_col) <= 1)`，首版 `_bot_id` 恒为 NULL。

> 实现修订：原设计写的是 exactly-one（`= 1`），实现时改为 `<= 1`。原因：平台约定用户引用使用 `ON DELETE SET NULL`，成员被移出团队时 SET NULL 会违反 exactly-one 导致无法移除成员。双 NULL 语义为「作者已不在团队」，与 `bot_workdirs.created_by_user_id` 可空的先例一致。

- `project_nodes.created_by_* / updated_by_*`
- `project_node_versions.editor_*`
- `project_issue_activity.actor_*`
- `project_comments.author_*`
- `project_issue_details.assignee_*`（这一列是 `<= 1`，允许未指派）

这一条几乎零成本，但让 Agent 工具那一版成为**零 schema 变更**；否则那时要改 CHECK、回填数据、动写路径。

### 3.7 删除：软删

`deleted_at` 标记。删一个文档节点时把整棵子树一起标记，列表默认过滤，版本快照一律保留。不做回收站 UI——数据可恢复但界面上没有入口，等真的出现误删再补。

## 4. API

沿用 `internal/handlers/workdir.go` 的形状（`e.Group` + swaggo 注解）。节点一律嵌在 project 下面：虽然 `node_id` 全局唯一，但嵌套可以避免 `/projects/nodes/...` 与 `/projects/:project_id` 在 Echo 路由里「静态段压参数段」的隐晦匹配。

```
POST   /projects                                    建 Project
GET    /projects                                    列出全部（Team 级）
GET    /projects/:pid                               单个
PATCH  /projects/:pid                               改名 / 描述
DELETE /projects/:pid                               软删

GET    /projects/:pid/tree                          整棵 doc 树，不含正文
GET    /projects/:pid/issues                        看板数据，一次拉完
GET    /projects/:pid/labels                        标签管理
POST   /projects/:pid/labels
PATCH  /projects/:pid/labels/:label_id
DELETE /projects/:pid/labels/:label_id

POST   /projects/:pid/nodes                         建 doc 或 issue
GET    /projects/:pid/nodes/:nid                    正文 + version + issue 字段
PATCH  /projects/:pid/nodes/:nid                    标题 / 正文，带 expected_version
POST   /projects/:pid/nodes/:nid/move               parent_id + rank（仅文档树）
DELETE /projects/:pid/nodes/:nid                    软删子树
PATCH  /projects/:pid/nodes/:nid/issue              issue 字段 + rank，带 expected_revision
PUT    /projects/:pid/nodes/:nid/labels             整体替换标签集
GET    /projects/:pid/nodes/:nid/versions           历史列表
GET    /projects/:pid/nodes/:nid/versions/:version  单版本内容
GET    /projects/:pid/nodes/:nid/comments
POST   /projects/:pid/nodes/:nid/comments
PATCH  /projects/:pid/nodes/:nid/comments/:cid
DELETE /projects/:pid/nodes/:nid/comments/:cid
POST   /projects/:pid/nodes/:nid/links
DELETE /projects/:pid/nodes/:nid/links/:target_nid

GET    /projects/search?q=&type=&project_id=        跨 Project，结果标注来源
```

**一次拉完，首版不做懒加载。** `GET /tree` 返回整棵树但不含正文；`GET /issues` 返回全部 issue 及其 details、labels、assignee，前端本地分列。已知限制：节点数极多的 Project 需要改为分层拉取或分页，首版不处理。

## 5. 前端

### 5.1 外壳形态

Projects 是**右侧的一个可收起导航面板**，只承担树状导航；点条目在 tabs 区打开新标签页。

```
┌──────────┬─────────────────────────────┬──────────────────┐
│ SideBar  │  tabs 区（dockview）         │ Projects 面板     │
│ (bot)    │  会话 │ 数据模型.md │ Issues  │ Projects  ＋⌕◨   │
│          │                             │ ▾ 📁 产品设计     │
│          │  ← 点树里的条目在这里开标签   │   ✓ Issues  4    │
│          │                             │   ▾ 📄 需求文档   │
│          │                             │     📄 数据模型   │
└──────────┴─────────────────────────────┴──────────────────┘
```

树的形状：

```
Projects
├── 产品设计            ← 根节点 = 一个 Project
│   ├── Issues         ← 固定伪节点，点击开 kanban 标签页；不可展开
│   └── 需求文档        ← doc，既是文档也是父节点
│       ├── 数据模型    ← 点击开文档标签页
│       └── 看板交互
├── 运营
└── 客户支持
```

`Issues` 是每个 Project 下的固定伪节点，不是数据库里的行——它对应「该 Project 全部 `type='issue'` 节点」这个查询。**它不可展开**：issue 数量会远多于文档，展开后会把文档树淹掉；单张卡的详情从看板里点开。

**收起按钮的归属规则是对称的：它永远在「当前占着那块空间的容器」的顶栏右端。** 面板展开时在面板 header 里（`Projects` 与 ＋、⌕ 并排）；面板收起后迁移到 tabs 顶栏。收起走左侧 `SideBar` 那套 push/pull——面板 flex 占位归零，tabs 区顺势长满，内容是被推开而非被遮盖。

评估过并否决的两个形态：

| 形态 | 否决原因 |
| --- | --- |
| 把 Project 主界面塞进 400px 右侧面板 | 单篇文档尚可读，但四列看板每列只剩约 90px，标题全截断、拖拽没有落点。看板是首版明确要做的两块之一 |
| 新加一条最左区域 rail（Chat 区 / Projects 区并列） | 结构上更干净，但要改 `main-section/index.vue` 加一层导航，且 Chat 与文档无法同屏。当前形态用更小的改动拿到了同等效果 |

### 5.2 文件结构

```
apps/web/src/
├── components/project-panel/
│   ├── index.vue                 header（Projects ＋ ⌕ ◨）+ 树容器
│   ├── tree-node.vue             递归节点，sortablejs 拖拽
│   ├── project-create-dialog.vue
│   └── node-context-menu.vue     重命名 / 删除 / 新建子文档
├── pages/home/components/dockview/
│   ├── panel-project-doc.vue     文档标签页：monaco 编辑 + markstream 预览
│   ├── panel-project-kanban.vue  看板标签页：四列 + sortablejs
│   └── panel-project-issue.vue   单张卡详情标签页
└── store/projects.ts             树数据 / 展开态 / 选中态 / 面板开合
```

依赖全部现成，不新引库：`sortablejs`（含 `@types`）用于树与看板拖拽，`monaco-editor` 编辑，`markstream-vue` 预览，`pinia-plugin-persistedstate` 持久化面板状态。

### 5.3 状态归属

**`store/projects.ts` 与 `workspace-tabs` 完全分离。** 面板开合、展开了哪些节点、当前选中项走 `persistedstate` **存全局**，不进 `BotLayoutState`。这是让导航面板本身与 Bot 无关的落点——否则会出现「切个 Bot 树就折叠回去了」。

**标签页复用 `workspace-tabs` 现有机制。** 新增三个 `WorkspacePanelComponent` 值：`projectDoc` / `projectKanban` / `projectIssue`。panel id 用稳定键（`project:doc:<node_id>`、`project:kanban:<project_id>`），同一个文档点两次是聚焦已有标签而非开第二个——与现有 `DISPLAY_PANEL_ID` 的做法一致。

### 5.4 已知且已接受：标签页按 bot 存

`workspace-tabs` 的布局状态是 `Record<botId, BotLayoutState>`，因此 Project 的标签页会落进 per-bot 布局：**切换 Bot 后文档标签页会跟着换掉，同一篇文档在两个 Bot 下各存一份标签。**

这是明知的取舍，本版不处理。将来若要让 workspace 整体与 Bot 解耦，落点是把该布局的存储键从 `botId` 换掉，届时导航面板与本设计的其余部分都不需要改动。

**不要把这条当 bug 重新讨论。**

## 6. 并发与错误处理

### 6.1 两把独立的乐观锁

| 锁 | 由什么驱动 | 留痕去向 |
| --- | --- | --- |
| `project_nodes.version` | 标题 / 正文变更 | `project_node_versions` 不可变快照 |
| `project_issue_details.revision` | status / assignee / priority / due_at 变更 | `project_issue_activity` |

**不能让 issue 字段变更去 bump `node.version`**：那会产生「内容没变但版本号跳了」的空版本，版本表要么插重复快照要么留空洞，历史列表全是噪音。

分成两把锁还有一个实际收益：A 在编辑某张 issue 的描述、B 同时把它从「待办」拖到「进行中」，这两件事本来就不该冲突，现在天然不冲突。

写路径在同一个 Postgres 事务内：锁 node → 比对期望版本 → 插入快照（或 activity 行）→ 递增版本号。冲突一律返回 **409**，body 携带当前 `version` / `revision` 与当前值。

### 6.2 保存策略与版本合并

自动保存用防抖（停止输入约 2 秒、失焦、关闭标签页各触发一次）。

**但快照必须合并**，否则历史列表一天几百条等于没有历史：同一作者在一个时间窗内（建议 5 分钟）的连续编辑只更新最后那条快照的内容，不插新行；跨窗口、换作者、或中间有他人编辑插入，才开新版本。窗口值进配置，不硬编码。

这与「不可变快照」的说法需要一句精确界定，否则会自相矛盾：**处于合并窗口内的最新一条版本行是可原地更新的，窗口关闭后它才转为不可变。** 已关闭的历史版本行任何时候都不得被改写或删除——包括回滚，回滚是追加一条内容等于目标历史版本的新版本。实现上以「该 node 的最大 version 行 + 同作者 + 在窗口内」为唯一可更新条件，其余一律追加。

冲突（409）回来时既不静默覆盖也不静默丢弃：提示「远端已更新」，把用户当前草稿保留在编辑器里，提供「重新加载」。首版不做三方合并——有版本快照兜底，最坏情况用户能自己找回内容。

### 6.3 树移动的服务端校验

前端也要禁用非法落点，但**服务端是权威**：

1. **环检测** —— 把节点移到自己的子孙底下必须返回 400。少了这条，一次误操作就能把整棵子树从树上摘掉。实现细节：sqlc 的分析器不支持在 SELECT 中引用递归 CTE，环检测改为服务层从候选父节点向根上溯（`GetProjectNodeParent` 逐级查询）；move 与 delete 事务内先取 `pg_advisory_xact_lock`（per-project），否则两个并发 move 可以各自通过环检测后共同提交出一个环。
2. `type='issue'` 的节点不接受 `parent_id`，恒扁平。
3. **跨 Project 移动子树首版禁止**（400）。递归改写整棵子树的 `project_id` 是事务内的批量更新，且跨 project 后「issue 关联文档」的语义会变模糊，而首版没有真实需求。将来支持时它是一个独立、可单独测试的改动。

### 6.4 rank 耗尽与 rebalance

lexorank 反复在两个相邻键之间插入会让字符串越来越长。检测到同层某个 rank 超过长度阈值时，在一次事务内重排该层，对用户无感。

**这一条必须写进实现**，否则拖拽几百次之后会出现难以复现的排序错乱。

## 7. 测试

| 层 | 覆盖 |
| --- | --- |
| `internal/project/` 单测 | rank 计算与 rebalance、树移动环检测、两把乐观锁的冲突路径、版本合并窗口的边界条件 |
| 集成测试 | `TEST_POSTGRES_DSN=… go test -tags=integration ./internal/db/` —— schema guard 机械校验 `team_id` 复合主键、复合外键、FORCE RLS、`SET NULL` 带列名单。**这些普通单测与 lint 都不会报错**，必须跑集成测试才能发现 |
| 前端 Vitest | store 的树规范化与展开态持久化、rank 的前端侧计算 |

不做 E2E。

## 8. 实现约束清单

摘自根 `CLAUDE.md` 与 #937 README 第 8.4 节，实现前逐条核对：

- 新表编号从 `0130` 起，每个 `.up.sql` 配套 `.down.sql`，DDL 用 `IF NOT EXISTS` / `IF EXISTS` 保证可重跑。
- 同步更新 `db/postgres/migrations/0001_init.up.sql`。本次全是新表，可直接内联 `CREATE TABLE`；**若后续需要给既有表加列，必须以 `ALTER` 追加到文件末尾**，否则全新安装与增量升级的物理列序不一致，sqlc 的 `SELECT *` 位置扫描会错位。
- 用户引用一律 `FOREIGN KEY (team_id, col) REFERENCES team_members(team_id, user_id)`，**不指向 `users(id)`**；`ON DELETE SET NULL` 必须带列名单 `SET NULL (col)`。
- 不使用多态关联（`actor_type + actor_id`），一律真实复合外键。
- schema 变更后运行 `mise run sqlc-generate`。
- 新增 handler 后运行 `mise run swagger-generate` 与 `mise run sdk-generate`。
- 前端改动提交前跑 `mise run lint`（含 `scripts/check-ui-contract.mjs` 的设计令牌守卫：禁止裸颜色、自造阴影、清单外的任意圆角）。
- Web 改动遵循 `apps/web/AGENTS.md` 与 `packages/ui/AGENTS.md`。

## 9. 与后续阶段的接口

本版有意为 #937 后续阶段留了三个不需要 schema 变更的挂载点：

| 后续能力 | 挂载点 |
| --- | --- |
| Agent 读写 Project | 3.6 预留的 `_bot_id` 列；工具集按 #937 第 5 节的能力切分（不按视图切分） |
| ACL | `projects` 上新增四张关系表，权限判定统一在 Project 一级，节点层不加任何权限字段 |
| @提及与 Inbox | `project_nodes.body` 与 `project_comments.body` 是唯一两个提及来源，两者已共用同一套结构 |

同时留下两条**已接受的债**，均已在正文中标注，不要当作缺陷重新讨论：标签页按 `bot_id` 存（5.4）、Project 节点数极多时需要改分层拉取（第 4 节）。
