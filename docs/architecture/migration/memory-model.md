# Memory / Model Business Owner 迁移账本

状态：Direction Update Accepted；详细Move账本仍为Discussion Draft

基线：`origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5`

主审范围：`internal/memory/**`、`internal/models/**`、`internal/providers/**`、
`internal/registry/**`、`internal/providertemplates/**`、`internal/capabilities/**`、
`internal/copilot/**`、`internal/fetchproviders/**`、`internal/searchproviders/**`、
`internal/audio/**`、`internal/video/**`、`internal/oauthclients/**`、
`internal/oauthctx/**`。同时审计对应 HTTP handler、`cmd/internal/core` wiring、Agent/Chat
consumer、PostgreSQL/pgvector query 与 migration、SQLC 生成物和配置资产。

上位约束：

- 目标目录为 `domains/<owner>`：代码 owner/领域隔离，不是 Go module、进程或部署单元；
  composition root 留 `cmd/agent`/`cmd/channel`，无 `internal/composition`；
- `docs/architecture/process-boundary-rpc-decision.md`决定Memory和Model均留Server；
- embedded/split Server都编译相同Memory/Model concrete；不建立standalone binary、
  `local` wrapper或Channel-facing gRPC；
- interface 放在 consumer 侧，SQLC/pgtype 不得进入 service contract；
- 本文件是 current -> target 账本，不代表当前目录已经整理完成。

## 1. 审计结论

Memory 和 Model 当前都不是可机械移动的包。

Memory 的 `adapters.Provider` 同时承担 Conversation hooks、MCP tool、CRUD、compact/usage，
Registry 又同时负责 team scoping、factory、instance cache 和 lifecycle。Builtin provider 还直接
依赖 Conversation、Account、Workspace Bridge、Model、PostgreSQL、pgvector 与两套 SQLC。
如果直接搬目录，Memory仍会携带跨owner concrete依赖和global Store；这与是否独立部署无关。

Model 侧的 `models`、`providers`、`registry`、`providertemplates`、Audio、Video、Search、
Fetch 都直接持有 `dbstore.Queries` 或 SQLC record；Provider OAuth 约 1,500 行，并与 provider
CRUD、token persistence、GitHub/Codex HTTP flow 混在同一 `Service`。Provider template 又同时
存在 YAML source registry、DB catalog 和 runtime provider CRUD 三套“registry”语义。

目标边界：

```text
Agent/API -> Memory consumer ports -> domains/memory concrete
Memory implementation -> consumer ports for Model, Runtime FS, Agent Chat access

Agent/API -> Model consumer ports -> domains/model concrete
Model -> catalog/provider/template/oauth/audio/video/fetch/search implementation
```

Memory和Model都与Server同进程；owner边界仍要求通过consumer ports消除横向concrete import，
但这不产生RPC。

## 2. 目标目录

```text
domains/memory/
  memory.go                         # Item、request/response、错误；接口由consumer定义
  internal/
    provider/                       # provider SPI（按 capability 拆接口）
      builtin/
      mem0/                         # 若保留
      openviking/                   # 若保留
    registry/                       # factory + team instance lifecycle
    recall/                         # before-turn retrieval/context packing
    formation/                      # after-turn extract/decide/apply
    compact/
    segment/
    slug/
    migrate/
    index/                          # semantic pgvector seed index
    store/
      fs/
      wiki/
    postgres/                       # provider/wiki stores

domains/model/
  model.go                          # Model/Provider DTO、ClientType/ModelType；不放provider接口
  local/
    module.go
    chat.go                         # Twilight model/provider construction
    embedding.go
    cache.go
    http.go
  capability/                       # cmd/synccaps 可导入的 build-time API
  internal/
    catalog/                        # model CRUD/selection
    provider/                       # provider CRUD/probe/remote discovery
      copilot/
    template/                       # YAML load/normalize + DB sync/catalog
    oauth/                          # provider OAuth flow；store 分离
    fetch/
    search/
    audio/
      edge/                         # 仅在证明确有生产用途时保留
    video/
    postgres/
    secret/                         # schema-driven merge/mask，禁止日志泄漏

internal/oauth/
  client/                           # 跨 Model/Email/Plugin 的 operator OAuth client registry
  context/                          # authenticated principal context vocabulary
```

测试helper优先留在相邻`*_test.go`或`testdata`。只有多个package确实需要同一编译型fixture且
无法通过公开contract构造时，才单独批准`internal/test`；不预设生产可见的`testkit` package。

`domains/model/capability` 不能放在 `domains/model/internal`：当前 `cmd/synccaps` 是合法
consumer，Go `internal` 规则会禁止它导入。该包是纯 build-time API，不进入 runtime Server。

## 3. 当前 package -> 目标 package

