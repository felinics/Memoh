# Reasoning 能力元数据:别再打补丁了,聊聊怎么根治

> 状态:**提案,等团队讨论**。同意了才动手。
> 起因:review PR #966 时发现的结构性问题。相关上下文:#563、#926、#966。
> 日期:2026-08-10

## TL;DR

"这个模型的 thinking 能不能关、有哪些档位"这个问题,我们现在有**三个地方各答各的**(Web picker、`/reasoning` 命令、后端 resolver),而唯一真正知道答案的 adaptor 层没有被任何一方问过。再加上"能否关闭"被塞在 `reasoning_efforts` 字符串列表里当哨兵值,导致每次改动都在漏水——#966 一个 PR 里就自己堵了三次洞,而且现在 DeepSeek/MiniMax 还漏着。

提案分三步:**① 后端算好下发,前端只渲染;② catalog schema 把"能力"和"档位"拆开;③ 数据源主力换成 models.dev,LiteLLM 降级为价格源。** 三步独立,可以分开做,第一步收益最大。

---

## 一、起因:#966 是怎么把我们带到这里的

### 这条 PR 链的故事

先快速回放一下历史,不然看不懂问题在哪:

- **#563** 引入了 capability 驱动的 thinking 元数据:`thinking_mode` + `reasoning_efforts`,数据从 LiteLLM registry 同步(synccaps)。当时 picker 的"关闭"选项(`disable`)是无条件排在最前面的,同时 `none` 是模型可声明的档位。
- **#926** 干掉了 `reasoning_enabled` 布尔,让 `reasoning_effort` 一个字段同时承担开关和档位:存 `disable` 就是关,存别的就是档位。
- **#966**(还没合)发现 `disable` 和 `none` 是同一个状态的两个名字,俩都出现在 picker 里,于是统一成 `disable`。

#966 的方向没问题,但看它自己的 commit 演化,味道就不对了:

1. **Commit 1**:统一 token,`disable` 变成可声明的。
2. **Commit 2**:糟糕,`disable` 进了 effort 列表之后,`pickEffort` 直读 stored setting 会把它当成活跃档位发上 wire(没有任何厂商认识 "disable" 这个词)→ 加守卫。
3. **Commit 3**:review 指出存量数据和没再生成的 registry 还在说 `none` → 在 ModelConfig 读写两侧加 normalize,Go 和 TS 各写一份。
4. **Commit 4**:Copilot review 指出 Claude 全系的 Off 没了(Anthropic 的 wire 上"关"是靠省略字段表达的,catalog 永远不会声明 `disable`)→ 前端硬编码一个 `IMPLICIT_OFF_CLIENT_TYPES = {'anthropic-messages'}` 名单救回来;顺带还得把 `disable` 从 `anthropicEffortEra` 的判断信号里摘出去,因为往列表里塞 token 干扰了一个不相干的世代推断。

一个 PR,四个 commit,后三个都在堵前面开的洞。这不是作者水平问题——是**每个补丁都逻辑自洽,但补丁越打越说明位置不对**。

### 我们还能立刻找到下一个洞

Commit 4 修了 Claude,但**同型的问题在 DeepSeek 上还在**(一度以为 MiniMax 也是,后来发现更有意思,见下):

- DeepSeek 的 YAML 是 `client_type: openai-completions` + `thinking_mode: toggle`,`reasoning_efforts` 里没有 `disable`;
- 后端 `sdk.go` 的 compat 分支**明确支持关闭**——`rc.Disabled` 时发 `reasoning_effort: "none"`,SDK compat 层翻译成 thinking-off;
- 但前端 `canTurnOff` 一看:没有 `disable` token,client type 也不在 Anthropic 名单里 → **Web 端 Off 被藏掉了**。而 `/reasoning off` 命令照常提供,而且真的生效。

(→ 已修:本 PR 给 deepseek-v4-flash/pro 的 YAML 补了 `disable` token,synccaps 改为保留手维护的 disable 不被 re-sync 冲掉。)

**MiniMax 则是一个反转,而且反转本身更能说明问题**:我们本以为它和 DeepSeek 一样漏了 Off(后端 compat 同样支持翻译),但交叉核对 models.dev(`reasoning_options = []`,always-on)和 OpenRouter(`mandatory: true`)后发现,**M2.x 全系根本关不掉**——反倒是我们 YAML 里 M2.5/M2.1-Lightning 标着 `thinking_mode: toggle` + 三档 efforts 这份数据本身可疑。也就是说,同一份残缺元数据,在 DeepSeek 上表现为"藏了一个能用的控件",在 MiniMax 上表现为"差点给一个假控件正名"。单源数据两头都能骗到你,这正是后文"多源交叉校验"的现实论据。MiniMax 的数据修正留给后续(需要探针或官方文档确认 Lightning 变体的真实行为)。

