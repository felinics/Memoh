# App：Supermarket 资源统一为应用

状态：已实施（Supermarket 侧在 `feat/packages` 分支，Memoh 侧在本分支）。本文是 skills、workspace dependencies、connector 三条线合并为 app 的设计与实施记录。涉及两个仓库：Supermarket（registry 与 API）与 Memoh（Server 与 Web）。

## 1. 背景与目标

现状：

- 重构前，Supermarket 里 Package 只是 `packages/<id>/skills/<skill>/` 的容器，可选的 `package.yaml` 只有 `postinstall`。app 的名称、描述、标签、图标全部由 skills 聚合而来，没有版本号。
- workspace dependencies 是 `registries/memoh/dependencies/<id>/` 下的独立资源，有独立的不可变发布、独立 lock 与独立 API。Memoh 侧有完整的目录缓存、脚本 runner、状态机与 launcher 绑定。
- connector 的目录完全来自 connect-it 服务编译期注册的 `connector.Type`。Memoh 只保存 `(bot, connection_id, alias)` 绑定，Supermarket 里没有 connector。
- Web 端 Supermarket 有 connectors、skills、dependencies 三个 tab；bot 详情页也有三个平行 tab。

目标：

1. 用户面只有 app 一种可浏览、可安装、可删除、可更新的单元。
2. app 可以组合 skills、dependencies、connectors 三类组件。
3. app 拥有版本、作者、主页、许可证、分类、图标与多语言描述，并支持检查更新与更新。
4. deps 与 connector 在 registry 与 Memoh 两侧都保持独立实体，app 只按 id 引用，同一 bot 上不会重复安装。

## 2. 已定决策

| 决策 | 结论 |
| --- | --- |
| 名字 | 对外及业务协议统一为 App，中文 UI 显示为“应用” |
| deps 的位置 | 保留 `registries/memoh/dependencies/<id>/`，app 只引用 id；不搬进 app 目录 |
| deps 的独立发布 | 保留不可变发布、`dependencies.lock.json` 与 `/api/dependencies*`，只撤掉独立浏览入口 |
| connector | app 引用 connect-it 的 `connector_type`，绑定与凭据仍由 connectors 服务与 connect-it 管理 |
| 数据迁移 | 不做，当前版本尚未发布 |
| 部分安装 | 允许，app 有 `partial` 状态 |
| 哪些 registry 可含 deps 与 connector | 第一期仅 `memoh` registry；远程 registry 的 app 只有 skills |
| dep 的 revision 是否被 app 锁定 | 不锁定，安装与更新时总是解析 dep 当前定义，与现有 workspacedeps 行为一致 |
| 工作区目标粒度 | app 安装记录与 dep 引用按 `(bot, workspace_target)`；connector 引用挂在安装记录上，但 connection 是 bot 级共享 |
| 子项删除 | skills、deps、connector 不能在 app 之外单独删除；dep 子项保留 update、reinstall、rollback、查看脚本，connector 子项保留授权、重新授权、启停、断开（断开会吊销 Bot 级连接并让引用它的 app 重新要求授权） |
| 自动带装的包 | 不自动回收，删除确认框提供“同时移除仅被它使用的自动安装包”勾选，默认不勾 |

## 3. Registry 侧（Supermarket 仓库）

工作基于分支 `feat/workspace-dependencies`，因为 deps 尚未合入 main。

### 3.1 目录布局

```text
registries/
├── categories.yaml                       # 新增：跨 registry 共享的分类表
├── memoh/
│   ├── registry.yaml
│   ├── release.lock.json
│   ├── dependencies.lock.json
│   ├── dependencies/<dep-id>/            # 不变
│   └── apps/<pkg>/
│       ├── app.yaml                  # memoh registry 必需，schema 2
│       ├── icon.svg                      # 可选
│       └── skills/<skill-id>/SKILL.md    # 可选，app 至少要有一种组件
└── openai/                               # 不变，adapter 派生 app
```

### 3.2 `app.yaml` schema 2