| 当前 package | 目标 | 类型 | 说明 |
| --- | --- | --- | --- |
| `memory/adapters` | `domains/memory` + `internal/{provider,registry}` | Split/Delete | 删除含糊 `adapters` 名；contract、SPI、registry、admin CRUD 分开 |
| `memory/adapters/builtin` | `domains/memory/internal/{provider/builtin,recall,formation,compact,index}` | Split | 当前 13 文件横跨 5 个职责 |
| `memory/adapters/mem0` | `internal/provider/mem0` 或删除 | Decide | 当前是永远 disabled 的兼容 placeholder |
| `memory/adapters/openviking` | `internal/provider/openviking` 或删除 | Decide | 当前是永远 disabled 的兼容 placeholder |
| `memory/memllm` | `domains/memory/internal/formation` + ModelPort | Split | prompt/parsing 归 Memory，模型构造归 Model port |
| `memory/migrate` | `domains/memory/internal/migrate` | Move | Markdown -> wiki node 转换，不是 DB migration runner |
| `memory/segment` | `domains/memory/internal/segment` | Move | 纯文本分段 |
| `memory/slug` | `domains/memory/internal/slug` | Move | 纯 slug 规则 |
| `memory/storefs` | `domains/memory/internal/store/fs` | Split | 修复复合包名；Bridge concrete 改 Runtime FS port |
| `memory/wikistore` | `domains/memory/internal/store/wiki` + `postgres` | Split | value/store interface 与 SQLC adapter 分开 |
| `models` | `domains/model` + `local` + `internal/catalog` | Split | DTO/validation、SDK client、CRUD/selection/probe 分开 |
| `providers` | `domains/model` + `internal/{provider,oauth,secret}` | Split | CRUD、probe、remote models、OAuth、credential resolution 分开 |
| `registry` | `domains/model/internal/template` | Split/Delete | YAML source catalog 与 sync 合并到明确 owner，删除泛化包名 |
| `providertemplates` | `domains/model/internal/template` + `postgres` | Split/Delete | DB template catalog/sync/instance resolver |
| `capabilities` | `domains/model/capability` | Move | build-time only；不编进 runtime |
| `copilot` | `domains/model/internal/provider/copilot` | Move | token/client/provider implementation |
| `fetchproviders` | `domains/model/internal/fetch` + `postgres` | Split | 修复复合名，CRUD/store 分开 |
| `searchproviders` | `domains/model/internal/search` + `postgres` | Split | 修复复合名，CRUD/store 分开 |
| `audio` | `domains/model` contract/local + `internal/audio` | Split | catalog、synthesis/transcription、temp transport 分开 |
| `audio/adapter/edge` | 删除或 `internal/audio/edge` | Decide/Delete | 当前 production 无调用点；Twilight registry 已提供 Edge |
| `video` | `domains/model` contract/local + `internal/video` | Split | provider registry、catalog、execution 分开 |
| `oauthclients` | `internal/oauth/client` | Move | Email/Plugin 也消费，不归 Model internal |
| `oauthctx` | `internal/oauth/context` | Move | principal context vocabulary；最终可并入统一 auth principal |

## 4. Memory contract、SPI、Registry、CRUD 拆分

### 4.1 `memory/adapters` 逐文件

| 当前文件/代码 | 目标 | 类型 | 精确动作 |
| --- | --- | --- | --- |
| `types.go` CRUD/Item/Status DTO | `domains/memory/memory.go` | Split | 保留 transport-safe 值；`time.Location` 改 timezone string，不能进入 protobuf contract |
| `types.go` `BeforeChat*`, `AfterChat*` | `domains/memory` 的 `RecallRequest/RecallResult/RecordTurnRequest` | Rewrite | contract 不使用 Conversation/Agent internal type |
| `types.go` `LLM`, Extract/Decide/Compact DTO | `internal/formation` consumer port | Move | 不是外部Memory API；Server-local Model concrete实现 |
| `provider.go:Provider` | 删除巨型接口 | Delete/Split | 拆为 `RecallProvider`、`Recorder`、`Reader`、`Writer`、`Compactor`；consumer 只收最小接口 |
| `provider.go` MCP `ListTools/CallTool` | Agent Memory tool adapter | Delete/Move | Memory provider SPI 不 import MCP；Agent tool 调 Memory contract |
| `MemoryVersionProvider`, `SourceSyncProvider`, `MarkdownIngestProvider`, `SemanticCompactProvider` | `internal/provider` capability interfaces | Move | 保持可选 capability，不并回巨型接口 |
| `registry.go` `Factory/TeamIDResolver/ProviderConfigLoader/TeamDefaultFactory` | `internal/registry` | Split | Factory 返回所需 capability set；team resolver 由 request/tenant adapter 注入 |
| `registry.go:Registry` instance map/Get/Instantiate/Remove/Close | `internal/registry` | Move | 保留有界lifecycle；Server shutdown必须Close |
| `service.go` provider metadata | `domains/memory` admin DTO + internal catalog | Split | secret schema 保留，response 必须 mask |
| `service.go` CRUD/EnsureDefault/Status | Memory admin service + `internal/postgres/provider.go` | Split | 删除 SQLC/dbstore/config concrete；store interface 就地定义 |
| `context_cache.go` | `internal/recall/cache.go` | Move | cache key 必须含 team/bot/provider/version/query hash |
| `helpers.go` | 邻近 `recall`/`formation` 文件 | Delete | 不建立 helpers package；Deduplicate/metadata 各归使用方 |