这不是碰巧漏掉,是结构性必然:"能不能关"的真相只存在于 adaptor 层(Anthropic 靠省略、DeepSeek/MiniMax 靠 compat 翻译、Google 目前压根什么都不发),但决策却让前端拿 `(mode, levels, clientType)` 这个残缺投影去猜。前端连 `chat_completions_compat` 都看不到,它不可能答对。

顺带一提 Gemini 是同一问题的另一面:picker 提供 low/medium/high,但 `BuildReasoningOptions` 对 Google 永远返回 nil——选了也白选。这坑 #966 之前就有。

### 病根到底是什么

两句话:

**1. 能力布尔值伪装成了 tier 哨兵。**"这个模型能被关掉"是个 capability boolean,却被塞进 `reasoning_efforts` 字符串列表里当成员。于是每个读这个列表的人都得记得把它滤掉:`orderedReasoningEfforts` 排除它、`NearestEffortToMedium` 忽略它、`KNOWN_EFFORTS` 不含它、`pickEffort` 加守卫、`availableEffortsForMode` 拆开重排、`anthropicEffortEra` 还得专门声明"这个 token 不算信号"。一个列表被四种消费者读出四种含义,commit 2 和 commit 4 的洞都是这个重载的直接产物。

有人可能问:那把 bool 反过来存("不能关"名单)行不行?不行,这条链已经把两个极性都试过了:#966 之前是"默认都能关"(gpt-5.0 关不掉也摆着 Off,点了没效果);#966 之后是"默认都不能关"(Claude 全系立刻丢 Off,得打名单补丁)。**两个全局默认值各错一半**,因为"能否关闭"根本不是一个能有统一默认的逐模型事实,它按 provider 家族分叉——这只能由知道家族语义的那一层来回答。

**2. "什么可选"这个策略有三份独立实现。** Web picker(TS 推导)、`/reasoning` 命令(硬编码 off+low~xhigh,完全不看模型)、后端 resolver(Go),三份逻辑,7 处 "keep in sync" 注释横跨两种语言。每处都是一条会断的手动同步链,Commit 4 的 `IMPLICIT_OFF_CLIENT_TYPES` 本质上是把 adaptor 知识手抄一份到前端——抄漏了 DeepSeek/MiniMax 就是证明。

### LiteLLM 也帮不了我们

有人会想:是不是 LiteLLM 数据再全一点就好了?我们实测拉了 registry(2988 条)做了普查:

- 整个 registry 里跟"关闭"沾边的字段只有一个 `supports_none_reasoning_effort`,82 条,**全部是 OpenAI wire 的模型**。173 条 Claude 里零个。
- 这很合理:这个 flag 描述的是"模型接受 `reasoning_effort: 'none'` 这个枚举值"这个 **OpenAI wire 专属事实**,不是"可关闭"这个抽象能力。Anthropic wire 上不存在这个值,LiteLLM 永远不会给 Claude 标它。
- 翻了近期 PR/issue,没有修这个缺口的计划,反而有 issue 抱怨它把 DeepSeek 的映射搞错了。

所以 `discover.go` 里 `supports_none_reasoning_effort → disable token` 这条映射,是把一个单一 wire 的事实当成了中立能力写进 catalog。对 OpenAI 系恰好成立,对其他家族系统性缺失。

---

## 二、经过:我们调研了业界怎么解决这个问题

派了两路调查:一路实测各候选数据源,一路翻同类开源项目的源码。结论出奇地一致。

### 数据源:models.dev 已经解决了语义表达问题

**models.dev**(sst/models.dev,MIT,6.3k stars,小时级自动 sync):182 个 provider / 6,247 个模型,我们要的 provider 全命中(包括 LiteLLM 没有的 GitHub Copilot、MiniMax 国内外双站)。关键是它的 `reasoning_options` 结构,业界已经收敛到这个语义模型:

```toml
# DeepSeek V4 Pro 的真实数据
[[reasoning_options]]
type = "toggle"            # 有独立开关,可关闭
[[reasoning_options]]
type = "effort"
values = ["high", "max"]   # 档位枚举
```

三种 option 类型,可关闭性有精确三态:

