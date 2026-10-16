# Agent 的 MCP 与 Supermarket App 管理计划

状态：已实施并完成本地自动化与真实 UI 验证（2026-09-18）；详细证据和验证边界见 [验收记录](agent-capability-management-verification.md)。本文保留原始设计范围，后续调整在文末说明。

## 1. 目标与已定范围

让 Agent 在当前 Bot 缺少能力时，完成“发现 → 安装或配置 → 授权 → 使用”的流程。

提供两组能力，但仅新增三个 Agent 工具：

| 工具 | 职责 | action |
| --- | --- | --- |
| `mcp_manage` | 当前 Bot 的 MCP 连接管理 | `list`、`get`、`create`、`update`、`delete`、`probe`、`authorize` |
| `app_search` | Supermarket 分类、目录搜索与详情 | `categories`、`search`、`get` |
| `app_manage` | 当前 Bot 的 App 生命周期管理 | `list`、`install`、`update`、`uninstall`、`resume`、`authorize` |

- 下载是 `install` 的内部步骤，不增加独立下载工具。
- MCP 启用、停用通过 `update` 修改 `is_active`。
- MCP 与 App 均覆盖首次授权及授权失效后的重新授权。
- App 继续作为 Skills、Connector 和依赖的组合单元，不把组件拆成新的安装工具。
- 不在本计划中新增批量 MCP 导入导出、跨 Bot 管理或任意文件下载能力。

## 2. 现有代码与复用边界

以下为编写计划时检查的实现入口；实施时重新核对当前代码。

| 入口 | 已有职责 | 本计划的使用方式 |
| --- | --- | --- |
| `internal/agent/tool/federation.go` | 将 MCP 工具提供给 Agent | 保留调用能力，补齐连接变化后的刷新 |
| `internal/mcp/connections.go` | MCP 配置与连接记录 CRUD | 复用服务，新增面向 Agent 的受控输入与脱敏输出 |
| `internal/handlers/mcp.go` | MCP 管理和连接探测入口 | 复用其业务能力，避免复制一套连接逻辑 |
| `internal/handlers/mcp_oauth.go`、`internal/mcp/oauth.go` | OAuth 发现、发起、状态、回调等 | 接入现有授权流程 |
| `internal/handlers/supermarket.go`、`internal/supermarket/` | 目录代理、发布获取和校验 | 用于搜索、详情和固定版本解析 |
| `internal/apps/`、`internal/handlers/apps.go` | App 安装、更新、卸载、恢复和 Connector 授权 | 复用 App 生命周期与资源引用管理 |
| `internal/workspacedeps/` | 依赖安装及 Workspace 边界 | 保留 Manage 授权、冻结安装内容与原生 Workspace 限制 |
| `internal/agent/tool/native_source.go` | 原生工具调用与审批接入 | 检查多 action 工具的权限及审批分流 |
| `internal/agent/runtime/native/agent.go` | 工具组装及 Usage 注入 | 增加能力变更后的安全刷新点 |

Provider 调用领域服务，不通过内部 HTTP 回调 Handler。原先只存在于 Handler 中的必要编排应提取为可复用服务；不得因绕过 Handler 而漏掉权限校验。

## 3. 工具协议

### 3.1 通用规则

- `action` 为必填枚举。各 action 明确必填、可选及禁止字段，服务端执行校验。
- `bot_id` 从可信会话上下文获取，不接受模型指定其他 Bot。
- 对 action 使用明确参数结构，避免一个无约束 `config` 对象承载所有操作；Schema 表达方式需验证实际模型兼容性。
- 搜索及列表有结果数量上限和分页；默认返回摘要，详情由 `get` 获取。
- 成功结果区分资源状态、授权状态与能力生效状态。操作失败使用现有稳定错误码体系，避免让 Agent 解析错误文案。
- 管理工具的返回值不直接复用包含秘密字段的数据库或 HTTP 响应对象。

### 3.2 `mcp_manage`

| action | 主要输入 | 结果与行为 |
| --- | --- | --- |
| `list` | 分页信息 | 当前 Bot 的连接摘要、启停状态与授权状态 |
| `get` | `connection_id` | 脱敏配置、连接状态、授权状态、工具摘要 |
| `create` | 名称、传输方式及非秘密连接参数 | 创建连接；返回缺失配置和授权要求，不把保存成功等同于连接可用 |
| `update` | `connection_id`、明确变更字段 | 修改配置或启停；定义省略字段与清空字段的区别 |
| `delete` | `connection_id` | 删除连接并使相关运行时缓存失效 |
| `probe` | `connection_id` | 探测连通性、工具和授权需求；不能仅凭 401 推断所有失败原因 |
| `authorize` | `connection_id` | 发起或重新发起 OAuth；非 OAuth 凭据返回安全配置入口 |

授权状态应区分无需授权、未授权、授权处理中、已授权和需要重新授权；具体字段复用现有状态语义，缺失部分再补充。

