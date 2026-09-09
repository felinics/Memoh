# Twilight SDK 的 reasoning 抽象:设计决策

> 状态:**提案,等团队讨论**。同意了才动手。
> 关联文档:[能力元数据架构提案](./2026-08-10-reasoning-capability-architecture-proposal.md)(为什么)、[收敛工程工作文档](./2026-08-10-reasoning-consolidation-workplan.md)(Memoh 侧怎么做)。本文档管 **Twilight SDK 侧**的 API 设计。
> 依据:三份独立调研(官方 SDK 源码实测、产品 UX 实测、Go 类型设计论证),2026-08-10。

## TL;DR

Gemini 的 thinking 完全不可控——不是模型不支持,也不是数据缺失,是**我们自己的 Google provider 请求侧没接**。修它顺带触发了一个更大的问题:SDK 的 reasoning 抽象该长什么样。

调研结论有点反直觉:**不要在通用层加 budget 字段,不要做客户端校验,不要抽象跨 provider 的统一 reasoning 模型**。官方 SDK 一致的做法是"通用层薄 + provider 私有层忠实镜像 + 误用交给服务端 400"。我们现状已经天然接近这个模式,需要的是补齐 Google、把 Anthropic 的扁平 struct 改成 union、删掉一个自作聪明的归一化函数。

用户可见的控件**保持纯档位**,不暴露 token 预算——业界已经收敛,而且 budget 正在从各家 API 里消失。

---

## 一、问题从哪来

排查"为什么 Gemini 的 reasoning 控件是假的"时,逐层验证的结果:

| 层 | 状况 |
|---|---|
| Gemini 官方 | **支持**。2.5 世代 `thinkingConfig.thinkingBudget`(预算制),3.x 世代 `thinkingConfig.thinkingLevel`(档位制) |
| models.dev 数据 | **齐全**。3.5-flash 给 `effort:[minimal,low,medium,high]`,2.5-pro 给 `budget_tokens{128,32768}`,连两代机制不同都表达了 |
| **我们的 Twilight Google provider** | **断在这里**。`generationConfig` 结构体没有 `thinkingConfig` 字段,`buildRequest` 不读 `params.ReasoningEffort`,也没有 `WithThinking` 这类 Option |

所以 picker 里 Gemini 的三档从第一天起就是安慰剂:选了什么都不会发出去。

### 现状抽象:两层,但不齐

Twilight 的 reasoning 分两层:

1. **通用请求层**:`sdk.GenerateParams.ReasoningEffort *string` + `sdk.WithReasoningEffort(string)`。表达力 = "一个档位字符串或 nil"。**没有 ProviderOptions 之类的逃生舱。**
2. **provider 私有构造层**:各 provider 自己的 Option,如 `provider/anthropic/messages.WithThinking(ThinkingConfig{Type, BudgetTokens})`,构造 Model 时注入。

Anthropic 之所以能工作,是因为它两层都用:budget/adaptive 这种私有形状走第二层(Memoh 在 `NewSDKChatModel` 里按世代注入),档位走第一层。OpenAI 只用第一层。**Google 一层都没接**——这才是它断掉的确切原因,不是抽象逼它做了什么错事。

---

## 二、调研发现(三份互相印证也互相修正)

### 2.1 官方 SDK 的一致惯例

实测六个 SDK 的源码(不是文档转述),结论出乎意料地一致:

| SDK | 新老机制关系 | 冲突处理 | model id 嗅探 | effort→budget 表 |
|---|---|---|---|---|
| **Anthropic**(py/ts/go) | **一个字段多态**(3 元 union) | 类型系统即排他,无法同时设 | 无 | 无 |
| **OpenAI**(py/node/go) | 单一 effort 字段(开放 enum) | 无冲突可能 | 无 | 无 |
| **Google GenAI** | 两个并列字段(同一 struct) | **完全透传,零校验** | 无 | 无 |
| **Vercel AI SDK** | provider 私有选项 | 各 provider 内部 union | 无 | 无 |
| LangChain(第三方) | 两个并列 + 客户端校验 | **抛 ValueError** | **有(`startswith`)** | 无 |
| LiteLLM(网关) | 翻译层 | 后者覆盖 + 自动降级 | **有(能力表)** | **有硬编码** |