- `effort` 的 values 含 `none` → 用 effort=none 关(OpenAI 系);
- 独立 `toggle` 条目 → 有单独的 on/off wire 字段(DeepSeek/MiniMax/旧 Claude);
- `reasoning_options = []` → 会推理但调用方不可控(deepseek-reasoner 这种 always-on)。

而且有成熟的编辑规范和贡献者审核流程约束数据质量。

**OpenRouter `/api/v1/models`** 是可关闭性最干净的真值源:2026 年新增的顶层 `reasoning` 对象带 `mandatory`(true = 拒绝关闭)和 `default_enabled`——"能否关" = `!mandatory`,一个字段答完。免鉴权实时 API,适合交叉校验,但没有 Azure/Copilot 条目,当不了主源。

**Anthropic 官方 `/v1/models`** 2026 版直接返回 `capabilities.thinking.types.{enabled,adaptive}.supported`——这是 adaptive 世代的权威源。

**LiteLLM** 的价格粒度是全场最全的(cache 分时、分层计价、Azure/Bedrock 区域条目)——但查了 synccaps 的实际消费面,我们一个价格字段都没读过,只用能力 flag 和 `max_input_tokens`。这两样 models.dev 都有,所以 LiteLLM 可以直接退役(详见第三步)。

### 同类项目:头部玩家全部走向"后端解析、前端渲染"

看了 OpenCode、Cherry Studio、LobeChat、Open WebUI、aider、Cline、Roo Code、Vercel AI SDK。模式非常清晰地分成两组:

**做对的**:

- **Cline**(和我们架构最同构的参照):同样 build-time 从上游 JSON 生成目录,源是 models.dev,schema 原样采纳 `reasoning_options`。可关闭性派生就一行:`supportsOff = toggle ∨ effort.has("none")`。**不需要任何前端 client-type 名单。** 数据缺失时的 Claude 世代 fallback 也有成熟规则:"目录里有 effort control ⇒ adaptive 世代;未知 Claude id 默认按最新世代"(向前兼容,新模型发布当天不至于坏)。
- **OpenCode**:把 reasoning_options 编译成 `variants` map,UI 只循环 key,对 reasoning 语义零知识。
- **Cherry Studio**:main 进程投影出 `selectableEfforts` 给 renderer,文档明文写着 "UI helpers do not inspect model IDs"。Anthropic 世代方言显式建模为 `wireDialect` 字段。

**做错的(也就是我们现在的样子)**:

- **aider**:LiteLLM(价格)+ 804 条手工 YAML(行为)+ 60 行正则兜底,Anthropic 4.7 的 adaptive 问题靠 `"4.7" not in model` 这种**硬编码负名单**处理——和我们的 `IMPLICIT_OFF_CLIENT_TYPES` 如出一辙。LiteLLM 的 registry 缺口在它那边也是长期 issue 源。
- **Roo Code**:集中式手工布尔字段(`supportsReasoningBudget/Binary/Effort`),表达力不足后被迫扩成数组,per-model-id 修正补丁堆积。
- **LobeChat**:每个厂商每一代模型加一个专属枚举值 + 专属控件组件(`gpt5_1ReasoningEffort`、`opus47Effort`……约 40 个),维护成本全场最高。

对照很残酷:**我们现在走的路,尽头就是 aider 的 804 条 YAML 和 LobeChat 的 40 个控件组件。** 而正确的路已经有三个项目验证过了。

### 两条路走到最后,账单长什么样

上面的定性判断值得展开成可以横向比的账目。先说清楚:两条路的分界不是代码质量,是**知识存放的形态**——补丁路线把"哪个模型有什么怪癖"存成一条条例外(正则、名单、专属枚举),抽象路线把它存成一个语义 schema 加一层派生。例外之间互相不复用,所以边际成本恒定甚至递增;schema 摊销一次,新模型只是多一行数据。

**补丁路线的实测账单**(都是当前 main 分支数出来的,不是估算):

| 项目 | 存量 | 新出一代模型要干什么 | 症状案例 |
|---|---|---|---|
| aider | 804 条手工 YAML + 60+ 行子串匹配链 | 加 YAML 条目;有 wire 怪癖就改通用正则 | Claude 4.7 不收 budget → 硬编码 `"4.7" not in model`,openrouter/ 前缀还得再抄一遍 |
| LobeChat | ~40 个 reasoning 控件组件,每厂商每代一个专属枚举 | 加枚举值 + 加组件 + runtime 硬编码规则 | `gpt5_1ReasoningEffort` 和 `gpt5_2ReasoningEffort` 是两个字段——相邻两代都不能复用 |
| Roo Code | 多布尔字段被迫扩成数组 + OpenRouter fetcher 里 per-model-id 修正堆 | 改字段定义或加 per-id 修正 | "disable" vs "none" 双语义是踩坑后补的,注释专门解释两者 UI 同显示但行为不同 |