Memory public contract 至少按 consumer 分成：

- `Recall(ctx, RecallRequest) (RecallResult, error)`；
- `RecordTurn(ctx, RecordTurnRequest) error`；
- `Add/Search/List/Update/Delete` 管理/工具面；
- `Compact/Usage/Status/Rebuild/Ingest` 可选管理面；
- Provider config admin contract。

不得把当前全部方法再包装成一个 `MemoryService` 巨型接口。Agent flow、Agent tool、API handler
分别定义自己需要的interface，由Server-local Memory concrete满足。

### 4.2 Builtin 逐文件/职责族

| 当前文件 | 目标 | 类型 | 说明 |
| --- | --- | --- | --- |
| `builtin.go` | `provider/builtin/provider.go` + `recall/hook.go` + `formation/hook.go` | Split | Provider facade、before/after chat、CRUD/MCP 混合；MCP 部分移 Agent adapter |
| `factory.go` | `local/builtin.go` | Split | 替换 `dbstore.Queries/pgvectordb.Store/wikistore/storefs` concrete 为 Memory stores 与 Model/Runtime ports |
| `context_packer.go` | `internal/recall/pack.go` | Move | recall budget/dedupe/lost-in-middle |
| `formation.go` | `internal/formation/apply.go` | Move | Extract/Decide/apply；只依赖 formation LLM port + Writer |
| `compact.go` | `internal/compact/semantic.go` | Split | compact algorithm 与 wiki/index persistence 分开 |
| `graph_runtime.go` | `provider/builtin/graph.go` | Split | CRUD/graph search/status/rebuild；store/index ports 注入 |
| `graph_cache.go` | `internal/recall/graph.go` | Move | graph build/cache/version/lexical score |
| `graph_sync.go` | `internal/store/fs/sync.go` | Move | DB source -> Markdown derived view |
| `ingest.go` | `internal/migrate/ingest.go` | Move | Markdown derived/source input -> DB nodes |
| `pgvector_index.go` | `internal/index/postgres.go` | Split | embedding model lookup走 ModelPort；pgvector SQLC 仅在 adapter 内 |
| `retry.go` | `internal/index/retry.go` | Move | retry goroutine必须随 registry/provider Close 停止并保持 team binding |
| `file_runtime.go` | `provider/builtin/file.go` 或删除 | Decide | 当前仅bootstrap/fallback；若DB wiki必需应fail fast，避免静默改变source of truth |
| `shared.go` | 相邻 `provider/builtin`/store value | Delete/Split | `memoryStore` interface 留 consumer；转换/ID/hash 就近放置 |

`mem0.go` 与 `openviking.go` 当前所有 CRUD 均返回 disabled error、hook 为 no-op。迁移前必须
统计持久化 `memory_providers.provider` 使用量：无存量则删除类型和 UI meta；有存量则保留
明确 Unsupported 状态并提供迁移提示，不能假装是已实现 provider。

### 4.3 Store、migration、LLM

| 当前文件族 | 目标 | 类型/前置 |
| --- | --- | --- |
| `memllm/client.go` | `internal/formation/llm.go` | Split；prompt orchestration 留 Memory，`models.NewSDKChatModel/ApplyPromptCache` 改为 `FormationModelPort` |
| `memllm/prompts.go` + 2 个 Markdown | `internal/formation/prompts.*` | Move；embed 路径同提交 |
| `migrate/tomemorywiki.go` | `internal/migrate/wiki.go` | Move |
| `segment/segment.go` | `internal/segment/segment.go` | Move |
| `slug/slug.go` | `internal/slug/slug.go` | Move |
| `storefs/service.go` | `internal/store/fs/{read,write,plan,format}.go` | Split；当前大文件拆职责，Workspace Bridge 由 `FilePort` 替代 |
| `wikistore/{store,conv}.go` | `internal/store/wiki` | Move/Split；纯值与接口不含 sqlc |
| `wikistore/postgres.go` | `internal/postgres/wiki.go` | Move/Rewrite；SQLC row 转 wiki value |

## 5. Memory owner 前置 ports

| 当前 concrete dependency | Memory 实际需要 | 前置 contract/owner |
| --- | --- | --- |
| `conversation.Accessor` | chat read authorization | `ChatAccessPort`（Agent/API），或由调用端先授权后携带 scope |
| `accounts.Service` AdminChecker | principal admin 判断 | `PrincipalPort`（API Identity） |
| `settings.Service` + `models.Service` + Provider SQL | formation/embedding model selection/execution | `FormationModelPort`、`EmbeddingPort`（Model） |
| `workspace/bridge.Provider` | `/data/memory/*.md` read/write/list/delete | `FilePort`（Runtime） |
| PostgreSQL `dbstore.Queries` | provider config、wiki nodes/edges | Memory-owned stores |
| pgvector Store/SQLC | semantic embedding index | Memory index store，team-bound transaction |
| MCP types | expose `search_memory` tool | Agent tool owns MCP surface，调用 Memory Search |
| `team.DefaultTeamID` | tenant scope | request/RPC tenant context；禁止业务 fallback |