三条最关键的:

**① 官方 SDK 一律不做客户端语义校验,不做 model 嗅探,纯透传。** Anthropic Go SDK 全线零校验(`BudgetTokens` 只出现在 struct 定义、构造函数和 getter 里,没有一个 `if`);Google 连"budget 和 level 同时设"这种明显冲突都不查。做嗅探的两个都是第三方封装,且都因此背上持续维护模型知识库的负担。

**② 新老机制并存时,官方首选 union,不选并列字段。** Anthropic 面对的问题和我们**完全一样**(老世代 budget、新世代 adaptive、4.7+ 拒绝 budget),它的选择是 3 元 discriminated union,关键在于 `ThinkingConfigAdaptiveParam` 里**根本没有 `budget_tokens` 字段**——非法组合在类型层不可表达,于是不需要任何冲突处理逻辑。Go SDK 用指针 + `MarshalUnion` 实现:

```go
// anthropic-sdk-go/message.go:6637
// Only one field can be non-zero.
type ThinkingConfigParamUnion struct {
	OfEnabled  *ThinkingConfigEnabledParam  `json:",omitzero,inline"`
	OfDisabled *ThinkingConfigDisabledParam `json:",omitzero,inline"`
	OfAdaptive *ThinkingConfigAdaptiveParam `json:",omitzero,inline"`
	paramUnion
}
```

唯一的"并列字段"先例是 Google,而那是被上游 protobuf 形状绑住的历史包袱(`thinking_budget` 和 `thinking_level` 是同一个扁平 struct 上两个 Optional 字段),它的应对方式是**干脆不管**。

**③ 跨 provider 抽象层倾向于不抽象 reasoning。** Vercel AI SDK 通用层的 reasoning 参数数量是 **0**——`LanguageModelV3CallOptions` 里只有 maxOutputTokens/temperature/topP/topK,全部 thinking 下沉到 `providerOptions`,并且**忠实镜像各家原生形状**:Anthropic 是 union 就做 union,Google 是两字段并列就两字段并列。

**④ 不加 deprecation 标记。** Anthropic 官方在 `budget_tokens` 已被 4.7+ 移除的情况下,源码里**仍未加任何 deprecation 标记**——因为老模型上它依然完全合法,加标记会误伤。世代差异写在 doc comment 里(Vercel 的做法:`/** for models before Opus 4.6 */`)。

### 2.2 产品 UX:档位制已是 API 层现实

调研了 Claude Code、Cursor、Windsurf、Copilot Chat、Cherry Studio、LobeChat、Open WebUI、Cline、Roo Code、三家第一方 playground。

**最锋利的一句:我们正在为一个正在消失的参数形态考虑是否要暴露它。**

- `budget_tokens` 在 Claude 4.7+ 是 400 错误
- `thinkingBudget` 在 Gemini 3 上"仅为向后兼容而接受",和 `thinkingLevel` 同时发会 400
- OpenAI 从未有过数字预算

**用户反馈方向和我们的担心相反。** 所有仓库里检索的结论:**没有任何一个 issue 要求"我想精确控制 budget 数字"**。抱怨全是三类——档位不够用、档位藏得太深、档位不透明导致失控成本。

反过来,**数字预算被证实是 bug 源头**:Cline #4503(Gemini 上设的 52k budget,切到 Bedrock Claude 时数字被带过去了)、#7260(1024 硬下限触发 Gemini 免费额度 429)、#7735/#7957(给 2.5 Pro 发 0、给 gemini-3 同时发两个字段)、Open WebUI 一串自由文本输入导致 500。

**最强的演进证据:Cline 把数字滑块删了。** PR #12716(2026-07-30)`deleted file mode ThinkingBudgetSlider.tsx`,-175 行,改成纯档位下拉。它前一个 PR #12542 的动机陈述几乎是对我们现状的逐字描述:

> "Previously, Cline reduced model reasoning support to a Boolean and reconstructed provider behavior using duplicated effort types, regexes, version lists, and hard-coded model matrices. That could collapse max/xhigh, drop native OpenAI effort, combine unsupported Gemini controls, choose the wrong Anthropic thinking mode."

