# Channel边界拆分实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按已批准的spec（`docs/superpowers/specs/2026-07-17-channel-boundary-design.md`）落地Channel边界：Turn契约包、Channel切换到port、目录重组、命令装配模块与`cmd/channel`验证二进制。

**Architecture:** 新增`internal/agent/turn`契约包（命令＋事件＋port），`internal/agent/turn/inprocess`适配器包装`flow.Resolver`（chat）与discuss编排；Channel入站Processor与DiscussDriver只依赖port；`cmd/agent`的providers拆到`cmd/internal/core`与`cmd/internal/channel`两个fx模块。

**Tech Stack:** Go 1.25（mise管理）、Uber FX、pgx/v5、Vitest不涉及。测试用标准`go test`。

## Global Constraints

- 分支：`feat/channel-boundary`，自`main`创建；全程单分支多提交，最后开一个PR（用户决定，替代spec第9节的五个独立PR）。
- 提交信息结尾加：`Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。
- 不修改`internal/db/postgres/sqlc/`（sqlc生成）；本计划不涉及SQL变更。
- lint用`mise exec go@1.25.7 -- golangci-lint run`（mise.toml钉1.25.6与go.mod不匹配的已知绕过；执行时先验证是否仍需要）。
- 每阶段结束：`go build ./... && go test ./...`必须全绿再commit。
- 行为不变约束：all-in-one（`cmd/agent`）对外行为、配置、部署方式完全不变；现有channel与pipeline测试不得删改断言（只允许更新构造方式）。
- 契约规则（spec§5.1）：命令必填`TeamID`，适配器fail-closed校验；契约不携带函数与Go channel；禁止契约层回退`team.DefaultTeamID`。

---

## Phase 0：准备

### Task 0.1: 创建分支

- [ ] **Step 1: 确认工作区干净并创建分支**

```bash
git -C /Users/acbox/projects/memoh/Memoh status --short   # 预期：仅untracked的docs/superpowers/与.claude/worktrees/
git -C /Users/acbox/projects/memoh/Memoh checkout -b feat/channel-boundary
```

- [ ] **Step 2: 提交spec与plan文档**

```bash
git add docs/superpowers/specs/2026-07-17-channel-boundary-design.md docs/superpowers/plans/2026-07-17-channel-boundary-split.md
git commit -m "docs: add channel boundary split spec and plan"
```

---

## Phase 1：契约包`internal/agent/turn`＋进程内适配器（chat模式）

### Task 1.1: 契约类型

**Files:**
- Create: `internal/agent/turn/turn.go`
- Test: `internal/agent/turn/turn_test.go`

**Interfaces（Produces）:** `turn.StartTurnCommand`、`turn.Event`、`turn.RunHandle`、`turn.Service`、`turn.InjectMessage`、`turn.Attachment`、`turn.OutboundAssetRef`、`turn.SkillActivation`、`turn.RequestedSkillContext`——后续所有任务按此签名消费。

- [ ] **Step 1: 写契约类型**

`internal/agent/turn/turn.go`（完整内容）：

```go
// Package turn defines the application-level contract for starting and
// observing agent turns. It is the only surface Channel may depend on;
// it must not import Echo, fx, sqlc, or any channel package.
package turn

import (
	"context"
	"encoding/json"
)

type Mode string

const (
	ModeChat    Mode = "chat"
	ModeDiscuss Mode = "discuss"
)

// Attachment mirrors conversation.ChatAttachment as boundary-owned data.
type Attachment struct {
	Type        string         `json:"type"`
	Base64      string         `json:"base64,omitempty"`
	Path        string         `json:"path,omitempty"`
	URL         string         `json:"url,omitempty"`
	PlatformKey string         `json:"platform_key,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Name        string         `json:"name,omitempty"`
	Mime        string         `json:"mime,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type SkillActivationSkill struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	SourceKind  string `json:"source_kind,omitempty"`
	State       string `json:"state,omitempty"`
}

type SkillActivation struct {
	Skills []SkillActivationSkill `json:"skills,omitempty"`
	Prompt string                 `json:"prompt,omitempty"`
}

type RequestedSkillContext struct {
	Name           string
	Description    string
	Content        string
	SourceKind     string
	OpaqueSourceID string
	ContentHash    string
	Identity       string
}

