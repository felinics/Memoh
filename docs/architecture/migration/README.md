# Detailed Migration Ledgers

状态：六个domain账本、HTTP transport账本与composition/release账本均已完成覆盖审计；
2026-07-23已批准process-boundary RPC方向重置。七个 `domains/<owner>` 只表示代码 owner/领域隔离，不是 Go module、进程或部署单元；首期只有Server与
Channel两个业务进程；等待final inbound/Turn、AD1、D11、D12和剩余迁移Gate闭合。

本目录将 `docs/architecture/build-profile-service-blueprint.md` 中的架构级映射下钻为
逐 package、逐文件/职责文件族的迁移账本。所有账本基于：

```text
/Users/bbq/Code/Memoh-Cloud/Memoh-main
origin/main@257f894f54140ab60d0860ee5c1fb272b6ef51d5
```

## 账本格式

每项至少包含：

1. 当前 package path 和 package name；
2. 当前生产文件、测试、生成代码、embed/proto/SQL 资源；
3. 当前公开 type/function 与主要 consumer；
4. 目标 domain、目录和 package name；
5. `Move`、`Split`、`Keep`、`Delete` 或 `Decide`；
6. 需要迁入/迁出的具体代码；
7. 前置 consumer-owned interface、数据 owner 和事务边界；
8. import 方向变化；
9. wiring、配置、RPC/HTTP 和生成步骤变化；
10. 测试迁移与验收命令；
11. 当前范围的文件覆盖计数和未覆盖清单。

职责不同的代码不能因为位于同一个当前 package 就写成一条机械 Move。目标 package
也不能为了保留旧 import 而建立长期转发 facade。

## 账本索引与覆盖结果

| 账本 | 机械覆盖结果 | 主要内容 |
| --- | --- | --- |
| `api-http.md` | 115/115 Go（67 production + 48 tests），missing/extra/duplicate 0/0/0 | 全部 Handler、route/method/symbol split、OpenAPI/Swagger/SDK |
| `api-identity-bot.md` | 43 primary + 13 Handler cross-reference = 56/56，missing/stale 0/0 | Identity、Bot、ACL、Access domain与对应HTTP port语义 |
| `agent-chat-turn.md` | 408/408 Go（219 production + 189 tests），missing/extra 0/0 | Turn、Chat、Message/Session transaction、Tools、ACP、Automation |
| `agent-run-journal.md` | Discussion Draft；不计入迁移覆盖分母 | Agent run/Turn durable journal、checkpoint、SaaS横向扩缩容一致性和恢复语义 |
| `channel.md` | 346/346 domain Go（202 production + 144 tests）及 4/4 owned assets | 外部 adapter、Inbound/Outbound、Email、Command；RPC与`cmd`仅作symbol cross-reference |
| `memory-model.md` | 119/119 Go（77 production + 42 tests），missing/extra 0/0 | Memory SPI/provider/store、Model/Provider/Template/OAuth/config |
| `runtime-media.md` | 135/135（90 production Go + 44 tests + 1 proto），missing/stale 0/0 | Container、Workspace/Bridge、Network、Display、Media/Storage/Attachment |
| `persistence-iam.md` | 459/459 assets，missing/extra 0/0 | 122 Go、287 SQL、1 proto、1 sqlc config、48 config；46/46 query owner、118/118 migration ID review owner |
| `database-epoch-v2.md` | Accepted decision；不计入旧资产coverage分母 | 统一Goose Migrator、owner schema/version table、v1到v2 bridge |
| `../process-boundary-rpc-decision.md` | Accepted decision | 七业务owner/两进程；RPC只位于Server/Channel真实边界；command留Server |
| `service-rpc-channel.md` | 20/20 generic RPC重新归并；transport closure Blocked | Server -> Channel typed capability；Channel -> Server单一inbound/Turn；无per-owner reverse RPC |
| `implementation-direction-change.md` | Active implementation notice | 正在落地lane的停止、保留、改向和报告要求 |
| `cross-module-review.md` | 7份蓝图/账本交叉审计 | owner/path/coverage/build-profile 冲突、已一致决策和实施 Gate |
| `composition-release.md` | 40/40 primary + 26/26 compatibility surface | `cmd` composition、build profile、Docker/Air/Compose、CI、release、mise |

这些分母是各账本的机械审计范围，包含跨模块 handlers、composition、generated 和资产，
存在有意的关联面审计，不能相加当作仓库文件总数。唯一性约束应用于下面的 81 个
`internal` 一级目录主审 owner，以及 type/function/data writer 的最终归属。

## Normative 实施顺序

所有模块账本必须遵循同一阶段依赖：

```text
Guard / owner freeze
  -> Store 与 transaction boundary
  -> Channel boundary typed RPC 与 `cmd/{agent,channel}` composition
  -> build profile（只裁剪Channel implementation）
  -> mechanical Move
  -> Database Epoch v2（统一Goose Migrator、owner独立schema）
  -> generated SQLC/proto/OpenAPI/SDK cleanup
```

“Store先行”不是先全仓重写Store/SQLC：每个owner按vertical slice定义consumer-owned port，
先用旧SQLC实现adapter并切consumer，再移动domain；per-owner SQLC重生成位于该slice最后。
生产transaction runner不得在未开启真实事务时直接执行callback；测试必须显式使用fake
transaction或真实PostgreSQL transaction。

