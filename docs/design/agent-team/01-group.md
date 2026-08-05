# Phase 1：Group模型

> 前置阅读：[README.md](./README.md)
> 依赖：无。本阶段是Phase 2的地基。Phase 3（Project）与本阶段无依赖关系，可并行推进。

## 1. 目标与定位

引入Group的动机是让一部分人和Bot形成显式的协作关系。Group让有管理权的人把Bot编入分组，让组内成员发现和使用这些Bot，也让组内Bot能够互相联系。

**Group是可选的协作与授权分组，不是Bot的必选归属，也不是隔离边界。** Bot可以不属于任何Group；这时它继续按现有owner与`bot_user_grants`规则工作，只是没有Group带来的成员发现与A2A授权。隔离边界仍然是Team。

**Group与Project解耦**（决策D3）：Project是与Group平行的独立实体，可以有多个，各自带ACL。Group不拥有Project，只能作为ACL的一种授予主体（「研发组的人都能写这个Project」）。Group不决定谁能看到哪些知识，只为Bot访问与A2A增加一种授权来源。

## 2. Team与Group的关系

Group位于Team之下（决策D1）。两者的分工：

| | Team | Group |
| --- | --- | --- |
| 作用 | 租户、隔离、RLS、计费 | 协作、发现、授权 |
| 数量（开源版） | 恒为1（`DefaultTeamID`） | 任意多个 |
| Bot成员关系 | 恰好属于1个Team | 可以是0到多个（D2） |
| 用户成员关系 | 可以加入多个Team | 可以是0到多个 |
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
    role     TEXT NOT NULL,        -- owner | admin | member（组内角色，与team_members.role无关）
    PRIMARY KEY (team_id, group_id, user_id),
    FOREIGN KEY (team_id, group_id) REFERENCES public.groups(team_id, id) ON DELETE CASCADE,
    -- 注意：指向team_members而不是users，见README第8.4节
    FOREIGN KEY (team_id, user_id)  REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE
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

1. 本代码库的外键都是实打实的复合外键。多态表无法同时外键到`team_members`和`bots`，删除用户或Bot时不会自动清理成员关系。
2. 人和Bot在组内需要的字段本来就不同：人有治理角色（owner/admin/member），Bot有职责描述与能力开关。
3. 两者的引用目标也不同——人指向`team_members(team_id, user_id)`，Bot指向`bots(team_id, id)`，无法共用一列。

「必须先是Team成员才能成为Group成员」这个约束由第一条外键自然保证，不需要额外校验。

### 3.2 关于`allow_inbound_contact`

该列默认为真，与成员关系一起落地。**Phase 1不实现任何基于它的限制逻辑**；Phase 2直接将它作为同事发现与A2A授权的入站开关，不需要后续补迁移。

Project相关的权限列不在这里，也不在`bots`上——Project与Group解耦后，「谁能读写哪个Project」完全由Project自身的ACL表达。见[03-project.md](./03-project.md)第4节。

### 3.3 人格与角色的切分

Bot的人格（system prompt、模型、workspace、记忆）属于Bot自身，跨Group同一份。**「在这个组里扮演什么角色」属于成员关系**，因此`description`放在`group_bot_members`上，而不是`bots`表上。同一个Bot在研发组和客服组里的职责说明本来就应该不同。

这个字段是Phase 2中`list_teammates`的数据来源——它回答「这个同事会什么、什么时候该找它」。

## 4. 权限规则

### 4.1 把Bot加入Group需要双向授权

执行者**必须同时**具备：

- 对目标Group的管理权（owner或admin）
- 对目标Bot的管理权

只校验Group一侧会构成提权路径：任何Group管理员都能把别人的私有Bot拉进自己的组，组内成员随即可以使用该Bot。这条必须在handler层fail-closed地校验。

### 4.2 Group提供增量访问，不替换现有权限

用户对Bot的有效访问是以下两类授权的**并集**：

1. 既有直接授权：Bot owner与`bot_user_grants`。
2. Group授权：用户与Bot至少共享一个Group时获得`chat`权限。

Group成员关系只增加Bot列表可见性与`chat`权限，不授予`workspace_read`、`workspace_write`、`workspace_exec`或`manage`。这些能力仍只能来自owner或`bot_user_grants`。把Bot加入或移出Group仍需第4.1节的双向授权；Group管理员不能借成员关系修改Bot本身的配置。

Bot可以不属于任何Group。无Group Bot仍对owner和持有直接授权的用户可见并可用，只是不出现在任何Group视图中，也不能使用Phase 2的`list_teammates`或被其他Bot通过Group联系。**不得为了实现Group而创建Default Group、强制新Bot入组，或把未入组Bot从既有owner/直接授权视图中隐藏。**

Group**不**约束知识的可见性——那由每个Project自身的ACL决定。

### 4.3 关于早期设计中的跨组泄漏

设计早期版本采用「每个Group一个共享空间＋Bot可访问其所属全部Group的共享空间」，由此产生过一个已知口子：Bot同时属于G1与G2时，会成为G1成员间接读取G2内容的传导路径。

**Project与Group解耦后（D3），这个口子的Group形态不再存在**——Project不归属于Group，多组归属不会带来任何额外的知识可见性。