(顺带:那个老滑块是 `step={1}`,一个 token 一格——这本身就是"暴露数字"的实现陷阱。)

**混合模式确实存在,但形态是"档位在主界面 + 数字在设置层",不是并排。** 三个范本按推荐度:Copilot(档位进 model picker、数字降级为 settings.json 键、adaptive 模型上数字直接忽略)、Vertex AI Studio(下拉默认 Auto → 选 Manual 才展开 slider,渐进披露)、Claude Code(UI 纯档位,数字仅存于 `MAX_THINKING_TOKENS` 环境变量逃生阀)。

**不推荐 LobeChat 的并排 slider+InputNumber**:代价是同目录约 25 个按模型族定制的控件 + 18 套档位词表,对多 provider 平台是维护灾难。

**三家第一方都刻意拒绝公布档位→token 映射**,两家在文档里明说档位"是相对配额而非严格的 token 保证"。任何第三方映射都是自己发明的。

### 2.3 被修正的两个前提(记录下来,避免重犯)

调研过程推翻了我在讨论中提出的两个判断,值得留档:

**① "可序列化性是 SDK 的硬约束"——错。** 全仓搜索确认 `sdk.GenerateParams` **从未被 Marshal**,那些 json tag 是遗留装饰(结构体里 `Model *Model` 持有 `Provider` interface 含 `*http.Client`、`ToolChoice any`,本来就不可能往返)。真正需要序列化的是 **Memoh 自己的 `native.RunConfig`**(要过 server↔channel 的 gRPC),那是我们的 DTO 约束。

推论:`service.go:751-755` 把 `ReasoningConfig` 拆成 5 个扁平字段穿过 RunConfig 是**过度反应**——直接放一个带 json tag 的 `*ReasoningConfig` 就行。这顺手解释了普查发现的 B1(spawn 路径只搬 `ReasoningEffort`、丢掉另外 4 个字段):**字段拆得越散,漏传的机会越多。**

**② "那张 16000 的表该搬进 SDK"——不采纳。** 设计论证提议把 effort→budget 表搬进 SDK(带 `TierBudgets` 覆盖点),理由是"legacy Claude 只吃 budget"属于 wire 知识不该外泄。但实测显示:**官方 SDK 无一有换算表**;唯一有的 LiteLLM 是**网关角色**——它必须把一种输入形状翻译成 N 家形状。而在我们的架构里,**Memoh 才是那个网关角色**,Twilight 是 provider SDK。所以表留在 Memoh 是对的,只是形态要改(见 §3.7)。

---

## 三、设计决策

### SDK 侧(Twilight)

**3.1 通用层 `ReasoningEffort *string` 不动,不加 budget 字段。**

理由:Vercel 通用层 reasoning 参数为 0,我们已天然接近这个模式;`*string` 而非自定义 enum 也是对的——对齐 OpenAI 的开放 enum 策略(它的 Go SDK 就是 `type ReasoningEffort string` + 具名常量,注释明说 "Not all reasoning models support every value"),服务端先发新档位时我们不用改版本。

可以补一组具名常量方便调用方,但**不在 SDK 里校验取值合法性**。

**3.2 Anthropic 的 `ThinkingConfig` 从扁平 struct 改成 union。**

现状(`provider/anthropic/messages/messages.go:36`):

```go
type ThinkingConfig struct {
	Type         string // "enabled", "adaptive", or "disabled"
	BudgetTokens int    // required when Type is "enabled"
}
```

这允许 `{Type:"adaptive", BudgetTokens:8000}` 被构造出来,只能靠服务端拦。照官方形状改造后,调用方拿不到非法组合:

```go
func WithThinkingEnabled(budgetTokens int) Option  // 老世代(<=4.5)
func WithThinkingAdaptive() Option                  // 4.6+
func WithThinkingDisabled() Option
```

**3.3 Google provider 补 thinking,如实镜像上游两字段并列。**

```go
type thinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`   // 2.5 世代
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`    // 3.x 世代
	IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
}
```

挂到 `generationConfig` 上,`buildRequest` 消费。**不校验两者互斥**——Google 官方和 Vercel 都是这么做的,忠实镜像的可预测性高于自创归一化。

