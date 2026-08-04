# Phase 1：Group模型

> 前置阅读：[README.md](./README.md)
> 依赖：无。本阶段是Phase 2的地基。Phase 3（Wiki）与本阶段无依赖关系，可并行推进。

## 1. 目标与定位

引入Group的动机是Bot的访问权限存在差异：有的人能访问某些Bot，有的人不能。Group让有权限的人把Bot编入分组。

**Group是「谁能看到哪些Bot、哪些Bot之间可以互相联系」的分组，不是隔离边界。** 隔离边界仍然是Team。这个定位是本阶段全部设计的出发点，它决定了大量复杂度可以不做（见第6节）。

**Group与Wiki解耦**（决策D3）：Wiki是Team全局的，与Group没有任何关系。Group不决定谁能看到哪些知识，只决定谁能看到哪些Bot。

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

### 3.2 关于`allow_inbound_contact`

该列默认为真，**Phase 1不实现任何基于它的限制逻辑**，由Phase 2在同事发现与授权时消费。

留这一列的理由：将来某个Bot需要拒绝组内其他Bot的委托时，有列在是改代码，列不在是一次迁移。这是零成本对冲。

Wiki相关的权限列不在这里——Wiki与Group解耦后，「某个Bot是否允许写Wiki」是Bot自身的设置，不是成员关系上的属性。见[03-wiki.md](./03-wiki.md)第4节。

### 3.3 人格与角色的切分

Bot的人格（system prompt、模型、workspace、记忆）属于Bot自身，跨Group同一份。**「在这个组里扮演什么角色」属于成员关系**，因此`description`放在`group_bot_members`上，而不是`bots`表上。同一个Bot在研发组和客服组里的职责说明本来就应该不同。

这个字段是Phase 2中`list_teammates`的数据来源——它回答「这个同事会什么、什么时候该找它」。

## 4. 权限规则

### 4.1 把Bot加入Group需要双向授权

执行者**必须同时**具备：

- 对目标Group的管理权（owner或admin）
- 对目标Bot的管理权

只校验Group一侧会构成提权路径：任何Group管理员都能把别人的私有Bot拉进自己的组，组内成员随即可以使用该Bot。这条必须在handler层fail-closed地校验。

### 4.2 Group约束的是Bot可见性

用户只能看到并使用自己所属Group中的Bot（决策D4）。这条要在handler层按用户的Group成员关系严格过滤。

Group**不**约束Wiki内容——Wiki是Team全局的，全部成员可见。

### 4.3 关于早期设计中的跨组泄漏

设计早期版本采用「每个Group一个Wiki＋Bot可访问其所属全部Group的Wiki」，由此产生过一个已知口子：Bot同时属于G1与G2时，会成为G1成员间接读取G2 Wiki的传导路径。

**Wiki与Group解耦后（D3），这个口子不再存在**——只有一份Wiki，不存在「跨」。原本为它准备的授权时刻提示与来源标注两项缓解措施也一并取消。

保留本节是为了记录这个演进，避免后续有人重新引入per-group Wiki时忽略该风险。

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
| `wiki_nodes`增加`group_id` | Wiki与Group解耦（D3）。Wiki是Team全局的。 |
| 长期记忆按Group分区 | D5。Wiki本身就是Team全局的，单独限制记忆没有意义。 |
| memory provider接口增加scope参数 | 同上。 |
| A2A工具增加group参数 | 共享任意一个Group即允许contact，无歧义需要消解。见Phase 2。 |
| 每种Session入口的group来源推导 | D6之后不存在这个问题。 |

**Group只在两处被消费：**

1. 人类查看Bot列表时按成员关系过滤（第4.2节）
2. `list_teammates`与`contact_agent`的同事发现与授权（Phase 2）

除此之外，任何地方都不应该出现group维度——不在Session上，不在渠道配置上，不在Wiki上，不在记忆里。

## 7. 前端影响

Group成为Bot的管理单元后，Web侧需要：

- Group切换器
- Bot列表按当前Group过滤（人类视角，严格过滤）
- 跨Group的「我的全部Bot」管理视图
- Bot加入、移出Group的管理界面

**Wiki不在此列**——它是Team全局的，没有Group切换的概念，也不随Group切换器变化。这正是解耦的主要收益：用户不需要在「我现在在哪个组」和「这篇文档属于哪个组」之间建立心智映射。

详见`apps/web/AGENTS.md`的页面与路由约定。

## 8. 验收要求

### GRP-001：成员关系

- 必须能创建Group，并把用户与Bot加入、移出。
- 删除Bot或Group时，对应成员关系必须由数据库级联清理，不依赖应用层补偿。

### GRP-002：双向授权

- 缺少Bot管理权时把Bot加入Group必须失败，且失败原因可读。
- 缺少Group管理权时同样必须失败。
- 该校验必须fail-closed：任一侧权限查询出错时拒绝操作。

### GRP-003：人类侧过滤

- 用户查询Bot列表、Group列表时，返回结果必须只包含其所属Group的资源。
- 直接以ID访问非所属Group的Bot必须返回未找到或无权限。
- Wiki**不**受此约束：Team内任意用户访问Wiki必须成功。

### GRP-004：Group与Wiki无耦合

- Wiki的任何读写路径都不得引用Group表。
- 用户切换当前Group时，Wiki的可见内容必须不发生变化。
- 该项需要有测试守卫，防止后续实现悄悄引入group维度。

### GRP-005：迁移

- 在含有既存Bot、用户与会话的数据库上执行迁移后，全部Bot与用户必须位于`DefaultGroupID`。
- 迁移前后，chat与channel inbound的行为必须完全一致。
- `.down.sql`必须完整反向撤销`.up.sql`。