```yaml
schema_version: "2"
id: codex                        # 必须等于目录名
version: 1.0.0                   # semver，仅供展示与人类沟通
name: Codex
description: OpenAI Codex CLI.
author: { name: Memoh, email: hello@memoh.ai }
homepage: https://github.com/openai/codex
repository: https://github.com/felinics/supermarket
license: Apache-2.0
icon: icon.svg
category: agent                  # 必须是 categories.yaml 中的 id
tags: [codex, agent]
translations:
  zh: { name: Codex, description: "OpenAI Codex 命令行" }
  ja: { description: "OpenAI Codex コマンドライン" }
dependencies: [codex]            # 引用 registries/memoh/dependencies/<id>
connectors:
  - { type: github, required: false }   # 引用 connect-it 的 connector_type
postinstall: []                  # 沿用 schema 1 的格式与限制
```

规则：

- `schema_version` 只接受 `"2"`，schema 1 不再支持。
- `id`、`version`、`name`、`description`、`category` 必填。
- skills 由 `skills/` 目录扫描得到，不在 yaml 里声明。
- `dependencies` 只允许在 `memoh` registry 出现，每项必须能解析到已启用的 dependency。
- `connectors[].type` 只允许在 `memoh` registry 出现，格式 `^[a-z][a-z0-9_]*$`，构建时不校验 connect-it 是否真的有这个 type。
- app 至少要有一个 skill、一个 dependency 或一个 connector。
- `translations` 只接受 `en`、`zh`、`ja`，结构与 dependency 的 `translations` 一致。
- skill 的 `metadata.author`、`metadata.homepage` 缺省时回落到 app。

### 3.3 `categories.yaml`

```yaml
schema_version: "1"
categories:
  - id: agent
    name: { en: Agents, zh: 智能体, ja: エージェント }
    order: 10
  - id: runtime
    name: { en: Runtimes, zh: 运行时, ja: ランタイム }
    order: 20
  - id: developer-tools
    name: { en: Developer Tools, zh: 开发工具, ja: 開発ツール }
    aliases: ["Developer Tools", "devtools"]
    order: 30
  - id: other
    name: { en: Other, zh: 其他, ja: その他 }
    order: 999
```

远程 registry 的自由文本分类先经 `normalizeSkillCategory` 归一化，再按 `aliases` 匹配，匹配不到落到 `other`。`memoh` registry 的 app 与 dependency 的 `category` 必须是表中的 id。

### 3.4 规范包

每个 dependency 都必须有一个同 id 的 app 作为它在商店里的载体，构建时校验。第一期新增五个包：`node`、`python`、`uv`、`codex`、`claude-code`，各自只引用同名 dependency，`python` 的 `requires: [uv]` 关系仍由 dependency.yaml 表达，不在 app 层重复。

### 3.5 发布产物

`AppRelease` 与 `AppDescriptor` 新增字段：`version`、`author`、`homepage`、`repository`、`license`、`icon`、`category`、`category_name`、`translations`、`dependencies: [{ id }]`、`connectors: [{ type, required }]`。`AppSummary` 新增 `version`、`category`、`dependency_count`、`connector_count`。app 的 revision 仍是 release JSON 字节的 SHA-256，因此引用列表、元数据或任一 skill 变化都会产生新 revision；dependency 定义变化不影响 app revision。

### 3.6 API 变化

| 变化 | 说明 |
| --- | --- |
| `GET /api/apps` | 新增 `component=skills\|dependencies\|connectors` 过滤；`category` 改为按 categories.yaml 的 id 过滤 |
| `GET /api/categories` | 新增，返回全局分类表含三语名称与各 registry 计数 |
| `GET /api/registries/:id/categories` | 删除，由全局接口替代 |
| `/api/dependencies*` 四个接口 | 不变，Memoh 继续用它们拉定义 |
| `GET /api/artifacts/icon/:digest` | 不变，app 图标复用 |

### 3.7 涉及文件