这些ports用于业务所有权和测试隔离；不得据此新增`cmd/memory`、Memory RPC或profile分支。

## 6. Model：DTO、CRUD、client、probe 的 type/function 级拆分

### 6.1 `internal/models`

| 当前文件/函数族 | 目标 | 类型 | 精确动作 |
| --- | --- | --- | --- |
| `types.go` `ModelType/ClientType/ModelConfig/Model` validation | `domains/model/model.go` | Split/Move | 稳定值可公开；HTTP Add/Get DTO 与 internal persistence patch 分开 |
| `models.go` `Create/Get/List/Update/Delete/Count` | `internal/catalog/service.go` + `postgres/model.go` | Split | service 不收 `dbstore.Queries`，不返回 SQLC |
| `models.go` `SelectMemoryModel*`, `FetchProviderByID` | Model consumer ports/local selector | Split | 不返回 `sqlc.Provider`；返回 credential-free execution spec 或执行 client |
| `models.go:FetchProviderByID -> channel.SetIMErrorSecrets` | `internal/secret` boundary sanitizer | Delete/Rewrite | 删除 Model -> Channel 反向 import；provider error 在离开 Model 前清洗，RPC/Agent/Channel 永远不接触原 secret |
| `models.go` `IsValid*` | `domains/model/model.go` | Move | pure vocabulary validation |
| `sdk.go` `SDKModelConfig/NewSDKChatModel/BuildReasoningOptions/ResolveClientType` | `domains/model/internal/execution/chat.go` | Move/Split | Agent经consumer port调用；Provider credentials由resolver注入 |
| `embedding.go` | `domains/model/internal/execution/embedding.go` | Move | embedding client/dimension inference；Memory经port调用 |
| `prompt_cache.go` | `domains/model/internal/execution/cache.go` | Move | model execution policy |
| `http_client.go` | `domains/model/internal/execution/http.go` | Move | provider HTTP timeout/User-Agent transport |
| `chat_completions_compat.go` | `domains/model/internal/execution/chat.go` | Move | provider wire compatibility |
| `config.go` | `internal/catalog/config.go` 或删除 | Delete/Move | 当前仅 SQL raw helper；SQL decode 下沉 postgres |
| `probe.go:Service.Test` | `internal/provider/probe.go` | Split | catalog lookup、credential resolution、network probe 分开 |
| `probe.go:NewSDKProvider` | `domains/model/internal/execution/provider.go` | Move | reusableServer-local constructor |
| `probe.go:resolveModelCredentials/resolveGitHubCopilotAccessToken` | `internal/provider/credential.go` | Split | 不接 `sqlc.Provider`，secret 不进入 response/log |

Model owner的公共API应按consumer需要返回具体client/execution spec，不建立一个包含CRUD、
probe、generate、embed、audio、video 的统一 interface。

### 6.2 Provider CRUD、credential、OAuth、secret

| 当前文件/代码 | 目标 | 类型 | 精确动作 |
| --- | --- | --- | --- |
| `providers/types.go` CRUD/Test/Remote DTO | `domains/model/model.go` + internal DTO | Split | OAuth status可公开；DB/import patch内收 |
| `service.go` CRUD/Count | `internal/provider/service.go` + `postgres/provider.go` | Split | template resolver通过 interface，删除 SQLC leakage |
| `service.go` `Test/FetchRemoteModels` | `internal/provider/{probe,discover}.go` | Split | network operations与CRUD分开 |
| `service.go` template/catalog helpers | `internal/template/instance.go` | Split | `registry.ProviderDefinition` 与 DB template统一 value |
| `mergeProviderConfig/preserveMaskedConfigSecret` | `internal/secret/merge.go` | Move/Rewrite | schema-driven secret keys，不能只硬编码 api_key |
| `maskConfigSecrets/maskAPIKey` | `internal/secret/mask.go` | Move/Rewrite | 所有 list/get/test error path使用同一 redactor |
| `credentials.go` `ModelCredentials/ResolveModelCredentials` | internal credential resolver | Split | public只返回执行 client/spec；token不得 JSON/log |
| `codex_models.go`, `copilot_models.go` | `internal/provider/{codex,copilot}/catalog.go` | Move | remote model discovery |
| `oauth.go` provider flow methods | `internal/oauth/{service,codex,github,http}.go` | Split | 1,500 行按 flow/storage/HTTP 拆文件 |
| `oauth.go` token load/save/state | `internal/postgres/oauth.go` behind store | Split | OAuth service不得持有 `dbstore.Queries/sqlc.ProviderOauthToken` |
| `oauth.go` URL validation/PKCE/state helpers | `internal/oauth/security.go` | Move | SSRF/issuer allowlist、state/verifier保持测试 |
| `oauth.go` account/device metadata | `internal/oauth/value.go` | Move | secret字段禁止进入状态响应 |

安全规则：

- secret merge/mask 由 template config schema 的 `secret` 字段驱动，覆盖 API key、OAuth client
  secret、refresh/access/id token；
