---
name: memoh-frontend-rules
description: >-
  Memoh 前端对用户和维护者的承诺：红线、冲突处理顺序，以及每条承诺在本仓库中的
  实现方式。在 apps/web 或 Desktop 渲染层编写、修改或审查代码时使用：页面、
  组件、composable、store、服务端数据、路由、i18n、流式传输。不用于
  @felinic/ui 的 token 和视觉（使用 packages/ui 的 skill）、后端代码，或只修改
  文档的工作。
---

# Memoh 前端规则

适用于 `apps/web`（文中未标前缀路径均在 `apps/web/src/` 下）及复用它的 `apps/desktop` 渲染层。引用其他文档章节名保留英文原文以便检索。

## 使用流程与存量边界

1. 阅读本文件，从下方规则索引选出本次任务相关条目，按 `references/rules.md` 目录行号按需加载对应小节（行号不符时按标题查找），不必整篇通读。
2. 交接前对照 `references/review.md` 对应条目与 `D1–D4` 检查 diff。
3. **增量从严、存量单拆**：新改代码不得新增或扩大违规；抄样板只参照 `references/architecture.md` § 认证样板参考页。无关存量问题记入交接说明另行立项，按 `M8` 分步重构，不混入业务 PR（`D1`）。

## 背景与目标

写 Memoh 前端的人背景各不相同：有前端工程师、平时只写后端的工程师、直接让 AI 生成
代码的人，以及每次会话都从零开始、只能靠当前读到的文档做判断的 agent。不管是谁写的，
合入后都必须像同一个产品。

这套规则起因于一个真实问题：有个列表没做缓存，每次点开都要空白等半秒。因为当时的文档
没写这条要求，写代码时也看不到提醒，顺手发个裸请求是最省事的写法。光靠批评作者只能修
这一处；只有把要求写成明确的规则，再把正确做法封装成最顺手的写法，所有人和 agent 才会
自然照着做。

这套规则同时解决三件事：
1. **一致性**：同一个概念在全站长相和交互完全一样。
2. **开发效率**：已经有的组件和逻辑直接拿来用，不重复造。
3. **演进成本**：今天写的代码不能给下次修改埋坑。这三件事里，演进成本最容易被忽略，也最关键。

以前这些分寸靠老同事带人和 Code Review 口头传授；现在写代码的人未必认识老同事，agent
更不会自己去猜潜规则。所以必须把判断标准白纸黑字写清楚，让完全没背景的读者和 agent
光靠这份文档就能推出正确做法。如果某条规则必须知道文档外的背景才能用对，说明规则本身
还没写透，要在交接说明里记一笔。

## 五条原则

后面的 22 条具体规则，全是从下面这五条原则推出来的。只有先看懂每条规则背后的原则，才
知道怎么在具体代码里拿捏分寸；遇到规则没写到的新场景，也靠这五条原则从零推导出做法。

1. **绝不弄丢用户交给我们的数据，也绝不替用户乱动界面。**
   用户的密钥不能泄露，填了一半的表单和选好的配置不能被后台刷新冲掉，任何报错或空状态
   都要给下一步出路，用户没点鼠标时绝不能擅自抢焦点或弹面板。用户把工作和密钥交给产品，
   全靠信任；出一次事故信任就没了。（推出规则：U1、U2、U6、U7）

2. **少让用户重新学、少让用户干等、少拿无关信息晃眼。**
   同一个东西在全站保持同一种长相和用法，用户学一次就会；已经看过的页面再点开要秒出，
   不退回空白重新转圈；页面切换的每一帧都要排版完整，不能闪一下坏掉的画面；一屏只突出
   一件事，手机窄屏、深浅色、中英日三语、断网重连和久开不关的标签页下都能正常工作。每多
   造一种变体、多一次等待或多塞一个装饰，所有用户每次用都要多花精力。（推出规则：U4、U5、U8、U9、U10）