// OutboundAssetRef mirrors conversation.OutboundAssetRef.
type OutboundAssetRef struct {
	ContentHash string
	Role        string
	Ordinal     int
	Mime        string
	SizeBytes   int64
	StorageKey  string
	Name        string
	Metadata    map[string]any
}

// InjectMessage carries a user message injected into a running turn
// between tool rounds.
type InjectMessage struct {
	Text            string
	Attachments     []Attachment
	HeaderifiedText string
}

// StartTurnCommand is a pure-data command. Field set mirrors exactly what
// the channel inbound processor supplies today (audited against
// conversation.ChatRequest); function- and channel-typed fields are
// intentionally excluded — injection goes through RunHandle.Inject and
// outbound assets through RunHandle.AddOutboundAssets.
type StartTurnCommand struct {
	SchemaVersion int
	TeamID        string // required; adapter fails closed when empty
	Mode          Mode

	BotID                   string
	ChatID                  string
	SessionID               string
	RouteID                 string
	Token                   string
	ChatToken               string
	UserID                  string
	SourceChannelIdentityID string
	DisplayName             string

	IdempotencyKey    string // derived from platform external message id
	ExternalMessageID string
	EventID           string

	Query           string
	ModelQuery      string
	UserMessageKind string
	UserVisibleText string
	Attachments     []Attachment

	ReplyTarget            string
	ConversationType       string
	ConversationName       string
	SourceReplyToMessageID string
	ReplySender            string
	ReplyPreview           string
	ReplyAttachments       []Attachment
	MentionsBot            bool
	RepliesToBot           bool

	ForwardMessageID          string
	ForwardFromUserID         string
	ForwardFromConversationID string
	ForwardSender             string
	ForwardDate               int64

	CurrentChannel string
	Channels       []string

	Model             string
	ReasoningEffort   string
	WorkspaceTargetID string

	SkillActivation      *SkillActivation
	RequestedSkills      []RequestedSkillContext
	SkipMemoryExtraction bool
	SkipTitleGeneration  bool
	UserMessagePersisted bool

	// Discuss-mode extras (Phase 3).
	SessionToken string
	ToolHTTPURL  string
}

// Event is one element of a turn's event stream. Payload is the raw JSON
// chunk exactly as produced by the runtime; Kind is the parsed "type"
// field (best effort, empty when unparsable). Seq is monotonically
// increasing per run.
type Event struct {
	RunID     string
	TeamID    string
	SessionID string
	Seq       int64
	Kind      string
	Payload   json.RawMessage
}

// RunHandle observes and steers one running turn. Events and Errs mirror
// the runtime's chunk/error channel pair; both close when the run ends.
type RunHandle interface {
	RunID() string
	Events() <-chan Event
	Errs() <-chan error
	Inject(ctx context.Context, msg InjectMessage) error
	AddOutboundAssets(refs []OutboundAssetRef)
	Cancel()
}

type Service interface {
	StartTurn(ctx context.Context, cmd StartTurnCommand) (RunHandle, error)
}
```

- [ ] **Step 2: 写失败测试（TeamID校验属于适配器，这里先测类型序列化往返）**

`internal/agent/turn/turn_test.go`：

```go
package turn

import (
	"encoding/json"
	"testing"
)

func TestEventPayloadRoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"type":"text_delta","text":"hi"}`)
	e := Event{RunID: "r1", TeamID: "t1", Seq: 1, Kind: "text_delta", Payload: raw}
	if string(e.Payload) != string(raw) {
		t.Fatalf("payload mutated: %s", e.Payload)
	}
}
```

- [ ] **Step 3: 运行测试确认通过**

```bash
go test ./internal/agent/turn/ -v
```
预期：PASS。

### Task 1.2: 进程内适配器（chat）

**Files:**
- Create: `internal/agent/turn/inprocess/adapter.go`
- Create: `internal/agent/turn/inprocess/convert.go`
- Test: `internal/agent/turn/inprocess/adapter_test.go`

**Interfaces:**
- Consumes: `flow.Runner`（`StreamChat(ctx, conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error)`）、Task 1.1的全部类型。
- Produces: `inprocess.New(runner flow.Runner) *Adapter`；`*Adapter`实现`turn.Service`。