- `Get/List/Test/remote model` 与错误日志都不得输出原 secret；
- Model/provider adapter 必须在返回 error 前以当前 execution spec 的 secret 集合清洗；禁止把
  secret 注册到 Channel 全局 map，也禁止依赖最终 UI transport 才 redaction；
- masked placeholder 只表示“不修改”，不得写回数据库；
- OAuth callback state 与 provider/team/principal 绑定，消费必须原子化；
- HTTP client有 timeout，token endpoint校验保持 fail-closed；
- provider-scoped `provider_oauth_tokens` 是当前 active owner；legacy
  `user_provider_oauth_tokens` 只作为明确兼容/rollback 数据，不形成第二 authority。

## 7. Template、Capability、Copilot、Search/Fetch

### 7.1 Template

| 当前文件 | 目标 | 类型 |
| --- | --- | --- |
| `registry/types.go` | `internal/template/source.go` | Move；YAML source value |
| `registry/registry.go:Load` | `internal/template/load.go` | Move |
| `registry/registry.go:Sync` 与 provider match/merge | `internal/template/provider.go` + postgres store | Split |
| `registry/templates.go` | `internal/template/normalize.go` | Move；YAML -> canonical Definition |
| `providertemplates/types.go` | canonical internal template value | Move |
| `providertemplates/service.go` | `internal/template/service.go` + postgres catalog | Split |
| `providertemplates/sync.go` | `internal/template/sync.go` + store | Split；transaction/lock接口由 sync consumer 定义 |
| `providertemplates/instance.go` | `internal/template/instance.go` | Split；禁止返回 SQLC template row |

最终只保留一个 canonical `Definition` 和一个 template owner。`registry` 与
`providertemplates` 两个泛化 package 全部删除，不建立转发 facade。

`conf/providers/*.yaml` 共 42 个，**Keep** 在 `conf/providers`：它们是 operator/build/runtime
catalog assets，不是 Go implementation。`[registry].providers_dir` 仍是外部可配置路径；Model
local loader拥有解析/sync，部署包必须继续包含这些 YAML。

### 7.2 Capability/Copilot/Search/Fetch

| 当前文件族 | 目标 | 类型/说明 |
| --- | --- | --- |
| `capabilities/{discover,match,resolver}.go` | `domains/model/capability` | Move；cmd/synccaps build-time only |
| `copilot/{client,provider}.go` | `internal/provider/copilot` | Move；token cache按 credential hash隔离、过期删除 |
| `fetchproviders/{types,service}.go` | `domains/model` DTO + `internal/fetch/service.go` + postgres | Split |
| `searchproviders/{types,service}.go` | `domains/model` DTO + `internal/search/service.go` + postgres | Split |

Fetch/Search 的 Agent tools 定义各自 reader/executor port；不能从 Agent 导入 Model internal。
当前 `GetRawByID` 返回 SQLC row，目标删除并返回 credential-safe execution config。

## 8. Audio / Video

### 8.1 Audio

| 当前文件 | 目标 | 类型 |
| --- | --- | --- |
| `audio/types.go`, `config.go` | `domains/model/model.go` audio values + internal config | Split |
| `audio/registry.go` | `internal/audio/registry.go` | Move；registry定义只用 Model public ClientType |
| `audio/service.go` | `internal/audio/{catalog,speech,transcription,resolve}.go` + postgres | Split；当前 760+ 行 |
| `audio/bootstrap.go:SyncRegistry` | 删除或 template sync hook | Delete/Decide；当前无生产调用点，provider template sync已接管 catalog |
| `audio/tempstore.go` | `domains/api/http/audio/temp.go` 或 Media temp asset | Split/Move；HTTP test-upload transport，不属于 Model domain |
| `audio/adapter.go` | 删除 | Delete；`TtsAdapter` 无生产 consumer |
| `audio/adapter/edge/{edge,type,ws}.go` + `voices.json` | 删除或 `internal/audio/edge` | Decide/Delete；当前无生产 consumer，Twilight provider registry已有 Edge |

Agent consumer只依赖`SynthesizePort`/`TranscribePort`，由Server-local Model concrete实现。Channel
输入通过final inbound/Turn进入Server后再触发speech，不建立Model RPC。

### 8.2 Video

| 当前文件 | 目标 | 类型 |
| --- | --- | --- |
| `video/types.go` | Model public video DTO + internal values | Split |
| `video/registry.go` | `internal/video/registry.go` | Move |
| `video/service.go` | `internal/video/{catalog,resolve}.go` + postgres | Split |
| `video/bootstrap.go:SyncRegistry` | 删除或 template sync hook | Delete/Decide；当前无生产调用点 |

Image generation目前直接走 `models`/provider SDK，不在独立 `internal/image` 包；迁移时由
Model local execution API承接，避免 Audio/Video各复制 provider/config/secret逻辑。

## 9. OAuth shared packages

`oauthclients` 被 Email Gmail、Plugin、handler 和 core wiring共同使用，因此不能放入
`domains/model/internal`：