3. **同一件事只留一个真源。**
   一种界面概念只由一个组件（owner）负责，一份服务端数据只走一条请求与缓存链路，一个
   视觉数值只对应一个 token；浏览器原生或现有成熟库能干的事，绝不自己手写；多端共用的
   代码只写一份，桌面端差异靠外部注入。一旦出现两份实现，改了 A 就会漏掉 B；在源头收口
   只花一次功夫，散在各处维护会永远还债。（推出规则：M1、M2、M3、M4、M6）

4. **今天写的代码，要让下个人好改。**
   组件接口只暴露当前真正在用的参数，不提前为假想需求留口子；同一个模式第二次出现时再抽
   公共组件；修 bug 要修根因，不能打补丁掩盖症状；一次只改需求范围内的代码；当一个公共
   组件开始塞进互相打架的特判时，立刻停手拆分。代码只写一次但会被改无数次，而且仓库里的
   现有代码会被后续的人和 agent 当成样板照抄——留下一处脏写法，很快就会繁殖出十处。
   （推出规则：M2、M5、M7、M8、D1）

5. **不管是界面给用户的反馈，还是你给同事的交接，没确认的事绝不乱说。**
   界面上显示“已保存”就代表后端真落库了，显示“实时画面”就代表画面帧真到了；交接时说
   “验证通过”就代表真跑过对应检查，改了哪些行为和默认值要逐条列清，加了豁免注释要写明
   原因。本规则文档自己也守这一条：代码里还没做到的事，绝不在文档里假装已经做到。虚假的
   “已保存”和虚假的“已测过”，都会害别人基于假消息做错决定。（推出规则：U3、D2、D3、D4）

## 冲突优先级与实现准则

- **冲突裁决顺序**（前项无条件高于后项）：`1. 数据安全与状态属实（U1–U3） > 2. 不改动范围外功能（D1） > 3. 全站一致（U9、M2、M4） > 4. 界面克制（U10） > 5. 单页局部效果`。仍无法裁决时停下确认并记入交接说明。
- **需求实现顺序**：先查 `@felinic/ui`、`ui-owners` 字典与认证样板页。字面需求踩红线时，用合规原语达成同一产品目标并向需求方说明；确无现成原语时先说明方案并经确认，首次在页面内写、第二次复用再抽共享 owner（`M2`）。
- **权威依据顺序**：`平台标准（WHATWG HTML / WCAG 2.2 / WAI-ARIA APG / Apple HIG / Material 3） > 锁定版本库官方文档 > 同类设计系统`（详见 `references/sources.md`）。未明文覆盖的场景沿上述五条原则、冲突优先级与现有原语体系推导，并在交接说明注明。

## 规则索引

标记 **`[红线]`** 的条目一旦踩中（露密、丢输入、假报状态/吞错、裸 `fetch`/自建缓存、手写视觉数值、重造现有组件、缺三语、后台抢焦点、Desktop 分叉 Web、无实测证据报完成），改动直接退回：

