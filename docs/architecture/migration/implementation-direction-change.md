# Implementation Direction Change: Channel-Only Process Boundary

状态：**立即生效，供所有落地 Agent 执行**

权威决策：`docs/architecture/process-boundary-rpc-decision.md`

## 立即停止

正在实现以下内容的 lane 应停止继续扩展，并先报告已有diff：

- `domains/api/grpc`、`domains/model/grpc`、`domains/memory/grpc`、
  `domains/runtime/grpc`、`domains/media/grpc`（含任何把这些目标落在旧根名
  `services/*/grpc` 的 lane）；
- 为上述owner建立的`local` wrapper、standalone module或Channel command client；
- 将command executor/catalog、skill resolution、model catalog、speech、memory catalog、runtime
  file等业务迁入Channel；
- Channel直接调用多个Server owner RPC；
- 每个domain复制`contract/local/grpc/internal`模板；
- 任何generic/typed双注册、fallback、compat DTO/package、mixed-version或旧Proto保留。

不要自行删除或回退共享工作树内容。先隔离自己的lane、列出文件和consumer，再由主协调lane
决定保留、改向或删除。

## 继续保留

以下工作方向不变，可以继续：

- owner识别、Store/Reader/Writer/Transactor下沉；
- PostgreSQL错误在owner adapter分类，业务代码不import pgx/SQLC/global Store；
- Agent transaction cluster、lock order、fencing和失败恢复；
- Channel-owned config、identity、route、event、cursor、passive message persistence；
- 已完成且确属Server -> Channel边界的Channel Admin/Status typed cutover；
- Database Epoch v1/v2、legacy archive、v1 -> v2 bridge、Goose manifest/schema/version table；
- 安装包文件名和service-manager路径兼容；
- build tag只选择embedded/split composition，不散入业务代码。

## 新目标

```text
Server = API + Agent + Memory + Model + Runtime + Media
Channel = Channel + Email + external adapters
RPC = only Server <-> Channel process boundary
```

Channel只负责外部平台连接、协议标准化、投递和观察。Command、Skill、Session、Model、Memory、
Runtime、Media和Agent loop均在Server执行。

API HTTP/SSE/WS也留Server，不为它建立RPC层。当前SSE只保证通过持久化snapshot/REST最终重建，
不是基于`Last-Event-ID`的事件级无损恢复；不得把“可重连”写成“横向伸缩无损”。

Channel -> Server不再拆成多个owner RPC。实现者应等待最终typed inbound/Turn contract冻结；它
承载标准化inbound event、interaction/control和稳定Channel facts。Server收到后通过本地Go调用
访问各owner业务。

## 每个lane的动作

1. 重新读取`process-boundary-rpc-decision.md`和重写后的`service-rpc-channel.md`。
2. 检查自己的diff是否创建非Channel owner的`grpc`/`local`/proto或把业务搬进Channel。
3. 若有，停止接线，不再增加测试或generated code；报告文件、call sites和可保留的纯业务部分。
4. 将consumer interface留在真实消费方；Server owner之间使用直接Go调用，不增加transport adapter。
5. 只有跨Server/Channel的capability才写RPC parity test。
6. 不触碰migration、query拆分、sqlc regeneration、Goose runner或安装兼容，除非lane原本已获授权。

## 完成判定

- Model/Memory/Runtime/Media没有因Channel拆分而获得独立RPC层；
- Channel child不import这些owner的concrete/internal，也不执行其业务；
- Server split build不编译Channel implementation；
- Embedded直接调用Channel concrete，split通过Channel boundary client；split Server同时注册
  final inbound/Turn handler供Channel提交输入；
- generic RPC按完整capability原子删除，不存在双路径；
- Send/Turn/Asset/Metadata未闭合时保持Blocked，不实现缩水子集。