- `oauthclients/oauth_clients.go` -> `internal/oauth/client/registry.go`（Move）；
- `conf/oauth-clients.example.toml` -> Keep；operator secret通过 env expansion注入；
- Model/Channel/Plugin各自只依赖小 `Resolver` interface。

`oauthctx/context.go` 当前被 Agent flow、subagent、Model/Provider handler、health checker使用：

- 移到 `internal/oauth/context/principal.go`（Move）；
- basename与package都使用单词 `context`；需要同时导入stdlib `context` 的 consumer在
  import处使用 `oauthctx` alias，不把复合词重新放回package声明；
- 长期应由统一 authenticated principal contract替代；本阶段不把 user ID context复制进
  Memory/Model domain。

## 10. HTTP、Agent/Chat consumer 与 composition

### 10.1 HTTP owner

| 当前 handler | 目标 | 依赖变化 |
| --- | --- | --- |
| `handlers/memory.go` | `domains/api/http/memory/{crud,graph,admin}.go` | 只依赖 Memory contract/client；Bot auth留 API |
| `handlers/memory_providers.go` | `domains/api/http/memory/provider.go` | Memory admin client |
| `handlers/models.go` | `domains/api/http/model/model.go` | Model local catalog port |
| `handlers/providers.go` | `domains/api/http/model/provider.go` | provider/catalog ports；import-model transaction下沉 Model |
| `handlers/provider_templates.go` | `domains/api/http/model/template.go` | template catalog port |
| `handlers/provider_oauth.go`, `acp_codex_oauth.go` | `domains/api/http/model/oauth.go` + ACP API | auth/bot access留 API，OAuth domain进 Model |
| `handlers/fetch_providers.go`, `search_providers.go` | `domains/api/http/model/{fetch,search}.go` | Model ports |
| `handlers/tts_providers.go`, `video_providers.go` | `domains/api/http/model/{audio,video}.go` | Model catalog/execution ports |
| `handlers/bot_tts.go` | `domains/api/http/model/speech.go` | API auth/settings + Model SynthesizePort；temp asset归 API/Media |

### 10.2 Agent/Chat consumers

| 当前 consumer | 目标 import/port |
| --- | --- |
| `agent/tools/memory.go` | Agent-owned tool adapter -> Memory CRUD/Search contract |
| `conversation/flow/resolver_memory.go` | `Recall`/`RecordTurn` ports；不获取 Provider/Registry concrete |
| `handlers/memory.go` graph helper | graph response加入 Memory contract，或 Memory提供Graph query；API不读 provider internal |
| `agent/tools/{image_gen,video_gen,transcribe,tts}.go` | Model local ports；未来 Model remote另立 spec |
| `agent/tools/{web,webfetch}.go` | Search/Fetch execution ports |
| `agent/agent.go`, `resolver_model_selection.go`, `capability_policy.go` | Model public execution spec/client，不导入 catalog internal |
| command model/memory/search actions | 对应 Memory/Model admin ports，不能导入具体 Service |
| model health checker | Model ProbePort；principal context显式传递 |

### 10.3 Wiring

| 当前 wiring | 目标 | 类型 |
| --- | --- | --- |
| `cmd/internal/core/module.go` Memory/Model providers | 对应owner公开constructors；两种Server profile相同 | Split |
| `provideMemoryProviderRegistry` | Memory owner composition | Rewrite；只装ports/store，不收concrete cross-domain services |
| `provideMemoryLLM/lazyLLMClient` | Memory `FormationModelPort` 的Model direct adapter | Split |
| `startProviderTemplateSync` | Model owner lifecycle | Move |
| `configureMemoryProviderRegistry` | Memory internal wiring | Move；不使用setter injection跨owner |
| `provideAudioRegistry/provideVideoRegistry` | Model owner | Move |
| `provideAudioTempStore` | API/Media composition | Move |
| `provideProvidersService` | Model provider composition | Move |

本轮不创建Memory RPC。Recall/Record/CRUD/Admin通过Server-local consumer ports调用；Channel的
final inbound handler若触发Memory行为，也先进入Agent/command application，再本地调用Memory。

## 11. 数据 owner、query、SQLC、migration

### 11.1 Memory owner

| 数据 | 当前 query | 目标 |
| --- | --- | --- |
| `memory_providers` | `db/postgres/queries/memory_providers.sql` | query迁`db/postgres/memory/queries/provider.sql`；adapter为`domains/memory/internal/postgres/provider.go` |
| `memory_nodes`, `memory_edges` | `memory_wiki.sql` | query迁`db/postgres/memory/queries/wiki.sql`；adapter为`.../postgres/wiki.go` |
| semantic embeddings | `db/pgvector/queries/embeddings.sql` | `.../index/postgres.go`，team-bound transaction |
| Markdown `/data/memory` derived view | Workspace Bridge | Runtime-owned FS；Memory通过 FilePort读写 |

### 11.2 Model owner