- [ ] **Step 1: 写失败测试**

`internal/agent/turn/inprocess/adapter_test.go`（要点：假Runner回放chunk，断言Event顺序、Seq单调、Payload逐字节相等、Kind取自JSON type字段；TeamID为空时StartTurn报错；Cancel后Events关闭；Inject送达ChatRequest.InjectCh；AddOutboundAssets被OutboundAssetCollector读回）：

```go
package inprocess

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/conversation"
)

type fakeRunner struct {
	gotReq conversation.ChatRequest
	chunks []string
}

func (f *fakeRunner) StreamChat(ctx context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error) {
	f.gotReq = req
	ch := make(chan conversation.StreamChunk, len(f.chunks))
	errCh := make(chan error)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, c := range f.chunks {
			ch <- conversation.StreamChunk(json.RawMessage(c))
		}
	}()
	return ch, errCh
}

func TestStartTurnRequiresTeamID(t *testing.T) {
	a := New(&fakeRunner{})
	_, err := a.StartTurn(context.Background(), turn.StartTurnCommand{Mode: turn.ModeChat})
	if err == nil {
		t.Fatal("want error for empty TeamID")
	}
}

func TestStartTurnStreamsEvents(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"text_delta","text":"a"}`, `{"type":"done"}`}}
	a := New(r)
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "team1", Mode: turn.ModeChat, BotID: "b", SessionID: "s", Query: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []turn.Event
	for e := range h.Events() {
		events = append(events, e)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Kind != "text_delta" || events[1].Kind != "done" {
		t.Fatalf("kinds = %q, %q", events[0].Kind, events[1].Kind)
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seq not monotonic: %d, %d", events[0].Seq, events[1].Seq)
	}
	if string(events[0].Payload) != r.chunks[0] {
		t.Fatalf("payload mutated: %s", events[0].Payload)
	}
	if r.gotReq.BotID != "b" || r.gotReq.Query != "hi" {
		t.Fatalf("ChatRequest not translated: %+v", r.gotReq)
	}
}

