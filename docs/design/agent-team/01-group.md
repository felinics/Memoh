# Phase 1：Group模型

> 前置阅读：[README.md](./README.md)
> 依赖：无。本阶段是后续三个阶段的地基。

## 1. 目标与定位

引入Group的动机是Bot的访问权限存在差异：有的人能访问某些Bot，有的人不能。Group让有权限的人把Bot编入分组，并以Group为单位组织协作。

**Group是「谁能找到谁 + 有哪些Wiki」的分组，不是隔离边界。** 隔离边界仍然是Team。这个定位是本阶段全部设计的出发点，它决定了大量复杂度可以不做（见第6节）。

## 2. Team与Group的关系

Group位于Team之下（决策D1）。两者的分工：

| | Team | Group |
| --- | --- | --- |
| 作用 | 租户、隔离、RLS、计费 | 协作、发现、授权 |
| 数量（开源版） | 恒为1（`DefaultTeamID`） | 任意多个 |
| Bot归属 | 恰好1个 | 可以多个（D2） |
| 用户归属 | 恰好1个 | 可以多个 |
| 是否隔离边界 | 是 | 否 |

现有的`0112_team_core`已经把`team_id`铺到全部业务表并启用RLS，`bots`的主键就是`(team_id, id)`。Group作为新增的一层挂在其下，**不改动任何既有的`team_id`列与RLS策略**。

> 命名提示：代码库中已有`internal/team/`、`teams`表与遍布的`team_id`。引入Group后，务必在根`AGENTS.md`中写入一行定义（Team=租户/隔离边界，Group=协作/权限单元），否则两个概念会被混用。

## 3. 数据模型

```sql
-- 分组本身
groups(
    team_id     UUID NOT NULL DEFAULT public.memoh_current_team_id()
                REFERENCES public.teams(id) ON DELETE RESTRICT,
    id          UUID NOT NULL,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, id)
)

-- 人类成员
group_user_members(
    team_id  UUID NOT NULL DEFAULT public.memoh_current_team_id(),
    group_id UUID NOT NULL,
    user_id  UUID NOT NULL,
    role     TEXT NOT NULL,        -- owner | admin | member
    PRIMARY KEY (team_id, group_id, user_id),
    FOREIGN KEY (team_id, group_id) REFERENCES public.groups(team_id, id) ON DELETE CASCADE
)

-- Bot成员
group_bot_members(
    team_id     UUID NOT NULL DEFAULT public.memoh_current_team_id(),
    group_id    UUID NOT NULL,
    bot_id      UUID NOT NULL,
    description TEXT,                          -- 该Bot在本组的职责说明，供list_teammates使用
    allow_inbound_contact BOOLEAN NOT NULL DEFAULT true,  -- 组内其他Bot是否可以contact它
    wiki_read   BOOLEAN NOT NULL DEFAULT true,
    wiki_write  BOOLEAN NOT NULL DEFAULT true,
    PRIMARY KEY (team_id, group_id, bot_id),
    FOREIGN KEY (team_id, group_id) REFERENCES public.groups(team_id, id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, bot_id)   REFERENCES public.bots(team_id, id)   ON DELETE CASCADE
)
```

以上为设计意图，落地时以实际迁移文件为准，并遵循README第8.4节的数据库约定。

### 3.1 为什么拆成两张成员表

不使用单张多态成员表（`member_type` + `member_id`）的原因：

1. 本代码库的外键都是实打实的复合外键。多态表无法同时外键到`users`和`bots`，删除Bot时不会自动清理成员关系。
2. 人和Bot在组内需要的字段本来就不同：人有治理角色（owner/admin/member），Bot有职责描述与能力开关。

### 3.2 关于三个布尔开关

`allow_inbound_contact`、`wiki_read`、`wiki_write`按D3默认全开，**Phase 1不实现任何基于它们的限制逻辑**。

留这三列的理由：将来某个组需要收紧时，有列在是改代码，列不在是一次迁移。这是零成本对冲。

### 3.3 人格与角色的切分

Bot的人格（system prompt、模型、workspace、记忆）属于Bot自身，跨Group同一份。**「在这个组里扮演什么角色」属于成员关系**，因此`description`放在`group_bot_members`上，而不是`bots`表上。同一个Bot在研发组和客服组里的职责说明本来就应该不同。

这个字段是Phase 2中`list_teammates`的数据来源——它回答「这个同事会什么、什么时候该找它」。

## 4. 权限规则

### 4.1 把Bot加入Group需要双向授权