| 数据 | 当前 query | 目标 |
| --- | --- | --- |
| `providers`, `models`, `model_variants` | `models.sql` | query迁`db/postgres/model/queries/model.sql`；adapter为`domains/model/internal/postgres/{provider,model}.go` |
| provider OAuth | `provider_oauth.sql` | query迁`db/postgres/model/queries/oauth.sql`；adapter为`.../postgres/oauth.go` |
| legacy user OAuth | `user_provider_oauth.sql` | 归`db/postgres/model/queries`的compatibility query或删除，不作active authority |
| template catalog | `provider_templates.sql` | query迁`db/postgres/model/queries/template.sql`；adapter为`.../postgres/template.go` |
| search/fetch configs | `search_providers.sql`, `fetch_providers.sql` | query迁`db/postgres/model/queries/{search,fetch}.sql`；adapter为`.../postgres/{search,fetch}.go` |
| speech/transcription/video catalog | `models.sql` 对应 typed queries | 共用 Model catalog store，禁止 Audio/Video复制 repository |

`settings` 中的 chat/memory/search/fetch/audio/video model/provider references 属 API/Agent bot
configuration owner；Memory/Model只提供 ID validation/snapshot port，不直接接管 settings 表。

`internal/db/store.{Queries,ModelStore,ProviderStore,ProviderOAuthStore,
UserProviderOAuthStore,SearchProviderStore,MemoryProviderStore,RegistryStore,AudioCatalogStore}`
是过渡层。目标 consumer-owned stores下沉各 domain。迁移期旧
`internal/db/postgres/sqlc` output package Keep且imports只减不增；owner adapter/port稳定后，
query按statement owner拆到`db/postgres/<owner>/queries`，每个owner持有自己的`sqlc.yaml`，
输出到`domains/<owner>/internal/postgres/sqlc`。仓库级命令可以统一调用这些配置，但最终不能
共享一个giant config/output package，generated type也只能被同owner postgres adapter导入。

Migration 历史全部 **Keep**，包括但不限于 `0001_init`、`0020_memory_providers`、
`0025_repair_memory_providers`、`0041_provider_model_refactor`、`0046_llm_provider_oauth`、
`0061_unify_providers`、`0062_github_copilot_user_oauth`、`0069/0071` speech/transcription、
`0093_fetch_providers`、`0099_video_providers`、`0104_memory_wiki`、
`0106_provider_oauth_metadata`、`0112_team_core`、`0114_provider_templates`，以及 pgvector
`0001_init`/`0002_team_isolation`。目录重构不改写历史；新 schema语义另立成对 migration，
同时维护 canonical `0001_init.up.sql`。

## 12. 配置与资产

主范围包内资产：

- `memory/memllm/prompts/{memory_extract,memory_update}.md` ->
  `domains/memory/internal/formation/prompts/`；
- `audio/adapter/edge/voices.json` -> 随 legacy Edge 删除，或随 `internal/audio/edge` Move；
- embed directive与测试同提交更新。

相关 operator资产：

- `conf/providers/*.yaml`：42/42 Keep，Model template loader owner；
- `conf/oauth-clients.example.toml`：Keep，共享 OAuth client registry owner；
- `conf/app.{example,docker,apple,windows,kata.docker}.toml` 的 `[registry]`、
  `[oauth_clients]`、`[pgvector]`：Keep；本轮不新增Memory RPC target。

Secret配置示例只能引用 env变量/placeholder；文档、日志、测试golden均不得写真实 token。

## 13. 测试迁移

生产文件Move时测试随职责移动；Split文件测试拆到contract、algorithm、store和owner integration，
不建立local/RPC parity或旧package facade。

Memory 当前测试：

- `memory/adapters`：`context_cache_test.go,helpers_test.go,service_test.go,types_test.go`；
- builtin 10 个：`builtin,context_packer,file_runtime,formation,graph_runtime,ingest,
  pgvector_index,retry,shared,store_test.go`；
- memllm：`client_test.go,prompts_test.go`；migrate/segment/slug/storefs各 1；
- mem0/openviking/wikistore当前无测试：若保留必须新增 unsupported/store contract测试。

Model 当前测试：

- models 7 个：`catalog_availability,description,http_client,prompt_cache,sdk,enable,models_test.go`；
- providers 4 个：`copilot_models,description,oauth_scope,service_test.go`；
- registry 2、providertemplates `sync_test.go`、capabilities 2、copilot 1、search 1；
- Audio Edge：`edge_test.go,ws_test.go` 及 build-tag ignored `ws_integration_test.go`；
- Video：`service_test.go`；Fetch/Audio root/OAuth shared当前无测试，迁移前需补 store/secret/
  config/registry contract tests。

必须新增：

- Memory local vs gRPC Recall/Record/CRUD/error/team/cancel parity；
- Registry concurrent Get/Remove/Close 与 retry shutdown；
- provider secret merge/mask覆盖所有 schema secret；
- OAuth state一次消费、team/principal binding、token refresh/redaction；
- Model local catalog/probe与 template sync transaction parity；
- split Server dependency closure不含 Memory internal/local/pgvector/provider SDK。

## 14. 文件覆盖账本