**这里可以超越官方一点**:google-genai 三语言都把 `-1`/`0` 留成裸 magic number(只写在字段 description 里),我们给具名常量:

```go
const (
	ThinkingBudgetDynamic  = -1 // AUTOMATIC
	ThinkingBudgetDisabled = 0  // DISABLED,仅 Flash/Lite 合法
)
```

仍然透传、仍不校验,只是让调用点可读。

**3.4 删掉 `NormalizeReasoningEffort`。**

`provider/openai/reasoning.go` 那个 max→xhigh 的映射,注释自称 defence-in-depth,实际是 defence-against-the-future:它基于 SDK 对"OpenAI 会接受什么"的**猜测**,哪天 OpenAI 支持了 `max`,它会静默降级用户请求且没人记得它存在。官方 SDK 无一有 tier 白名单。

判据(建议写进 Twilight 的贡献文档):**SDK 可以执行调用方的声明,不可以执行自己的猜测。**

它是 exported 函数,但 Memoh 侧没有直接调用(我们有自己的 `openAIWireEffort`),实际影响为零。建议先留空壳一个版本再删。

**3.5 不做 model id 嗅探。**

三份调研一致。做了的两个——LangChain 的 `startswith("claude-opus-5")`、LiteLLM 的能力表——都是第三方封装,都因此要持续维护模型知识库。**这条要写进 Twilight 的 AGENTS.md。**

**3.6 不加 deprecation 标记,世代差异写 doc comment。**

理由见 §2.1 ④。真要标记,等目标模型全部退役再说。

**3.7 唯一需要我们自己定的冲突点:通用 `ReasoningEffort` 与 provider 私有 thinking 同时设置时谁胜。**

建议 **provider 私有选项胜出**(表达力更强、意图更明确),**并写进 doc comment**——LangChain 靠 Pydantic alias 优先级隐式决定、只在 docstring 里补说明,是可避免的坑。

### Memoh 侧

**3.8 换算表留在 Memoh,但从硬编码改成比例式。**

这是三份调研里唯一**两个独立项目收敛出同一答案**的点:

```go
// Cline: sdk/packages/shared/src/llms/reasoning-effort.ts
ratio = {max:1, xhigh:0.95, high:0.8, medium:0.5, low:0.2, minimal:0.1, none:0}

// Cherry Studio: src/shared/ai/reasoning.ts —— 注意是 min↔max 的 lerp,不是 max × ratio
budget = max(1024, floor((limit.max - limit.min) * ratio + limit.min))
```

我们现状(`internal/models/sdk.go:271`)是硬编码 `low:5000 / medium:16000 / high:50000`。改成比例式的收益:

- **不需要为每个模型维护一行**,自动落在各模型的合法区间内
- **限制表必须按模型族分,不能扁平**:Gemini 是 `pro={128,32768}` / `flash={0,24576}` / `flash-lite={512,24576}`;Claude 各代 max_tokens 也不同(Sonnet 4.5 = 64000、3.7 = 32000)。理想是从 models.dev 拉,而非自己维护矩阵
- 顺带解释了一个 wire 事实:**2.5 Pro 下限是 128 所以关不掉,Flash 下限是 0 所以能关**——这正好对应 catalog 里 `reasoning_efforts` 该不该含 `disable`

⚠️ 一个需要澄清的点:现有的 `50000` 在 **Anthropic 路径上不越界**——`resolveMaxTokens` 会把 budget 加到 max_tokens 上(`4096 + 50000`)。它只在"把这张表用到 Gemini 2.5(上限 32768)"时才越界,而我们目前没有这条路径。所以这不是现行 bug,是**换比例式要防的坑**。

**3.9 档位保持为唯一用户可见控件,不暴露 budget。**

多 provider + agent 是档位制收益最大的象限:agent 场景下 thinking 只是总消耗的一部分(tool call 也吃 token),而 `effort` 影响**全部输出**、`budget_tokens` 只影响 thinking——**档位在 agent 场景下语义更正确**。

**3.10 加 `auto` 档位,并按模型能力隐藏不支持的档位。**