- `registry/app-manifest.ts`：schema 2 解析与校验。
- `registry/adapters/memoh.ts`：读取 app.yaml，允许无 skills 的包，收集 dependencies 与 connectors 引用。
- `registry/adapters/common.ts`、`skill-directory.ts`、`codex-marketplace.ts`：合成远程 registry 的 app 元数据，version 缺省。
- `registry/catalog.ts`、`registry/snapshot.ts`、`registry/types.ts`：类型、分类表、摘要字段。
- `registry/publish/candidate.ts`：跨资源校验，dependencies 引用必须解析到 `registries/memoh/dependencies/`，每个 dependency 必须有规范包。
- `registry/definitions/`：新增 categories.yaml 加载。
- `server/api/apps/index.get.ts`、新增 `server/api/categories.get.ts`、删除 `server/api/registries/[id]/categories.get.ts`。
- `registries/categories.yaml`、七个现有包的 `app.yaml`、五个规范包。
- `README.md`、`test/registry-http.test.ts`、`registry/*.test.ts`。

## 4. Memoh 后端

### 4.1 数据表

新增迁移 `0149_apps`，不做数据回填。

| 表 | 变化 |
| --- | --- |
| `bot_skill_package_installations` | 改名 `bot_app_installations`；新增 `version TEXT`、`status TEXT`（`installed`、`partial`、`installing`、`updating`、`removing`、`failed`）、`reason TEXT`（`user`、`required`）、`available_revision TEXT`、`available_version TEXT`、`last_checked_at TIMESTAMPTZ`、`last_error TEXT`、`release BYTEA`（缓存的 release 文档，让列表不依赖 Supermarket 在线） |
| `bot_app_dependency_refs` | 新增，`(team_id, installation_id, dependency_id)` 唯一，`installation_id` 级联删除 |
| `bot_app_connector_refs` | 新增，`(team_id, installation_id, connector_type)` 唯一，`connection_id TEXT` 可空，`required BOOLEAN` |
| `bot_dependency_installations` | 不变 |
| `connectors` | 不变 |
| `workspace_dependency_definitions`、`workspace_dependency_catalogs` | 不变 |

RLS 策略与现有表一致。

### 4.2 包级服务

新增 `internal/apps`，编排三个既有服务，不复制它们的逻辑：

- skills 部分沿用 `internal/supermarket` 的 `FetchAppRelease`、`prepareApp` 与 `internal/skills` 的 `PublishApp`、`PrepareAppRemoval`。
- deps 部分调用 `internal/workspacedeps` 的 `Install`、`Update`、`Remove`、`CheckUpdates`、`Preflight`。
- connector 部分调用 `internal/connectors` 的 `BeginOAuth`、`CreateCredential`、`Reauthorize`、`Delete`、`SetEnabled`。

`internal/skillapps` 改名 `internal/apps/store`，承载新表的读写。

### 4.3 安装流程

流式操作的 SSE 若在中途断开（代理抖动、5 秒写超时遇到卡顿的连接），服务端会继续执行；前端操作 store 改为轮询应用列表直到该包不再处于进行中状态，再按记录的结果收尾，只有超过 10 分钟仍未确认才显示“结果未确认”。

输入 `(bot, workspace_target, registry, app, revision)`。

1. 拉取并校验 release，检查 `dependencies` 与 `connectors` 非空时 registry 必须是 `memoh`。
2. 写安装记录，状态 `installing`。
3. 逐个处理 dependency 引用：若该 `(bot, target, dep)` 已安装或镜像自带，只写引用；否则调用 workspacedeps 安装，日志流透传给客户端。任一失败记录 `last_error` 但继续。
4. 发布 skills，原子替换，失败则整个安装记为 `failed` 并回滚 skills。
5. 逐个处理 connector 引用：bot 上已有该 type 的 active connection 则复用并写引用；否则写空 `connection_id` 的引用，等待用户授权。
6. 汇总状态：全部完成为 `installed`；有 dep 失败或必需 connector 未授权为 `partial`；skills 失败为 `failed`。

安装通过 SSE 返回，事件在现有 `started`、`log`、`done`、`error` 之外新增 `step`，携带 `kind`（`dependency`、`skills`、`connector`）与 `id`。