| 当前 package | 生产 | 测试 | 生产文件 |
| --- | ---: | ---: | --- |
| `memory/adapters` | 6 | 4 | `context_cache,helpers,provider,registry,service,types.go` |
| `memory/adapters/builtin` | 13 | 10 | `builtin,compact,context_packer,factory,file_runtime,formation,graph_cache,graph_runtime,graph_sync,ingest,pgvector_index,retry,shared.go` |
| `memory/adapters/{mem0,openviking}` | 2 | 0 | 各 `mem0.go`/`openviking.go` |
| `memory/memllm` | 2 | 2 | `client.go,prompts.go` |
| `memory/{migrate,segment,slug,storefs}` | 4 | 4 | `tomemorywiki.go,segment.go,slug.go,service.go` |
| `memory/wikistore` | 3 | 0 | `conv.go,postgres.go,store.go` |
| `models` | 9 | 7 | `chat_completions_compat,config,embedding,http_client,models,probe,prompt_cache,sdk,types.go` |
| `providers` | 6 | 4 | `codex_models,copilot_models,credentials,oauth,service,types.go` |
| `registry` | 3 | 2 | `registry.go,templates.go,types.go` |
| `providertemplates` | 4 | 1 | `instance.go,service.go,sync.go,types.go` |
| `capabilities` | 3 | 2 | `discover.go,match.go,resolver.go` |
| `copilot` | 2 | 1 | `client.go,provider.go` |
| `fetchproviders` | 2 | 0 | `service.go,types.go` |
| `searchproviders` | 2 | 1 | `service.go,types.go` |
| `audio` | 7 | 0 | `adapter,bootstrap,config,registry,service,tempstore,types.go` |
| `audio/adapter/edge` | 3 | 3 | `edge.go,type.go,ws.go`；含 ignored integration test |
| `video` | 4 | 1 | `bootstrap.go,registry.go,service.go,types.go` |
| `oauthclients` | 1 | 0 | `oauth_clients.go` |
| `oauthctx` | 1 | 0 | `context.go` |
| **合计** | **77** | **42** | **119 个 Go 文件全部映射** |

覆盖校验包含 build-tag ignored Go 文件：

```bash
scope=(internal/memory internal/models internal/providers internal/registry \
  internal/providertemplates internal/capabilities internal/copilot \
  internal/fetchproviders internal/searchproviders internal/audio internal/video \
  internal/oauthclients internal/oauthctx)
root=$PWD
find "${scope[@]}" -type f -name '*.go' -print \
  | sed "s#^#$root/#" | sort > /tmp/memory-model-current.txt
go list -f '{{.Dir}}|{{join .GoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}|{{join .IgnoredGoFiles ","}}' \
  ./internal/memory/... ./internal/models/... ./internal/providers/... \
  ./internal/registry/... ./internal/providertemplates/... ./internal/capabilities/... \
  ./internal/copilot/... ./internal/fetchproviders/... ./internal/searchproviders/... \
  ./internal/audio/... ./internal/video/... ./internal/oauthclients/... ./internal/oauthctx/... \
  | awk -F'|' '{d=$1; for(i=2;i<=5;i++){n=split($i,a,","); for(j=1;j<=n;j++) if(a[j] ~ /\.go$/) print d "/" a[j]}}' \
  | sort -u > /tmp/memory-model-ledger.txt
comm -23 /tmp/memory-model-current.txt /tmp/memory-model-ledger.txt
comm -13 /tmp/memory-model-current.txt /tmp/memory-model-ledger.txt
```

基线结果：

```text
current Go files = 119
mapped Go files  = 119
uncovered        = 0
extra            = 0
production       = 77
tests            = 42
main-scope assets = 3/3
related config assets = 48/48
```

## 15. 推荐提交序列与验收

1. 冻结 Memory Recall/Record/CRUD/Admin contract与 Model public values；
2. 先建Memory-owned postgres/pgvector Store与Model/Runtime/API ports，在旧SQLC上实现adapter；
   每切一组consumer就删除对应broad Queries method。
3. 在旧namespace拆Memory巨型Provider/MCP、Registry/CRUD和事务边界，先不移动算法。
4. Consumer port与Store graph闭合后机械迁builtin/formation/recall/index/store；Move不混合行为变化。
5. 两种Server profile始终直接构造Memory/Model；不新增`cmd/memory`、RPC或profile分支。
7. 统一Goose Migrator按Database Epoch v2把Memory/Model迁入各自schema/version table；
   fresh baseline与v1 bridge通过后cutover。
8. 最后按owner拆postgres/pgvector SQLC target并重生成，删除旧generated/package/facade。
9. Model继续按Store -> ports -> Move -> Database Epoch v2 -> generated cleanup拆catalog/provider/template/secret/
   OAuth，并合并YAML registry与DB template语义。
10. Search/Fetch/Audio/Video和handlers/Agent consumers随各自owner Gate迁移；dead Audio Edge/
   SyncRegistry先以production/config证据决定保留或删除。

验收：

```bash
go test ./domains/memory/... ./domains/model/...
go build ./cmd/agent
go build -tags split ./cmd/agent
go test ./cmd/synccaps/...
```

Memory与Model应出现在split Server依赖闭包。目录Move、contract change、behavior change和
SQLC生成更新必须分提交；本账本不产生Proto更新。