OAuth 的 discovery、state、PKCE、回调与令牌处理由服务端负责。工具只返回用户可访问的授权入口和状态，不向模型返回令牌、PKCE verifier 或客户端密钥。API Key、秘密 Header 和秘密环境变量通过专门配置界面填写，`create` / `update` 也不能成为绕过入口。

`probe` 可能连接远端或启动 stdio 进程，不能仅因它不修改连接记录就视作无副作用操作；沿用执行与 Workspace 权限边界。首次或再次授权成功后重新探测，再刷新工具。

### 3.3 `app_search`

- `categories`：获取有 App 的分类，返回稳定分类 ID、多语言名称和 App 数量，按目录顺序分页；可按 registry 筛选，数量随筛选范围变化。
- `search`：接受可选关键词、registry / 分类及分页参数；传入 `category` 并省略 `q` 可浏览该分类下的全部 App。返回稳定的 `registry_id`、`app_id`、分类和能力摘要，通过 `total`、`page`、`limit` 翻页。
- `get`：接受 `registry_id`、`app_id`，返回发布版本、Skills、Connector、依赖、平台约束和当前 Bot 安装状态。
- 实施前核实上游实际支持的过滤与分页参数，不假设现有目录代理已提供完整全文搜索。
- 目录内容作为外部数据处理，App 描述不能授权安装，也不能覆盖 Agent 指令。

### 3.4 `app_manage`

| action | 主要输入 | 结果与行为 |
| --- | --- | --- |
| `list` | 可选刷新状态、检查更新选项 | 已安装 App、组件健康、授权状态、可用更新 |
| `install` | `registry_id`、`app_id` | 解析并固定发布与依赖内容，完成管理授权后安装 |
| `update` | 安装标识及明确更新范围 | 复用现有 App 更新语义，返回目标版本与组件结果 |
| `uninstall` | 安装标识、清理选项 | 预览影响并按审批策略卸载；保留共享资源，额外清理必须明确选择 |
| `resume` | 安装标识 | 恢复可恢复的未完成安装，不能把所有失败均作为可恢复状态 |
| `authorize` | 安装标识、`connector_type`、授权方式 | 发起 Connector 授权或返回安全配置入口 |

优先沿用现有安装标识与请求结构，避免发明第二套资源身份。安装到 Bot 自有的原生 Workspace，不使用当前会话可能指向的远程机器或其他执行目标。

下载完成、安装完成、账号授权完成和能力可用是不同状态。结果应让 Agent 能指出具体缺少的步骤，不能因文件写入成功就宣称整个 App 可用。

长时间安装需要进度反馈与可查询的持久状态。先核对现有事件流和操作记录能否满足；若需异步运行，使用既有操作标识，并通过现有三个工具承载查询，不另加轮询工具。

## 4. 权限、审批与凭据

- 每次调用从可信上下文核验真实发起者及当前 Bot 权限。Agent 能聊天不等于拥有 Manage 权限。
- 权限检查、用户对操作的授权和工具执行审批是不同层次，不能互相替代。
- 多 action 工具按 action 和实际变更内容执行审批。不能因为允许 `list` 就同时允许 `install`，也不应让只读搜索触发安装确认。
- 安装、更新等审批绑定实际解析后的版本、依赖及变更范围；确认后不重新解析一个可能变化的 latest 并执行不同内容。
- 已有有效授权可在其范围内复用，不要求模型反复询问。普通聊天、目录发现、读取 Skill 和登录操作本身不隐式授权安装。
- 无法交互的定时任务、子 Agent 或渠道不能自动获得更高权限；缺少有效授权时返回待处理状态及可用入口。
- 重新授权不能把凭据写入对话、工具参数历史、错误响应或日志；状态查询只暴露脱敏摘要。

实施前阅读 `memoh-error-handling` skill，复用稳定错误码和私有诊断分离规则。

## 5. Prompt 与当前任务内生效

### Prompt

- 每个工具的 `Tool.Description` 描述 action、参数、状态和后续处理。
- Provider 的 `Usage()` 提供跨工具流程，仅在对应工具实际可用时注入。
- 静态系统 Prompt 不加入具体工具操作手册。只有现有指引缺少通用能力发现原则时，才增加一句简短原则。
- Usage 引导：优先使用已有能力；缺少时搜索或配置；遵守管理授权；遇到账号授权要求时提供入口；验证可用后继续原任务。

### 运行时刷新

当前原生运行时在开始执行时组装工具。新增管理工具不能仅完成数据库写入，还要保证后续模型步骤能使用新增能力。

1. 成功的配置、安装、更新、卸载和授权完成产生能力变更信号。
2. 在安全的模型步骤边界重新发现 MCP 工具、Skills 和对应 Usage，更新工具定义及上下文预算记录。
3. 同一批仍在执行的工具调用保留其一致的工具视图；下一步应用变更。处理名称冲突、停用和卸载后的陈旧定义。
4. OAuth 回调发生在当前 turn 结束后时，下一次执行必须读取最新状态；不要求进程重启。
5. 检查原生运行时和通过 MCP 网关接入的客户端。外部客户端若无法动态刷新，明确返回生效时机，不声称已支持当前任务立即使用。