但需要说明：该风险的**根因不是Group，而是Bot拥有独立于人的权限**，因此它在多Project＋ACL方案下换了个形式继续存在（Bot的Project授权集合与对话人的不一致）。处理方式记录在[03-project.md](./03-project.md)第4.4节，不在本阶段范围内。

保留本节是为了记录这个演进，避免后续有人重新引入per-group共享空间时忽略原始风险。

## 5. 兼容与迁移

本阶段只新增Group及成员关系，不为历史数据制造隐式组织结构：

- **不存在`DefaultGroupID`，迁移不得创建Default Group。**
- 既有用户与Bot迁移后都没有Group成员关系，既有owner与`bot_user_grants`保持原样。
- 新建Bot默认也不属于任何Group；只有显式的加入操作才创建`group_bot_members`记录。
- 创建Group时，创建者成为该Group的owner；不会自动把其全部Bot加入Group。
- 新增或移除Team成员不会自动修改任何Group成员关系。

因此升级兼容不依赖回填：Group是新增的可选能力，未配置Group的安装在升级前后行为完全一致。

## 6. 本阶段明确不做的事

D3、D5、D6三条决策消去了大量复杂度。以下内容**不要实现**：

| 不做 | 原因 |
| --- | --- |
| `bot_sessions`增加`group_id` | Session不携带group上下文（D6）。chat与channel inbound只需要`bot_id`。 |
| `bot_channel_configs`、heartbeat、schedule配置增加group列 | 同上。 |
| Project或`project_nodes`增加`group_id` | Project与Group解耦（D3）。Project是独立实体，权限由自身ACL决定；Group只能作为ACL主体出现。 |
| 长期记忆按Group分区 | D5。Group不决定知识可见性，单独按Group限制记忆没有意义。 |
| memory provider接口增加scope参数 | 同上。 |
| A2A工具增加group参数 | 共享任意一个Group即允许contact，无歧义需要消解。见Phase 2。 |
| 每种Session入口的group来源推导 | D6之后不存在这个问题。 |

**Group成员关系只在三处被消费：**

1. 人类查看Bot列表时，把Group成员关系作为既有直接授权之外的增量访问来源（第4.2节）
2. `list_teammates`与`contact_agent`的同事发现与授权（Phase 2）
3. 解析`project_group_acl`时，为该Group的人类成员批量授予Project read/write（Phase 3的可后置集成层）

除此之外，任何地方都不应该出现group维度——不在Session上，不在渠道配置上，不作为Project归属字段，也不在记忆里。

## 7. 前端影响

Group成为Bot的可选协作单元后，Web侧需要：

- Group筛选器；它是可选过滤条件，不是必须选择的全局上下文
- 保留按owner与直接授权得到的「全部可访问Bot」视图，其中包括无Group Bot
- 按Group查看该组授予的Bot
- Bot加入、移出Group的管理界面

**Project不在此列**——它有自己的列表与授权界面，不随Group切换器变化。这正是解耦的主要收益：用户不需要在「我现在在哪个组」和「这篇文档属于哪个组」之间建立心智映射。

唯一的交叉点是把Group作为ACL主体授予Project，以及建Group时可选的「顺手为这个组建一个Project」快捷操作——后者只是预填ACL，不建立任何结构关联。

详见`apps/web/AGENTS.md`的页面与路由约定。

## 8. 验收要求

### GRP-001：成员关系

- 必须能创建Group，并把用户与Bot加入、移出。
- 删除Bot或Group时，对应成员关系必须由数据库级联清理，不依赖应用层补偿。

### GRP-002：双向授权

- 缺少Bot管理权时把Bot加入Group必须失败，且失败原因可读。
- 缺少Group管理权时同样必须失败。
- 该校验必须fail-closed：任一侧权限查询出错时拒绝操作。

### GRP-003：人类侧增量访问

- 用户查询Bot列表时，结果必须是既有owner/`bot_user_grants`授权与Group授权的并集并正确去重。
- 用户与Bot共享Group但没有直接授权时，必须可以看到该Bot并获得`chat`权限，但不得因此获得任何workspace或manage权限。
- 用户与Bot不共享Group、也没有任何直接授权时，直接以ID访问必须返回未找到或无权限。
- 无Group Bot必须继续对owner和持有直接授权的用户可见并可用。
- Project**不**受此约束：访问权限由Project自身的ACL决定，与用户属于哪个Group无关。

### GRP-004：Group不拥有Project

- Project不得有归属Group的字段；删除Group必须只移除对应的ACL条目，不影响任何Project或其内容。
- Project的读写路径除ACL解析外不得引用Group表。
- 用户切换当前Group时，其可访问的Project集合必须不发生变化。
- 该项需要有测试守卫，防止后续实现把Project重新挂回Group。

### GRP-005：可选成员关系与迁移

- 在含有既存Bot、用户与会话的数据库上执行迁移后，不得自动创建任何Group或成员关系。
- 迁移前后，既有Bot列表、chat与channel inbound行为必须完全一致。
- 新建Bot默认没有Group成员关系，但owner必须仍能看到、管理和使用它。
- 从最后一个Group移出Bot后，它必须继续按owner与`bot_user_grants`工作。
- `.down.sql`必须完整反向撤销`.up.sql`。