func TestInjectAndAssets(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	a := New(r)
	h, _ := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err := h.Inject(context.Background(), turn.InjectMessage{Text: "more"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-r.gotReq.InjectCh:
		if got.Text != "more" {
			t.Fatalf("inject text = %q", got.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("inject not delivered")
	}
	h.AddOutboundAssets([]turn.OutboundAssetRef{{ContentHash: "h1"}})
	refs := r.gotReq.OutboundAssetCollector()
	if len(refs) != 1 || refs[0].ContentHash != "h1" {
		t.Fatalf("assets = %+v", refs)
	}
	for range h.Events() {
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./internal/agent/turn/inprocess/ -v
```
预期：FAIL（包不存在）。

- [ ] **Step 3: 实现适配器**

`internal/agent/turn/inprocess/convert.go`：`toChatAttachments`／`fromTurnAttachments`／`toConversationInject`／`fromAssetRefs`等逐字段拷贝函数（`turn.Attachment`↔`conversation.ChatAttachment`、`turn.OutboundAssetRef`↔`conversation.OutboundAssetRef`、`turn.SkillActivation`↔`conversation.SkillActivation`、`turn.RequestedSkillContext`↔`conversation.RequestedSkillContext`，字段名一一对应，无逻辑）。

`internal/agent/turn/inprocess/adapter.go`核心：

```go
// Package inprocess adapts turn.Service onto the in-process flow.Resolver.
// It is the migration-phase implementation; a cross-process transport will
// replace it behind the same contract.
package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/conversation"
	"github.com/felinics/memoh/internal/conversation/flow"
)

type Adapter struct {
	runner flow.Runner
}

func New(runner flow.Runner) *Adapter { return &Adapter{runner: runner} }

func (a *Adapter) StartTurn(ctx context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	if cmd.TeamID == "" {
		return nil, errors.New("turn: TeamID is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	injectCh := make(chan conversation.InjectMessage, 16)
	var (
		assetMu sync.Mutex
		assets  []conversation.OutboundAssetRef
	)
	req := chatRequestFromCommand(cmd) // convert.go: 逐字段翻译
	req.InjectCh = injectCh
	req.OutboundAssetCollector = func() []conversation.OutboundAssetRef {
		assetMu.Lock()
		defer assetMu.Unlock()
		out := make([]conversation.OutboundAssetRef, len(assets))
		copy(out, assets)
		return out
	}
	chunkCh, errCh := a.runner.StreamChat(runCtx, req)
	h := &runHandle{
		id:       uuid.NewString(),
		events:   make(chan turn.Event, 16),
		errs:     make(chan error, 1),
		cancel:   cancel,
		injectCh: injectCh,
		addAssets: func(refs []turn.OutboundAssetRef) {
			assetMu.Lock()
			defer assetMu.Unlock()
			assets = append(assets, fromAssetRefs(refs)...)
		},
	}
	go h.pump(cmd, chunkCh, errCh)
	return h, nil
}

func parseKind(p json.RawMessage) string {
	var env struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(p, &env) != nil {
		return ""
	}
	return env.Type
}
```

`runHandle.pump`：for-select消费chunkCh/errCh；每个chunk包成`turn.Event{RunID, TeamID: cmd.TeamID, SessionID: cmd.SessionID, Seq: 自增, Kind: parseKind(chunk), Payload: chunk}`发到`h.events`；error转发`h.errs`；两个上游都关后关闭两个下游。`Inject`向`injectCh`非阻塞发送（ctx/runCtx结束返回错误）。`Cancel()`调`cancel`。

- [ ] **Step 4: 运行确认通过**

```bash
go test ./internal/agent/turn/inprocess/ -v && go build ./...
```
预期：PASS。

- [ ] **Step 5: 对拍测试（chunk类型枚举完整性）**

在`internal/channel/inbound/channel_test.go`中找出`mapStreamChunkToChannelEvents`的全部fixture chunk（约3273行起的用例表），把每种`"type"`值复制进`internal/agent/turn/inprocess/parity_test.go`，断言`parseKind`对每个fixture都返回非空且等于fixture的type字段。

- [ ] **Step 6: Commit**

```bash
git add internal/agent/turn/
git commit -m "feat(turn): add turn contract package and in-process adapter"
```

---

## Phase 2：Processor切换到`turn.Service`（chat路径）

### Task 2.1: 替换`flow.Runner`依赖

**Files:**
- Modify: `internal/channel/inbound/channel.go`（字段169-235、流式块1114-1265）
- Modify: `internal/channel/inbound/dispatcher.go`（队列元素类型`conversation.InjectMessage`→`turn.InjectMessage`）
- Modify: `cmd/agent/app.go:624`（`provideChannelRouter`签名：注入`turn.Service`）
- Modify: 相关测试文件（构造方式更新，断言不变）

**Interfaces:**
- Consumes: `turn.Service`、`turn.StartTurnCommand`、`turn.RunHandle`（Task 1.1/1.2）。
- Produces: `NewChannelInboundProcessor(log, registry, routeResolver, messageWriter, turnSvc turn.Service, channelIdentityService, policyService, jwtSecret, tokenTTL)`——Phase 5装配按此签名。

- [ ] **Step 1: 字段与构造函数替换**

`runner flow.Runner`→`turnSvc turn.Service`；删除`flow`与`conversation`的import（编译器驱动清理残留引用，见Step 3）。

- [ ] **Step 2: 流式块改写**

原1114-1200行的collector＋injectCh＋`ChatRequest`构建＋`p.runner.StreamChat`替换为：

```go
cmd := turn.StartTurnCommand{
	SchemaVersion: 1,
	TeamID:        cfg.TeamID, // 若channel.Config尚无TeamID字段：在channel包Config上补字段，从sqlc行填充
	Mode:          turn.ModeChat,
	BotID:         identity.BotID,
	// ……其余字段与原ChatRequest构建逐一对应（1141-1191行的每个字段），
	// Attachments/ReplyAttachments/SkillActivation/RequestedSkills经convert翻译
	IdempotencyKey: sourceMessageID,
}
handle, err := p.turnSvc.StartTurn(streamCtx, cmd)
if err != nil { /* 走原streamErr错误路径 */ }
```

- 事件循环：`case e, ok := <-handle.Events():`中`mapStreamChunkToChannelEvents(conversation.StreamChunk(e.Payload))`——注意：`StreamChunk`是`json.RawMessage`别名，此处先保留conversation别名调用，Step 3统一把mapper签名改成`json.RawMessage`以摘除conversation import。
- 资产收集：原assetMu块改为`handle.AddOutboundAssets(convertAssetRefs(buildAssetRefs(ingested, ordinal)))`；ordinal计数器移到processor局部。
- 注入：dispatcher仍产队列，新增转发goroutine：`for m := range injectQueueCh { _ = handle.Inject(ctx, m) }`。
- `/stop`：`activeStreams`继续存`streamCancel`（ctx取消即终止适配器流），行为不变。

- [ ] **Step 3: 摘除conversation残留import**

```bash
grep -rn "internal/conversation" internal/channel/ --include="*.go" | grep -v _test
```
对每一处：`StreamChunk`→`json.RawMessage`、`InjectMessage`→`turn.InjectMessage`、`ChatAttachment`→`turn.Attachment`（含辅助函数签名）、`ModelMessage`（`finalMessages`）→若仅用于长度判断则改`json.RawMessage`切片或删除（目标是零残留）。

- [ ] **Step 4: 测试更新与运行**

`internal/channel/inbound/`测试：fake runner换成fake `turn.Service`（复用Phase 1的fakeRunner+adapter组合即可）。运行：

```bash
go test ./internal/channel/... ./internal/agent/turn/... && go build ./...
```
预期：全绿。

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "refactor(channel): route inbound processor through turn.Service"
```

---

## Phase 3：DiscussDriver切换

### Task 3.1: 适配器支持discuss模式

**Files:**
- Modify: `internal/agent/turn/inprocess/adapter.go`（增discuss分支）
- Create: `internal/agent/turn/inprocess/discuss.go`
- Modify: `internal/pipeline/driver.go`、`internal/pipeline/turn_response.go`
- Modify: `cmd/agent/app.go:384`（`provideDiscussDriver`签名）
- Test: `internal/pipeline/driver_test.go`（更新fake）

**Interfaces:**
- Consumes: 现driver的三个依赖搬进适配器——`pipeline.RunConfigResolver`（`ResolveRunConfig`／`InlineImageAttachments`／`StoreRound`）、`*agentpkg.Agent.Stream`、`discussRuntimeStreamer.StreamChat`。
- Produces: `inprocess.New(runner flow.Runner, opts ...Option)`增加`WithDiscuss(agent *agentpkg.Agent, resolver pipeline.RunConfigResolver)`；`DiscussDriverDeps`删除`Agent`／`Resolver`／`RuntimeStreamer`，新增`Turn turn.Service`。

- [ ] **Step 1: 迁移编排逻辑**

driver中「ResolveRunConfig→构建RunConfig→`Agent.Stream`→逐事件JSON化→StoreRound」的native分支，以及ACP分支的`RuntimeStreamer.StreamChat`调用，整体移入`inprocess/discuss.go`，作为`StartTurn`的`Mode==ModeDiscuss`分支。事件载荷：native分支`json.Marshal(agentpkg.StreamEvent)`；ACP分支chunk原样。命令字段用`SessionToken`／`ChatToken`／`ToolHTTPURL`／`ReplyTarget`／`ConversationType`／`ConversationName`（即原`DiscussSessionConfig`）。注意`pipeline.RunConfigResolver`接口若被适配器import会造成pipeline←→turn循环——先把该接口定义搬到`inprocess`包（driver侧删除），`flow.Resolver`天然满足。

- [ ] **Step 2: driver消费port**

`DiscussDriver`触发处改为`d.deps.Turn.StartTurn(ctx, cmd)`并消费`handle.Events()`；`driver.go:420`处原本就从JSON反序列化`agentpkg.StreamEvent`，改为对`e.Payload`做同样反序列化，`agentEventToChannelEvent`不动。

- [ ] **Step 3: 测试与提交**

```bash
go test ./internal/pipeline/... ./internal/agent/turn/... && go build ./...
git add -A && git commit -m "refactor(pipeline): drive discuss turns through turn.Service"
```
预期：pipeline现有测试全绿（fake streamer换成fake turn.Service）。

---

## Phase 4：`internal/channel`目录重组（已推迟，见执行记录）

> **执行记录（2026-07-17）**：实测交叉引用后确认本阶段不是纯移动——`registry.go`／`prepared_outbound.go`／`parts_render.go`被包根adapter契约引用须留根；`outbound.go`须先拆分Manager方法与adapters共用的chunking函数。按spec§4执行修正推迟到独立PR，本PR跳过此阶段。

### Task 4.1: 拆出`outbound/`再拆出`gateway/`

**Files:** 按spec§4映射表：
- `outbound/`：`outbound.go`、`outbound_prepare.go`、`prepared_outbound.go`、`parts_render.go`、`format.go`、`toolcall_filter.go`、`toolcall_format.go`、`toolcall_formatters.go`、`toolcall_summary.go`（含对应`_test.go`）
- `gateway/`：`manager.go`、`registry.go`、`connection.go`、`lifecycle.go`、`processor.go`、`observer.go`、`inbound.go`、`webhook_handler.go`、`webhook_endpoint.go`（含对应`_test.go`）
- 包根保留：spec映射表第三行全部文件

**依赖方向（必须成立，编译器验证）:** `gateway`→`outbound`→包根；包根不import子包。

- [ ] **Step 1: outbound批次**

```bash
mkdir -p internal/channel/outbound internal/channel/gateway
git mv internal/channel/{outbound.go,outbound_prepare.go,prepared_outbound.go,parts_render.go,format.go,toolcall_filter.go,toolcall_format.go,toolcall_formatters.go,toolcall_summary.go} internal/channel/outbound/
git mv internal/channel/{outbound_test.go,outbound_prepare_test.go,parts_render_test.go,format_test.go,toolcall_filter_test.go,toolcall_format_test.go,toolcall_formatters_test.go,toolcall_summary_test.go,parts_canonical_external_test.go} internal/channel/outbound/
```
包声明改`package outbound`；被`gateway`／外部调用的原unexported函数（如`buildOutboundMessagesWithCaps`）导出并在调用点改限定名。`go build ./...`驱动修完。

- [ ] **Step 2: gateway批次**

同法`git mv`gateway清单文件，`package gateway`；全仓引用更新（`channel.Manager`→`gateway.Manager`、`channel.NewStore`不动——`service.go`留根、`channel.NewWebhookServerHandler`→`gateway.NewWebhookServerHandler`等）：

```bash
grep -rln "channel\.Manager\|channel\.Registry\|channel\.NewWebhookServerHandler" --include="*.go" internal/ cmd/ | sort
```
逐文件更新import与限定名。不引入类型别名（一步到位，避免过渡态）。

- [ ] **Step 3: 全量验证与提交**

```bash
go build ./... && go test ./internal/channel/... ./internal/pipeline/... ./internal/handlers/...
git add -A && git commit -m "refactor(channel): split package root into gateway and outbound"
```

---

## Phase 5：命令装配模块

### Task 5.1: `cmd/internal/core`与`cmd/internal/channel`

**Files:**
- Create: `cmd/internal/core/module.go`（`package core`，`func Module() fx.Option`）
- Create: `cmd/internal/channel/module.go`（`package channel`，`func Module() fx.Option`）
- Modify: `cmd/agent/app.go`、`cmd/agent/module.go`（providers搬家后瘦身）

**搬家清单:**
- `channelmodule.Module()`：`provideChannelRegistry`、`provideCommandHandler`、`provideChannelRouter`、`provideChannelManager`、`provideChannelLifecycleService`、`providePipeline`、`provideEventStore`、`provideDiscussDriver`、`local.NewRouteHub`、`gateway.NewStore`、email全组（`provideEmailRegistry`等7个）、`webhooktunnel.NewManager`；Invoke：`startChannelManager`、`startEmailManager`、`startWebhookTunnelListener`、`startWebhookTunnel`。
- `coremodule.Module()`：`cmd/agent/app.go`其余非`provideServerHandler`、非server的providers（config、logger、db、workspace、agent、flow、schedule等）＋对应Invoke（`startScheduleService`等）＋新增`provideTurnService`（`inprocess.New(resolver, WithDiscuss(agent, resolver))`绑定`turn.Service`）。
- `cmd/agent/module.go`最终形态：`fx.Options(coremodule.Module(), channelmodule.Module(), fx.Provide(全部provideServerHandler…, provideServer), fx.Invoke(startServer, injectToolProviders, …HTTP相关))`。

- [ ] **Step 1: 机械搬函数**（函数体原样移动、改导出、更新package与import；`provideConfig`等cmd专属flag解析留cmd，以参数注入）

- [ ] **Step 2: 验证all-in-one不变**

```bash
go build ./... && go test ./...
go run ./cmd/agent --help   # 预期：正常输出，无fx装配错误
```

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "refactor(cmd): extract shared core and channel modules"
```

---

## Phase 6：`cmd/channel`验证二进制

### Task 6.1: main入口

**Files:**
- Create: `cmd/channel/main.go`

- [ ] **Step 1: 装配**

```go
package main

import (
	"go.uber.org/fx"

	channelmodule "github.com/felinics/memoh/cmd/internal/channel"
	coremodule "github.com/felinics/memoh/cmd/internal/core"
)

// cmd/channel is a single-instance assembly-closure verification binary
// (spec §7.3): functionally an all-in-one without the REST API until a
// cross-process turn transport exists. Now a deployment artifact in split mode (docker compose); embedded mode in cmd/agent covers single-process deployments.
func main() {
	fx.New(
		coremodule.Module(),
		channelmodule.Module(),
		// 最小Echo：仅注册channel webhook与weixin QR两个handler（从cmd/agent复用provider）
	).Run()
}
```

- [ ] **Step 2: 构建验证与CI**

```bash
go build ./cmd/channel && go vet ./cmd/channel
```
在CI工作流（`.github/workflows/`中现有Go build job）加`go build ./cmd/...`（若已是`./...`则无需改动，确认即可）。

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat(cmd): add channel single-instance verification binary"
```

---

## Phase 7：验证收尾（用户要求）

### Task 7.1: 全量验证

- [ ] **Step 1: lint与全量测试**

```bash
mise exec go@1.25.7 -- golangci-lint run ./... 2>&1 | tail -20
go test ./... 2>&1 | tail -20
```
预期：无新增violation；全绿。

- [ ] **Step 2: 重启开发环境**

```bash
mise run dev:restart -- server   # 若失败改用 mise run dev
mise run dev:logs 2>&1 | tail -30
```
预期：server启动无fx错误。

- [ ] **Step 3: API冒烟**

```bash
curl -sf http://localhost:18080/api/ping
# 用devenv/app.dev.toml中的admin凭据登录
curl -sf -X POST http://localhost:18080/api/auth/login -H 'Content-Type: application/json' -d '{"username":"<admin>","password":"<pass>"}'
# 带token列bots与channels，验证核心链路
curl -sf http://localhost:18080/api/bots -H "Authorization: Bearer $TOKEN"
```
预期：ping通、登录返回token、bots列表200。若有可用bot，再通过Web Chat SSE或消息API发一条消息验证Turn链路（chat经turn.Service）。

- [ ] **Step 4: 开PR**

```bash
git push -u origin feat/channel-boundary
gh pr create --title "refactor: extract channel boundary behind turn.Service contract" --body "<按spec§2范围逐条对照的总结＋验证记录>

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

## Self-Review记录

- spec覆盖：§5契约→Phase 1；§5.4切换→Phase 2/3；§4目录→Phase 4；§7装配→Phase 5/6；§10验证→Phase 7。§3数据所有权无代码变更（维持现状），§6 pipeline归属由Phase 3实现。
- 已知偏差（相对spec，均有依据）：`Service.Inject`收敛为`RunHandle.Inject`（进程内无需按RunID路由，跨进程spec恢复）；`RunHandle`增加`Errs()`与`AddOutboundAssets()`（忠实桥接现有双通道与资产收集语义）；五个PR改为单分支多提交一个PR（用户指示）。
- 类型一致性：`turn.Service`签名在Task 1.1定义，2.1/3.1/5.1按同名消费；`NewChannelInboundProcessor`新签名在2.1声明、5.1装配引用。