Server <-> Channel RPC尚未发布。`domains/<owner>` 不是 Go module、进程或部署单元；composition root
最终留在 `cmd/agent` 与 `cmd/channel`，禁止 `internal/composition` 与长期 `cmd/internal/process`。首期只有Channel从Server拆出。
Typed RPC只服务这条真实进程边界，不为API/Agent/Model/Memory/Runtime/Media对称创建
`local/grpc`。Command、Skill、Model、Memory、Runtime、Media业务留Server；Channel通过最终typed
inbound/Turn入口提交标准化输入。当前generic JSON RPC与Turn proto只作行为盘点输入，不保留旧
package/service/field，不建立v1/v2、mixed-version、dual-register、health probe、fallback或协议
迁移。首个split发布要求Server/Channel同版本原子部署；未闭合capability直接阻断切换。

Agent/Runtime等Server owner必须先建立稳定consumer ports和公开constructors再Move；除Channel
外typed RPC和build-profile Gate均为N/A，禁止为了目录对称预造空transport。不同lane可并行
准备characterization、contract和guard；共享`sqlc.yaml`、
`messages.sql`、`sessions.sql`、generated output与最终profile合并必须串行。

Database Epoch v2的normative contract见`database-epoch-v2.md`：v1 migration保持冻结；v2每个
owner拥有独立schema、从`00001`开始的migration序列和schema内`goose_db_version`表，由唯一
Migrator统一执行。Embedded/split不得各自维护或启动时自动升级数据库。

Active migration不使用`epochs/v2`目录：固定放在`db/postgres/<owner>/migrations`。旧v1源码
归档到`db/postgres/legacy/v1/migrations`，其一次性bridge放在同一legacy来源下的
`upgrade/to_v2`；upgrade不是owner，不与`platform/api/agent/...`并列，也没有独立schema或
version table。

每个owner的手写query与SQLC配置分别位于`db/postgres/<owner>/queries`和
`db/postgres/<owner>/sqlc.yaml`；生成Go代码位于`domains/<owner>/internal/postgres/sqlc`，
不能放进`db`。Store/Reader/Writer/Transactor接口由消费它的use case定义，PostgreSQL adapter
实现这些接口；禁止建立新的全局`db/store`或返回giant `*sqlc.Queries`。

## 两轮审计范围

### 第一轮

| 账本 | 一级 package 范围 |
| --- | --- |
| `api-http.md` | `handlers`（全部115个Go文件的唯一primary ledger） |
| `api-identity-bot.md` | `accounts auth team identity bots settings botbackup acl channelaccess policy server httpx embedded` |
| `agent-chat-turn.md` | `agent agentpayload acpagent acpclient acpfeedback acpprofile conversation message messageconv session sessionruntime runtimefence compaction historyfrag contextfrag contextlimit decision toolapproval userinput mcp hooks skills plugins schedule heartbeat prune` |
| `channel.md` | `channel email webhooktunnel messaging command commandsyntax slash pipeline i18n` |

### 第二轮

| 账本 | 一级 package 范围 |
| --- | --- |
| `memory-model.md` | `memory models providers registry providertemplates capabilities copilot fetchproviders searchproviders audio video oauthclients oauthctx` |
| `runtime-media.md` | `container workspace network display userruntime storage media attachment boot` |
| `persistence-iam.md` | `db config logger healthcheck apperror arch version textutil timezone rpc` |

`team`、OAuth、Attachment、Media、RPC 等存在跨账本 consumer。唯一 owner 由总蓝图决定，
其他账本只能记录依赖或迁出动作，不能各自创建第二份实现。

`handlers`、`rpc`、`db/postgres/sqlc` 等巨型共享面会被多个账本按 symbol/route/query
交叉审计；表中的一级目录只表示协调主审，不表示该目录所有代码会迁入同一个 domain。

## 全局覆盖验收

两轮账本必须覆盖当前全部 81 个 `internal` 一级目录，且每个目录只有一个主审账本。

```bash
find internal -mindepth 1 -maxdepth 1 -type d -exec basename {} \; | sort
```

逐账本还必须从其范围生成当前 Go 文件清单，并证明：

```text
current files = mapped files + explicitly excluded generated/testdata files
unmapped files = 0
```

文件族只有在目标路径、迁移类型和前置条件完全一致时才能合并记录。巨型文件必须按
type/function/route 区段拆解，不能仅写文件级目标。

## 交叉校验

所有账本完成后统一检查：

- 一个当前 type/function 不得迁入两个 owner；
- 一个目标 package 不得同时由两个账本定义不兼容职责；
- domain A 的 `internal` 不得被 domain B 直接导入；
- split Server的目标依赖不得触达Channel `internal`或外部adapter；
- DB query、migration 和 generated SQLC 的 owner 与写入语义一致；
- v2 schema、Goose version table、migration目录和运行角色与同一owner机械一致；
- 仅跨Channel边界的direct与RPC adapter使用同一contract并有parity test；
- package basename 与 `package` 声明一致；
- Move、contract change、behavior change、generated update 分成可独立 review 的步骤。
- 所有模块遵循本文件Normative顺序；局部账本只能增加前置Gate，不能把Move/build tag提前。

当前已机械验证：81 个现存 `internal` 一级目录对应 81 个主审分配，没有重复、遗漏或
陈旧目录。生产代码仍位于原路径；这些文档只记录迁移前后映射与前置改造。