### 4.4 删除流程

1. 状态置 `removing`。
2. 移除 skills。
3. 对每个 dependency 引用：删除引用；若该 dep 在同一 `(bot, target)` 上不再被任何安装记录引用，且不是镜像自带来源，调用 workspacedeps 的 `Remove`。
4. 对每个 connector 引用：删除引用；若该 connection 不再被任何安装记录引用，调用 connectors 的 `Delete`。
5. 若请求带 `remove_unreferenced_required=true`，对 `reason=required` 且引用的 dep 均已无其他引用的安装记录递归执行同一流程。
6. 删除安装记录。

删除前的预览接口返回将要跑 remove 脚本的 dep 列表与将要断开的 connection 列表，供确认框展示。

### 4.5 更新流程

检查更新：对每个安装记录取 registry 当前 descriptor，比较 revision，写入 `available_revision` 与 `available_version`，并给出差异摘要：skills 增删改、dependency 引用增删、connector 引用增删。dep 自身的更新沿用 workspacedeps 的 `CheckUpdates`，结果显示在所有引用它的包的子项上。

更新：`POST /apps/update` 按用户在弹窗里勾选的项目执行一条 SSE 流：先把选中的 dep 逐个更新到最新版本（`workspacedeps.Update`），再在勾选了发布时拉取新 release，按 4.3 的顺序处理新增引用，按 4.4 的规则处理被移除的引用，skills 原子替换，最后写新 revision。discovered 的规范包只能更新自身那一个 dep。一级列表在发布或任一 dep 有新版本时直接显示 Update。

### 4.6 发现的 dep 与规范包

列表接口把 workspacedeps 报告的、未被任何安装记录引用的已安装或镜像自带 dep，合成为规范包 `memoh/<dep-id>` 的虚拟条目，`installation_id` 为空，`reason=discovered`。列表里与已安装包一样显示（图标借用该 dep 的图标），不带状态标记，也不提供安装按钮（它已经可用）；只有从 Supermarket 详情页安装同 id 的规范包时才正式写入安装记录并建立引用。agent 启用流程的 preflight 与安装步骤改为安装规范包。launcher 绑定表 `BuiltinLauncherCommands` 与 `provides[0]` 校验不变。

### 4.7 HTTP 接口