| 编号 | 规则 | 适用场景 | 当前保证方式 |
|---|---|---|---|
| [U1](references/rules.md#u1-不泄露不越权) | 不泄露，不越权 **`[红线]`** | 显示、缓存、持久化或打印服务端/模型/工具/渠道数据；权限显隐 | 结构（部分） |
| [U2](references/rules.md#u2-用户输入不丢失) | 用户输入不丢失 **`[红线]`** | 表单、选择、上传、草稿，尤其同屏服务端数据会刷新时 | 结构（部分） |
| [U3](references/rules.md#u3-界面显示的状态属实) | 界面显示的状态属实 **`[红线]`** | 状态、进度、成功、报错（含 toast）、数量、实时画面或未知结果 | 结构（部分） |
| [U4](references/rules.md#u4-已获取的数据立即显示加载反馈遵守固定时限) | 已获取的数据立即显示；加载反馈遵守固定时限 | 服务端数据渲染、对话入口（Composer-First）、加载态时长 | 结构（部分）；机械检查（部分） |
| [U5](references/rules.md#u5-过渡中的每一帧都是有效画面) | 过渡中的每一帧都是有效画面 | 路由切换、列表/详情切换、弹窗、面板、加载切换与布局稳定 | 文字 |
| [U6](references/rules.md#u6-每个状态都提供后续操作确认强度与后果相称) | 每个状态都提供后续操作；确认强度与后果相称 | 空状态、失败、拦截或部分完成；可逆 vs 破坏性操作确认 | 文字 |
| [U7](references/rules.md#u7-用户控制) | 用户控制 **`[红线]`** | 后台事件/定时器、焦点、面板、tab、滚动、历史栈与快捷键 | 文字 |
| [U8](references/rules.md#u8-在所有运行环境下成立) | 在所有运行环境下成立 **`[红线]`** | 三语 i18n、深浅色、缩放/窄屏、跨 tab、长连接节流与无障碍 | 机械检查（部分） |
| [U9](references/rules.md#u9-同一概念在各处一致) | 同一概念在各处一致 | 复用全站或已有交互、控件外观、排版关系 | 结构；机械检查（部分） |
| [U10](references/rules.md#u10-克制每屏一个重点高级功能按需显示) | 克制：每屏一个重点，高级功能按需显示 | 新增可见区块、徽标、说明文案、按钮、设置项或强调色 | 文字 |
| [M1](references/rules.md#m1-优先使用平台其次使用库不自行实现) | 优先使用平台，其次使用库，不自行实现 | 弹层、焦点锁定、拖拽、虚拟列表、防抖、日期、过渡等通用能力 | 机械检查（部分） |
| [M2](references/rules.md#m2-一个概念一个-owner) | 一个概念一个 owner **`[红线]`** | 编写或修改组件、composable、store 或样式 | 结构；机械检查（部分） |
| [M3](references/rules.md#m3-服务端数据只有一条访问路径) | 服务端数据只有一条访问路径 **`[红线]`** | 读写服务端数据、查询 key、缓存失效或共享数据流 | 机械检查（部分） |
| [M4](references/rules.md#m4-视觉取值来自档位) | 视觉取值来自档位 **`[红线]`** | 颜色、字号、间距、圆角、阴影、透明度、z-index 与动效时长 | 机械检查（部分） |
| [M5](references/rules.md#m5-修复根因) | 修复根因 | 修复 bug/lint、系统边界校验清洗、消除假环境守卫与兜底补丁 | 文字 |
| [M6](references/rules.md#m6-保持分发边界) | 保持分发边界 **`[红线]`** | Web 与 Desktop 复用代码、OSS 与 Cloud 边界 | 结构；机械检查（部分） |
| [M7](references/rules.md#m7-性能有预算失败可追溯) | 性能有预算，失败可追溯 | 新依赖、重型组件、长列表、轮询、输入处理或报错链路 | 文字 |
| [M8](references/rules.md#m8-owner-保持可修改) | owner 保持可修改 | 扩展 owner prop/特判、超大文件拆解、路由/模块迁移收口 | 文字 |
| [D1](references/rules.md#d1-只改动需求范围内的内容) | 只改动需求范围内的内容 | 交接任何改动前检查 diff 纯净度与重构行为一致性 | 文字 |
| [D2](references/rules.md#d2-逐项列出行为外观和默认值的改动) | 逐项列出行为、外观和默认值的改动 | 交接或提 PR 前逐项列清可见变化与默认值调整 | 文字 |
| [D3](references/rules.md#d3-声明不超过证据) | 声明不超过证据 **`[红线]`** | 报告验证范围、运行截图与区分 Agent 自测 / 人类 QA | 文字 |
| [D4](references/rules.md#d4-检查全部通过每个豁免都写明理由) | 检查全部通过；每个豁免都写明理由 | 交接前 `mise run lint`、typecheck、测试及豁免说明 | 机械检查 |

## 高频惯性纠偏速查

| 惯性写法（禁止） | 本仓标准做法 | 规则 |
|---|---|---|
| `onMounted` 手写拉取塞 `ref` 或裸 `fetch` | `@memohai/sdk` + Pinia Colada `useQuery`/`useMutation`，变更后 `invalidateQueries` | [M3](references/rules.md#m3-服务端数据只有一条访问路径)、[U4](references/rules.md#u4-已获取的数据立即显示加载反馈遵守固定时限) |
| 骨架屏或整页内容绑 `v-if="isLoading"` | 骨架屏只绑 `isPending`；`isLoading` 仅作后台刷新轻量提示 | [U4](references/rules.md#u4-已获取的数据立即显示加载反馈遵守固定时限) |
| 等待用户/Bot/会话/历史加载完才显示或启用输入框 | 输入框首帧可打字（Composer-First），加载期仅拦 `handleSend` | [U4](references/rules.md#u4-已获取的数据立即显示加载反馈遵守固定时限)、[U7](references/rules.md#u7-用户控制) |
| `watch(() => query.data.value, ...)` 直接覆盖表单 | `useServerSyncedRecord` / `useServerSyncedScalar` 保护未保存草稿 | [U2](references/rules.md#u2-用户输入不丢失) |
| `toast.error(err.message)` 直出原文，或拿文案做分支；带按钮 toast 用默认 4 s | 展示用 `resolveApiErrorMessage`，分支用 `isApiErrorCode`；带按钮 toast 设 `8000` 或 `Infinity` | [U3](references/rules.md#u3-界面显示的状态属实) |
| 手写卡片/行 `div` 或 `bg-muted/40`、`text-[13px]`、`z-50`、`animate-spin` | 组合 `PageShell`/`SettingsSection`/`SettingsRow`/`DialogPanel`，用语义 Token 与四级加载组件 | [U9](references/rules.md#u9-同一概念在各处一致)、[M2](references/rules.md#m2-一个概念一个-owner)、[M4](references/rules.md#m4-视觉取值来自档位) |
| 业务层直引 `reka-ui` 或共享 store 直引 `@/router` | 通过 `@felinic/ui` 消费；共享 store 用宿主注入 router 与 `DesktopShellKey` | [M1](references/rules.md#m1-优先使用平台其次使用库不自行实现)、[M6](references/rules.md#m6-保持分发边界) |
| 后台 SSE/WS 到来时调用 `focusPanel` / `activateChatSession` 抢焦点 | 后台只静默更新对应 tab 数据，跳转做成显式按钮 | [U7](references/rules.md#u7-用户控制) |
| 纯客户端代码写 `typeof window === 'undefined'`，对内部 ID 跨层 `.trim()`，或在 `.vue` 直解裸 `metadata` | 仅用户输入与 `utils/`、`composables/api/` 专用 normalizer 收窄转换一次；内部信任 TS 类型，缺 DOM 配测试环境 | [M5](references/rules.md#m5-修复根因) |
| 往超千行页面/Store 继续塞新弹窗与独立子状态机，或合并路由后遗留跨目录旧组件 | 新增独立职责拆为聚焦子组件或 composable；合并路由同 PR 收拢遗留目录并更新文档 | [M8](references/rules.md#m8-owner-保持可修改) |

## 关联指南与参考文件

| 文档 | 用途 |
|---|---|
| [references/architecture.md](references/architecture.md) | 目录地图、认证样板页、局部原语、6 步重构自检、三层布局、Store 分工、WS/SSE 与依赖操作契约 |
| [references/rules.md](references/rules.md) | 22 条规则的完整契约、机制实现、边界与保证层级（按索引行号按需读取） |
| [references/review.md](references/review.md) | 交接自检与 Code Review 检查清单 |
| [references/upkeep.md](references/upkeep.md) | 违规登记、保证层级提升与规则维护规范 |
| [references/sources.md](references/sources.md) | 官方规范与库文档权威依据清单 |
| [scripts/check-contrast.mjs](scripts/check-contrast.mjs) | OKLCH 叠加对比度验算脚本（`node scripts/check-contrast.mjs`） |
| `packages/ui/AGENTS.md` / `ui-owners` / `ui-patterns` | `@felinic/ui` Token 与设计契约、原子/布局原语字典、跨组件交互规范 |
| `.agents/skills/memoh-error-handling/SKILL.md` | 稳定错误码 `apperror` + `Problem` + SSE 错误流规范 |
| `apps/desktop/AGENTS.md` / 根目录 `AGENTS.md` | Desktop 壳与类型桩约束；PR、截图留证与 QA 披露流程 |