- `auto`:Gemini 发 `-1`,Claude 发 adaptive
- `off`:Gemini 发 `0`(仅 Flash/Lite)、Claude 省略 thinking、OpenAI 发 `none`
- 区分"省略参数"和"发送 none 值"(参考 Roo Code 的 `disable` vs `none`,源于它的 issue #8624)
- **2.5 Pro 不能关 thinking,UI 就不该给 off**;切模型时把不支持的档位 fallback 到最近可用档(Claude Code 的做法),而不是报错

**3.11 对失控成本做防御。**

这是纯档位制**唯一的真实缺陷**:claude-code#64153——"Opus 4.8 **medium** effort 在一个简单编码轮次上花掉 46,433 output tokens",UI 显示思考 22 分 43 秒。但解法不是暴露 budget:

- 事后显示实际 thinking token 消耗(Anthropic 给了 `usage.output_tokens_details.thinking_tokens`)
- 用独立的输出/成本上限兜底,而不是靠 thinking budget 控成本
- 参考 Claude Code:让最高档**只在当前会话有效、不持久化**(防止用户忘记关掉烧钱)

---

## 四、还成立但需要单独处理的:生命周期错配

设计论证里有个发现独立于上面所有决策,而且**在 union 方案下依然存在**:

Anthropic 的 thinking 形状被钉在 **provider 构造期**(`WithThinking` 是 Model Option,构造一次),而档位在**请求期**(每次调用)。于是同一个逻辑决策("用户选了 high")被劈成两半:budget 在 `NewSDKChatModel`,effort 在 `BuildReasoningOptions`。这就是 `SDKModelConfig.ReasoningConfig` 要被消费两次的根源。

**建议:把 provider thinking 选项从构造期移到请求期**,作为独立工作项。它不阻塞 Gemini 修复,但会让 Memoh 侧的 reasoning 决策收敛成真正的单点(和收敛工程工作文档的目标一致)。

---

## 五、落地顺序

每步独立可发布,**第 1 步就交付了最痛的问题**:

| 步 | 内容 | 仓库 | Breaking |
|---|---|---|---|
| 1 | Google provider 补 `thinkingConfig` 字段 + Option + `buildRequest` 消费;`-1`/`0` 具名常量 | Twilight | 否(纯新增) |
| 2 | Memoh 侧填 Gemini 的方言声明,`BuildReasoningOptions` 接上 Google 分支 | Memoh | 否 → **Gemini 端到端打通** |
| 3 | Anthropic `ThinkingConfig` 改 union;`NormalizeReasoningEffort` 留空壳标废弃 | Twilight | 否 |
| 4 | 换算表改比例式 + 限制表按模型族分(理想:从 models.dev 拉) | Memoh | 否 |
| 5 | `auto` 档位 + 按能力隐藏档位 + 失控成本可观测性 | Memoh | UI 可见变化 |
| 6 | thinking 选项从构造期移到请求期(§4) | 两边 | 需协调版本 |
| 7 | 删 `NormalizeReasoningEffort` 空壳 | Twilight | 是(exported,但无调用方) |

第 4/5 步与 Memoh 侧收敛工程(工作文档的 PR A/B)有重叠,实施时合并进那两个 PR 更省事。

## 六、开放问题

1. **Gemini 修复的排期**:第 1-2 步能独立交付,是否插队到收敛工程之前?(Gemini 用户现在的控件是假的,但这个假控件已经存在很久了)
2. **比例值取谁的**:Cline 的 `{high:0.8, medium:0.5, low:0.2}` 还是 Cherry 的 lerp 公式?两者对 Gemini 2.5 Pro(128–32768)算出的结果不同。建议 Cherry 的 lerp(尊重下限)+ Cline 的比例值。
3. **`auto` 档位的 UI 位置**:是和 low/medium/high 并列,还是像 Vertex AI Studio 那样作为默认值单独呈现?
4. **Twilight 的 AGENTS.md 是否要新增"禁止 model id 嗅探"条款**?建议要,并附本文档链接。

## 七、明确不做

- 不在通用层加 budget 字段(§3.1)
- 不做客户端语义校验(方言不匹配、tier 白名单、budget 越界都交给 provider 400)
- 不做 model id 嗅探(§3.5)
- 不给用户暴露 token 预算输入(§3.9)
- 不抽象跨 provider 的统一 reasoning 模型(Vercel 验证过:忠实镜像比自创归一更可预测)