**抽象路线的实测账单**:

| 项目 | 存量 | 新出一代模型要干什么 |
|---|---|---|
| Cline | 一个 `reasoning_options` schema + `supportsOff` 一行派生 + 一个 era fallback 函数 | 等 models.dev 数据同步(小时级);未知 Claude 默认最新世代,发布当天不坏 |
| OpenCode | 数据编译成 variants map,UI 零知识 | 同上;fallback 正则族只在数据缺失时兜底 |
| Cherry Studio | 生成式 registry + `wireDialect` 显式字段,renderer 禁看 model id | creator 规则 2-3 天一改,但改动面集中、CI 防漂移 |

**再对我们自己算一遍。** 现状(补丁路线上的 t+1 时刻):策略逻辑 3 份(Web picker TS / `/reasoning` 命令 / 后端 resolver),跨 Go/TS 的 "keep in sync" 注释 7 处,client-type 特判名单 1 个(刚打的,Claude),已知未修的同型 bug 2 个(DeepSeek/MiniMax 的 Off、Gemini 的假档位)。照此外推,每接一个新 provider 家族,三份拷贝各有一次漏的机会,名单再长一条;aider 的 804 条就是这个循环跑了五年的样子。

改造后(提案三步走完):后端 1 个解析函数是唯一出口,前端 0 份能力逻辑、0 个名单,"keep in sync" 0 处;新 provider 家族的接入成本 = 在解析函数里加一个 case(这个 case 反正要写——adaptor 的 wire 编码躲不掉,区别只是写一处还是抄三处);新模型的接入成本 ≈ 0(数据同步 + 向前兼容默认)。

一次性实现成本当然是抽象路线更高——第一步要动 API、删重写前端,第二步要迁 schema。但这笔钱三个项目已经替我们验证过值得花,而且我们的删除量可观:TS 侧 `KNOWN_EFFORTS`/`canTurnOff`/`nearestEffortToMedium` 镜像和名单全部退役,净代码量大概率是负的。

---

## 三、结果:提案的三步走

三步互相独立,可以分开成 PR、分开排期。第一步不依赖换源,收益最大,建议先做。

### 第一步:后端解析,前端渲染(架构收敛)

后端加一个单一出口,按 `client_type + compat + catalog` 算出每个模型的:

```json
{
  "supports_off": true,
  "selectable_efforts": ["low", "medium", "high"],
  "default_effort": "medium"
}
```

通过 models API / SDK 下发。这些知识现在已经全在后端了(`offEffortFor`、`BuildReasoningOptions` 的 compat 分支、adaptor 语义),只是散着、没有一个出口。

然后:

- Web picker 和 `/reasoning` 命令降级为纯渲染;
- 删掉 TS 侧的 `KNOWN_EFFORTS`、`canTurnOff`、`nearestEffortToMedium` 镜像、`IMPLICIT_OFF_CLIENT_TYPES` 名单;
- 7 处 "keep in sync" 注释全部退役;
- **DeepSeek/MiniMax 的 Off 缺失顺手就修了**——后端 compat 层本来就知道它们能关。

### 第二步:catalog schema 把能力和档位拆开

provider YAML 从:

```yaml
reasoning_efforts: [disable, minimal, low, medium, high]
```

迁到 models.dev 同构的结构(具体字段名可以讨论,语义对齐就行):

```yaml
reasoning:
  toggle: true                # 或者由 effort 含 none 表达,二选一
  efforts: [minimal, low, medium, high]
  wire: adaptive              # Anthropic 世代显式化,不再从档位列表猜
```

这样:

- `disable` 哨兵从档位列表退役,`pickEffort` 的守卫、`orderedReasoningEfforts` 的排除注释这些防御性代码都可以删;
- `anthropicEffortEra`(从档位内容猜 wire 世代)换成显式字段 + Cline 式 fallback("未知 Claude 默认最新世代");
- #966 commit 3 的双语言永久 normalize 退化为一次性数据迁移。

### 第三步:数据源换血