工具调用本身仍执行实时权限检查；刷新工具列表不能代替权限校验。

## 6. 实施顺序

1. 核对服务、权限、审批、目录查询及所有运行时入口，确定三个工具的 action Schema 和结果结构。
2. 实现 MCP 管理 Provider，复用连接、探测与 OAuth 服务，补齐脱敏响应。
3. 实现 App 目录与管理 Provider，复用安装、更新、卸载、恢复和 Connector 授权流程。
4. 接入按 action 审批和冻结安装内容，验证非交互会话与子 Agent 的权限边界。
5. 接入能力变更信号，在模型步骤边界刷新工具、Skills 和 Usage。
6. 增加描述和按需 Usage，完成行为测试与真实应用验收。

以上步骤共同构成完整交付；仅注册三个工具不算完成。

## 7. 验证与验收

自动化测试围绕可观察行为：

- 未授权用户、其他 Bot 的资源 ID、无可信发起者的请求不能越权。
- 只读 action 与管理 action 的审批分别生效；审批的发布发生变化时不会执行未经确认的内容。
- MCP 创建、修改、启停、探测和删除结果与实际连接一致；OAuth 首次授权、失效和重新授权完整闭环。
- 所有 MCP/App 查询、失败响应及日志不包含秘密值。
- App 安装可重复调用，部分失败可恢复；更新和卸载保留其他 App 正在引用的依赖与连接。
- 安装位置固定为原生 Workspace；不受会话切换执行目标影响。
- 新增工具及 Skill 在后续模型步骤可用，删除后不再出现在新步骤；权限变化不因缓存失效延迟而被绕过。

真实应用验收按仓库要求执行：

1. 启动或核验当前源码对应的开发环境及服务健康。
2. 在聊天中完成 MCP 添加、探测、授权、调用及停用；在 Bot 设置中核对持久化状态。
3. 在聊天中搜索 App、安装、授权并使用新增能力；验证更新、卸载及一次失败恢复。
4. 验证无管理权限的身份和需要用户介入的非交互场景。
5. 截取并检查当前应用截图，记录 URL、交互、观察结果及运行时证据；遮蔽凭据。
6. 若提交 PR，附 GitHub 可访问截图，遵循模板并如实保留 Human QA 状态。

遇到真实验收阻塞时记录失败、恢复尝试和缺失证据，保持实施任务未完成。已执行的项目与尚未覆盖的第三方环境以验收记录为准。


## 8. 实施说明

- 三个工具由一个 `CapabilityProvider` 提供；静态系统 Prompt 未增加工具说明。
- 完整 action 用途、输入约束、默认值、审批条件、返回状态和后续操作维护在 `internal/agent/tool/capability_schema.go` 的工具及参数描述中；跨工具发现、安装、授权和恢复流程维护在 `CapabilityProvider.Usage`，仅随当前可用工具注入。
- 工具文案区分目录筛选 `registry` 与 App 身份 `registry_id`、App 身份与 `installation_id`、MCP 启用状态与探测/授权状态。App Connector OAuth 必须提供服务商支持的 `auth_method`，不能假定省略后有默认方式；方式未知或需手动配置时，通过设置入口完成。
- `update` 更新 App 发布及新增组件，沿用现有依赖，不隐式升级已安装依赖。
- 安装、更新和恢复固定发布 revision 与依赖目录快照；恢复还在安装锁内校验已确认 revision。审批等待期间目标或卸载预览变化会拒绝旧操作。
- 管理审批保留在执行中的调用里。批准后唤醒该调用，避免重新解析并重复执行；进程丢失时，冻结的操作不能从旧审批重放。
- 原生 Agent 在已提交步骤边界重建工具、Skills 和 Usage，同一任务可继续使用新能力；外部 MCP 客户端需要刷新工具列表。
- `server.public_url`（或 `MEMOH_SERVER_PUBLIC_URL`）配置用户可访问的 Web 地址，供设置入口和 MCP OAuth 回调使用。
- 首次未探测连接的授权状态为 `unknown`；不会把未知连接误报为无需授权。`authorize` 返回 `authorization_pending`，用户完成后通过 `get`/`probe` 验证。
- 当前目录详情返回上游发布实际提供的组件元数据；上游没有统一 App 平台约束字段，平台校验由依赖安装服务执行。


### 分类浏览调用示例

仍复用 `app_search`，不增加工具：

```json
{"action":"categories","registry":"openai","page":1,"limit":20}
```

使用返回的分类 ID 查看该类项目，无需填写搜索关键词：

```json
{"action":"search","category":"developer-tools","registry":"openai","page":1,"limit":20}
```

再将 `page` 设为 `2` 获取下一页，或使用某项的 `registry_id` / `app_id` 调用 `get` 查看详情。