新增：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/bots/:bot_id/apps` | 安装列表，含子项状态与发现的规范包，参数 `workspace_target_id` |
| POST | `/bots/:bot_id/apps` | 安装，SSE |
| GET | `/bots/:bot_id/apps/:installation_id` | 详情 |
| GET | `/bots/:bot_id/apps/:installation_id/removal-preview` | 删除预览 |
| DELETE | `/bots/:bot_id/apps/:installation_id` | 删除，SSE |
| POST | `/bots/:bot_id/apps/check-updates` | 检查更新 |
| POST | `/bots/:bot_id/apps/update` | 按选择更新（发布与/或依赖），SSE |
| POST | `/bots/:bot_id/apps/:installation_id/resume` | 继续部分安装，SSE |
| POST | `/bots/:bot_id/apps/:installation_id/connectors/:type/oauth` | 为引用授权，内部调用 connectors 服务并回填 `connection_id` |
| POST | `/bots/:bot_id/apps/:installation_id/connectors/:type/api-key` | 同上 |
| GET | `/supermarket/categories` | 代理 registry 的全局分类表 |

保留：`/supermarket/*` 其余代理接口；`/bots/:bot_id/dependencies` 列表、`check-updates`、`preflight`、`:dep_id/script`、`:dep_id/install`（仅用于已被 app 引用的 dep 的重试与镜像副本覆盖安装，UI 不再提供“安装新 dep”入口）、`:dep_id/update`、`:dep_id/reinstall`、`:dep_id/rollback`；`/bots/:bot_id/connectors/:connection_id` 的 GET、PATCH、`reauth`；`/connectors/catalog` 用于补全 connector 的名称、图标与授权方式。

删除：`POST /bots/:bot_id/supermarket/install-app`、`GET /bots/:bot_id/supermarket/apps`、`DELETE /bots/:bot_id/supermarket/apps/:installation_id`、`GET /workspace-dependencies/catalog`、`DELETE /bots/:bot_id/dependencies/:dep_id`、`POST /bots/:bot_id/connectors/oauth`、`POST /bots/:bot_id/connectors/api-key`、`DELETE /bots/:bot_id/connectors/:connection_id`、`GET /supermarket/registries/:id/categories`。

`internal/supermarket/protocol.go` 同步新增 3.5 中的字段。SDK 用 `openapi-ts` 重新生成。

### 4.8 涉及文件

- `db/postgres/migrations/0149_apps.{up,down}.sql`、`db/postgres/queries/apps.sql`、sqlc 重新生成。
- `internal/apps/`：service、store、install、remove、update、list、events。
- `internal/supermarket/protocol.go`、`app_installer.go`（拆出可复用的 release 拉取与 skills 发布）。
- `internal/workspacedeps/service.go`：暴露“是否被镜像提供”与“按 dep 列出安装状态”的查询，供包级服务复用；删除独立 install 与 remove 的 handler 绑定，但保留服务方法。
- `internal/connectors/service.go`：新增按 type 查找 bot 上 active connection 的方法。
- `internal/handlers/apps.go` 新增，`supermarket.go`、`supermarket_skills.go`、`workspace_dependencies.go`、`connectors.go`、`containerd.go` 调整路由。
- `internal/agent/runtime/codex`、`claudecode` 的依赖启用路径改为规范包。
- `cmd/internal/core/module.go`、`providers.go` 注册新服务。
- `docs/design/workspace-dependencies.md` 补一节说明与 app 的关系。

## 5. 前端

### 5.1 Supermarket

- `pages/supermarket/index.vue`：删除三个 tab 与 `?tab=`，改为单一列表；筛选为 registry、分类、组件类型三组；搜索框统一。卡片显示版本、分类、组件计数徽章。
- `pages/supermarket/app-detail.vue`：路由改为 `/supermarket/:registryId/:appId`；新增 Dependencies 与 Connectors 两节，依赖通过其规范包（`memoh/<dep-id>`）展示名称、图标与描述；Information 节增加版本、作者、主页、仓库、许可证、分类。connector 的名称、图标与授权方式从 `/connectors/catalog` 补全，connect-it 未配置时显示“此包需要 connector，当前部署未启用”。
- `pages/supermarket/components/install-app-dialog.vue`：选择 bot 与 workspace target 后展示安装预览，包括将安装的 deps、将发布的 skills、需授权的 connectors；提交后进入进度对话框。
- 删除 `install-dependency-dialog.vue`、`connect-connector-dialog.vue` 的独立入口，授权表单逻辑迁移为子项组件复用。

### 5.2 Bot 详情页

- `pages/bots/detail.vue`：删除 `connectors`、`dependencies` 两个 tab，新增 `apps` tab；`skills` tab 保留，只管理用户自建、发现的 skill 与发现路径。
- 新增 `pages/bots/components/bot-apps.vue`：app 以两列卡片网格显示（复用市场页的 `market-item-card.vue`，只有图标、名称、描述），点击进入二级页（`app-detail-panel.vue`，版式同市场详情页：返回/操作行、大图标与标题、描述）展示 Skills / 依赖 / 连接器；有可用更新时行上直接显示 Update，点击弹出多选对话框（`app-update-dialog.vue`）批量更新；其余操作为检查更新、继续安装、删除。
- 子项组件：`app-dependency-item.vue` 复用现有 `dependency-row.vue` 的状态与动作决策，去掉 remove；`app-connector-item.vue` 提供授权、重新授权、启停、断开；`app-skill-item.vue` 提供查看。
- 删除确认框显示删除预览，含“同时移除仅被它使用的自动安装包”勾选。
- `store/dependency-operations.ts` 泛化为 `store/app-operations.ts`，以安装记录为 key 持有 SSE 流，`step` 事件驱动进度对话框分组显示。
- `pages/home/components/dependency-missing-block.vue` 跳转目标改为 apps tab 并定位到规范包。
- `bot-agents.vue` 与 `dependency-enable-flow` 改为安装规范包。

### 5.3 i18n

新增 `apps.*` 命名空间，en、zh、ja 三份同步。删除 `supermarket.connectorsSection`、`supermarket.dependenciesSection`、`bots.tabs.connectors`、`bots.tabs.dependencies` 等不再使用的 key。顺手补齐目前缺失的 `bots.dependencies.confirm.versionInvalid`、`bots.dependencies.progress.log`、`progress.unknownTitle`、`progress.unknownHint`、`status.retired`、`requestedMissing*`。

## 6. 分阶段实施

每个阶段独立可合并，前一阶段合并后才开始下一阶段。

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| 1 Supermarket | schema 2、categories.yaml、adapter 与候选构建校验、发布产物字段、API 变化、五个规范包与七个现有包的 app.yaml、README | `bun test`、`bun run registry:validate`、`bun run registry:publish` 通过；本地 `GET /api/apps` 返回新字段；`GET /api/categories` 三语可用 |
| 2 Memoh 后端 | 迁移、`internal/apps`、handler、SDK 重新生成、删除旧路由 | Go 单测覆盖安装、删除、更新、部分安装、引用计数与规范包合成；`go test ./internal/apps/... ./internal/workspacedeps/... ./internal/connectors/... ./internal/supermarket/...` 通过；swagger 与 SDK 无 diff 残留 |
| 3 前端 | Supermarket 单列表与详情页、安装对话框、bot 详情 apps tab、操作 store、i18n | vitest 通过；手工验证安装含 dep 与 connector 的包、部分安装后继续、删除预览与执行、检查更新与更新 |
| 4 收尾 | memoh-docs 的 supermarket、connectors、bot 指南更新，新增 apps 指南；删除前端与后端残留代码与 i18n key | 文档与实际行为一致 |

## 7. 假设与后续

- connect-it 未配置的部署上，含 connector 的包仍可安装，connector 子项显示不可用，包状态按 `required` 决定是否 `partial`。
- 远程工作区目标离线时，dep 步骤失败进入 `partial`，用户在工作区恢复后点“继续安装”。
- 远程 registry 的 app 未来若要映射 `mcpServers` 为 connector，需要 connect-it 先提供对应 type，不在本期。
- 第三方 dependency registry 与递归前置解析仍不在范围内。

## 8. App 协议直接切换

Memoh #1197 与 Supermarket #22 必须配套升级。清单为 `app.yaml`（schema 2），自有 registry 目录为 `apps/`；应用路由为 `/api/apps`、`/api/registries/:id/apps/*`，Memoh 对应 `/supermarket/apps` 与 `/bots/:bot_id/apps`。JSON 使用 `app_id`、`apps` 和 `app_count`，应用级 SSE kind 为 `app`。不提供旧 Package 路由、字段或清单兼容层。

应用描述、Skill 来源描述、发布文档、快照与 registry 状态升级为 schema 2；registry 定义、分类表和 dependency 协议保持 schema 1。Skill 文件归档格式和第三方源格式不变。所有发布内容由 Bun 1.3.14 重新生成、计算摘要并发布；旧不可变对象不覆盖。旧状态文件不能被新代码读取，切换时使用独立数据目录或桶准备新发布，并将消费端与 registry 一起切换；回滚时恢复匹配的旧服务与旧数据源。

迁移 0149 尚未发布，直接替换成 App 版本，同时保持 0001 初始 schema 为最终结构。已运行旧 PR 的开发数据库先用旧迁移回退到 148，再切换代码并升级到 149；旧安装记录和相关工作区的安装产物需要重建。不要直接在已标记 149 的数据库上重跑 migrate up，也不要仅修改迁移版本号。

本轮保留应用列表中的运行环境条目、现有分类和交互。市场入口显示“应用市场”，Bot 入口显示“应用”；Supermarket 仍是服务名称。应用用途与运行依赖的展示层次另行设计。