执行者**必须同时**具备：

- 对目标Group的管理权（owner或admin）
- 对目标Bot的管理权

只校验Group一侧会构成提权路径：任何Group管理员都能把别人的私有Bot拉进自己的组，组内成员随即可以使用该Bot。这条必须在handler层fail-closed地校验。

### 4.2 人类侧不享受并集

用户只能访问自己所属Group的资源（决策D4）。G1的成员在API层**必须**看不到G2的Wiki，即使某个Bot同时属于两个组。

这条要在handler层按用户的Group成员关系严格过滤。不能因为「Bot都能看」就顺手放宽人的检查——下一节的口子正是建立在这个不对称之上，因此人这一侧必须是严的。

### 4.3 已知且已接受的口子

Bot是跨Group的传导路径：

> Bot B同时属于G1与G2，用户Alice只属于G1。Alice与B对话时，B可以读取G2的Wiki并把内容讲给Alice。

这与引入Group的初衷（人的访问权限有差异）存在张力。经讨论**决定接受**，理由是同一Team内本就共享信任边界。

要求把它变成有意识的选择而非疏忽，采取两项零成本措施：

1. **把风险挪到授权时刻。** 在把Bot加入Group的UI上明确提示：「B已属于G1，加入G2后G1成员可通过B间接读到G2的Wiki」。这是治理层的答案。
2. **Wiki检索结果携带group标注。** Bot引用Wiki内容时注明出处。不拦截，但保证可见、可审计。

## 5. 迁移

参照`internal/team/id.go`中`DefaultTeamID`的既有做法：

- 定义固定常量`DefaultGroupID`，**不得**按安装随机生成。理由与`DefaultTeamID`一致：迁移、fixture与应用代码需要跨环境引用同一个值。
- 迁移时幂等地播种该Group，并把现有全部Bot与用户加入其中。
- 升级后开源单租户安装的行为与升级前完全一致。

## 6. 本阶段明确不做的事

D3、D5、D6三条决策消去了大量复杂度。以下内容**不要实现**：

| 不做 | 原因 |
| --- | --- |
| `bot_sessions`增加`group_id` | Session不携带group上下文（D6）。chat与channel inbound只需要`bot_id`。 |
| `bot_channel_configs`、heartbeat、schedule配置增加group列 | 同上。 |
| 长期记忆按Group分区 | D5。Wiki既已放开，单独限制记忆没有意义。 |
| memory provider接口增加scope参数 | 同上。 |
| A2A工具增加group参数 | 共享任意一个Group即允许contact，无歧义需要消解。见Phase 2。 |
| 每种Session入口的group来源推导 | D6之后不存在这个问题。 |

**Group不是会话上下文，而是资源地址的一部分。** Wiki节点自带group归属，读取与搜索跨Bot所属全部Group取并集；只有「新建一篇顶层文档」这一个操作需要显式指定group。`list_teammates`同理，返回并集并标注来源组。

## 7. 前端影响

Group成为管理单元后，Web侧需要：

- Group切换器
- Bot列表、Wiki、会话列表按当前Group过滤（人类视角，严格过滤）
- 跨Group的「我的全部Bot」管理视图
- Bot加入Group的管理界面，含第4.3节要求的提示文案

这部分工作量不小，没有捷径。详见`apps/web/AGENTS.md`的页面与路由约定。

## 8. 验收要求

### GRP-001：成员关系

- 必须能创建Group，并把用户与Bot加入、移出。
- 删除Bot或Group时，对应成员关系必须由数据库级联清理，不依赖应用层补偿。

### GRP-002：双向授权

- 缺少Bot管理权时把Bot加入Group必须失败，且失败原因可读。
- 缺少Group管理权时同样必须失败。
- 该校验必须fail-closed：任一侧权限查询出错时拒绝操作。

### GRP-003：人类侧过滤

- 用户查询Wiki、Bot列表、Group列表时，返回结果必须只包含其所属Group的资源。
- 直接以ID访问非所属Group的资源必须返回未找到或无权限，不得泄漏存在性以外的信息。

### GRP-004：Bot侧并集

- Bot属于多个Group时，其Wiki检索必须覆盖全部所属Group。
- 检索结果必须标注来源Group。

### GRP-005：迁移

- 在含有既存Bot、用户与会话的数据库上执行迁移后，全部Bot与用户必须位于`DefaultGroupID`。
- 迁移前后，chat与channel inbound的行为必须完全一致。
- `.down.sql`必须完整反向撤销`.up.sql`。