- synccaps 主源切 **models.dev**(建议直接消费它 GitHub 仓库的 TOML,api.json 域名从国内访问偶发超时);
- **OpenRouter** `/api/v1/models` 的 `reasoning.mandatory` 做定期交叉校验,OpenRouter 渠道的模型直接用它;
- **Anthropic 官方 API** 按需补 adaptive 世代权威值;
- **LiteLLM 直接退役**。查了一下 synccaps 的实际消费面:我们从 LiteLLM 只读能力 flag 和 `max_input_tokens`,**一个价格字段都没碰**(开源仓库也不需要计价)。models.dev 的 `limit.context`/`limit.output` 覆盖 context window,能力语义又强一截,所以 LiteLLM 没有留下的理由——顶多在 synccaps 里留个交叉校验开关。

### 那 #966 怎么办:合,但为什么不一步到位?

**建议合。** 单一 token、picker 诚实化、`none` 降回 wire 拼写,都是净改进,而且它的 normalize 边界处理为第二步的迁移铺了路。

"为什么不干脆撤了 #966,直接一步到位做终态"——这个问题值得正面回答,因为答案不是"懒",是三个实际约束:

1. **一步到位 = 一个巨型 PR。** 终态横跨 API schema(加字段)、DB 迁移(存量 `reasoning_efforts` 数据)、synccaps 重写(换源)、前端删重写、`/reasoning` 命令改造——一次做完就是又一个 #563 规模的 PR(50 个文件起步),review 质量和回滚粒度都会崩。而这三步天然有依赖顺序:第一步(下发解析结果)不动数据格式,先把消费端收敛;第二步(schema)才动数据;第三步(换源)只动 synccaps。每步单独可测、单独可回滚。
2. **#966 修的 bug 是现行的,终态是需要团队排期的。** "picker 里 Off/None 并列"、"Off 出现在关不掉的模型上"是用户现在就能踩到的问题,#966 已经写完、测完、review 过一轮。让一个已完成的修复排队等一个还没开工的架构改造,等于把已知 bug 多养一个季度。
3. **#966 的产出物不会被终态浪费。** 单一 token 约定、`IsReasoningDisabled` 的 legacy 兼容、normalize 的边界位置(写前/读后),这些在第二步里都直接复用——迁移脚本就是把 normalize 逻辑跑一次固化。真正会被替换的只有前端 `canTurnOff` 那一小片,而那片本来就是几十行。

唯一的合入前置条件:**把 DeepSeek/MiniMax 的 Off 补上**(最小改法:在它们的 YAML/discovery 里声明可关闭;或者干脆等第一步一起修,但要在 #966 里留注释和 issue 链接,别让这个已知洞变成无主的)。同时把本文档的三步开成 issue 挂在 #966 上,明确 commit 4 的 Claude 特判是过渡态,不是先例。

---

## 常见问题

**Q: 为什么不直接换数据源就完事?**
因为就算数据再完美,Google adaptor 没接 effort、DeepSeek 靠 compat 翻译这些 **adaptor 层事实**还是只有后端知道。换源解决"原料缺语义",架构收敛解决"三个 surface 各自猜"。两个问题,两味药。

**Q: models.dev 数据质量靠谱吗?**
比 LiteLLM 在 reasoning 语义上强得多(LiteLLM 压根表达不了),而且 Cline、OpenCode、Vercel AI Gateway、Cherry Studio 四个头部项目在生产使用。有编辑规范 + 人审 PR + 小时级自动 sync。当然任何外部源都会有错漏,所以才要 OpenRouter 交叉校验 + 我们自己的 synccaps 人审流程兜底(这套流程 #563 就建好了,继续用)。

**Q: 工作量多大?**
第一步是主体:后端一个解析函数 + API 字段 + 前端删代码(删的比加的多);第二步是 schema 迁移 + synccaps 改造;第三步主要是 synccaps 的数据源适配。三步都有现成参照(Cline 的实现几乎可以照着看)。具体排期等团队同意方向后再拆。

**Q: 换掉 LiteLLM 后 context window 从哪来?**
models.dev 自带:`limit.context` / `limit.output` 是每个模型条目的标准字段(实测 claude-opus-4-6 → context 1,000,000,和 LiteLLM 的 `max_input_tokens` 一致)。我们从 LiteLLM 实际只消费能力 flag 和 `max_input_tokens` 两类信息,models.dev 都有替代,不存在换源后缺数据的问题。

**Q: 不做会怎样?**
每接一个新 provider、每出一代新模型,三份策略拷贝就有机会漏一次。#966 四个 commit 的演化和 DeepSeek/MiniMax 的现行 bug 已经演示过了;aider 和 LobeChat 演示了五年后的样子。
