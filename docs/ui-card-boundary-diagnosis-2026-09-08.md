# Bot 设置页卡片边界诊断

> 本文记录诊断时的现场状态。后续提交、图标修复及整体交接见 `ui-handoff-2026-09-08.md`；下文“未提交”描述的是诊断时点。

## 事情缘由

这份报告来自 2026-09-08 的一轮 Memoh UI 调整。此前讨论涉及 Computer / Connectors 菜单、图标视觉重量、菜单动画、分割线和阴影。用户的持续关注点是：不要靠逐个页面、逐个图标调数值维持一致性，要让共享实现能够承接视觉规则。那些菜单和图标工作是背景，不是本报告要求后续 agent 一并重做的范围。

进入本次问题前，普通设置卡片已经采用“浅色保留外描边、深色主要靠卡片填充区分层次”的方向，SettingsSection 已默认去掉深色外边框。用户随后在实际页面继续看到相邻卡片有的有边框、有的没有，要求修复一致性，而不是重新设计整套颜色。用户明确说过字体及深浅色颜色的系统调整“我们今天不做这个吧”，因此不要把边框排查扩展为文字颜色或主题重做。

### 问题如何逐步暴露

| 顺序 | 用户发现的实际页面 | 排查和处理 | 为什么仍未结束 |
| --- | --- | --- | --- |
| 1 | Overview 的 CPU / Memory / Storage 卡片仍有深色描边，相邻普通卡片没有 | MetricReadout 自己定义边框，补齐深色规则并保留 bordered 嵌套入口 | SettingsSection 的修改不会自动影响独立组件 |
| 2 | Workspace 的 “No metrics available” 仍有描边 | 无指标分支和生命周期提示是页面手写外壳，改为 SettingsSection | 只检查有数据的指标卡，覆盖不到空状态 |
| 3 | Email Outbox 和 Access 的 Advanced rules 仍有描边 | 找到 Table 与 ActionCard 的独立规则，同时处理 BackendCard | 即使补齐这些组件，页面手写外壳仍然存在 |
| 4 | Desktop 的 Live view 仍有描边 | 本次继续检查 Bot 设置页，迁移 Live view、详情头、侧栏身份卡及两处列表空状态 | 由截图逐点发现问题，不能证明全站规则已统一 |

最后一轮用户要求：“修掉，然后给我一个诊断，我看看需不需要在组件库上做工作”。因此本报告既记录已修复的页面，也解释为什么连续几次局部修复没有解决整体一致性。不能把这段过程理解为用户已经批准了某个 Surface API 或全站组件库重构；下面的架构方案仍是待评估建议。

用户随后要求补充事情缘由，以便之后的 agent 接手。本文是这次问题的交接记录，不是新的永久项目规范。

## 接手位置与当前状态

以下是记录时的工作环境，后续接手应先核实当前 Git 状态和实际前端进程：

- 工作目录：`/Users/qqqqqf/Documents/Memoh-ui-live-preview`，分支 `codex/ui-live-preview`。
- UI 子模块：该目录下的 `packages/ui`，分支 `codex/ui-design-checkpoint`。应用和子模块都要检查，不能只看主仓库 diff。
- 用户查看的前端：`http://localhost:18083`；本次实际验证使用 `/settings/bots/111?tab=desktop`。不要因为默认 cwd 是另一个 Memoh checkout 就在那里修改。
- 工作区包含此前菜单、图标及图标加载调整的未提交改动；本轮边框改动也未提交。保留这些改动，不通过 reset、stash 或替换子模块状态来制造干净基线。
- 本报告没有把整轮 UI 调整归为已通过 QA，也没有完成所有建议中的架构工作。已做、未做及验证边界见后文。

后续若获得继续实施的授权，应先核实下文的剩余调用方和现有 owner，再决定共享样式是否足够、是否确实需要新增组件。验收应包含真实设置路由的深浅色、空/有内容分支，以及需要保留边框的嵌套区域；不能仅以 lint 通过作为一致性完成的依据。

## 结论

需要在组件库做一次有限的表面样式收敛，无需重写组件库。重复遗漏同时来自共享组件各自拥有边框规则、页面手写卡片，以及验证只覆盖命中截图。单独再调 border token 无法解决：输入框、内部线、弹层和独立卡片共享该颜色，却有不同的边界需求。

## 本次修复

六处页面表面改为组合已有 SettingsSection：Desktop Live view、平台连接详情头、MCP 详情头、Bot 设置侧栏身份卡、平台列表空状态、MCP 列表空状态。页面保留内容、操作和布局职责，外壳由组件管理。

空状态现在使用普通卡片填充；平台空状态原来的虚线外框也改为共享规则（浅色实线、深色无外框）。列表中的虚线添加按钮保留，它表示可操作的添加位置。

上一轮已修 Table、ActionCard、BackendCard；此前修 MetricReadout 和 Workspace 无指标状态。它们仍不是同一个底层表面实现，因此这里没有宣称架构收敛已完成。

## 调查依据与边界

从 Desktop 页面沿 DisplayPane 与 SettingsSection 追踪，枚举 apps/web/src/pages/bots 中 Vue 模板的边框及卡片背景声明，再检查页面、详情与弹窗所处的组合上下文；另外查看 UI 库内使用卡片背景的共享组件与其调用方。

发现并修复的页面绕过点对应文件：

- apps/web/src/pages/bots/components/bot-desktop.vue
- apps/web/src/pages/bots/components/channel-settings-panel.vue
- apps/web/src/pages/bots/components/mcp-server-detail.vue
- apps/web/src/pages/bots/detail.vue
- apps/web/src/pages/bots/components/bot-channels.vue
- apps/web/src/pages/bots/components/bot-mcp.vue

明确保留：输入和操作按钮边界、表格内部线、导入选择与错误状态、代码/编辑器区域、弹窗中的快照和详情分组，以及添加按钮的虚线边界。这些不等价于独立普通卡片。

范围外仍有独立实现：通用 Card（Supermarket 卡片调用）、PersonaTile（Bot 列表调用）、Bot 列表加载骨架。它们需要按各自布局和交互确认，不能据本次 Bot 设置页面修复推断全站已经统一。

## 根因

1. **组件库缺少共同的表面所有者。** SettingsSection、MetricReadout、Table、ActionCard、BackendCard 分别声明 border 和背景；修改其中一个不会传播到其他组件。当前 bordered 属性为嵌套边界保留入口，但规则仍重复。
2. **页面能直接复制视觉配方。** Live view 与身份头都是合法 Tailwind class，却绕过已有组件。组件库有实现不等于调用方使用它。
3. **现有检查覆盖的是语法约束。** UI contract guard 允许合法 token 和圆角；它不能推断一个带边框的 div 是普通卡片还是需要边界的编辑器。检查通过不能证明外观一致。
4. **此前排查未覆盖状态与组合。** 只修截图中的组件，漏掉空状态、详情头和侧栏身份卡。此项属于执行覆盖不足，不能全部归因于组件库。

## 建议的下一步（尚未实施）

- 为独立卡片表面提取一个最小 owner（共享样式或无布局 Surface），只管理背景、外边框、圆角；不要让它接管内容间距、点击事件和表格结构。现有组件复用它。
- 明确普通表面与嵌套表面的边界模式；弹层、输入框和内部线继续各自管理，不全局清空 border。
- 为主要 owner 建立浅色/深色 × 普通/嵌套 × 空/有内容的浏览器验证，检查计算样式并保留截图。对页面迁移用代表性真实路由补充验证。
- 增加针对新增手写卡片配方的提示或检查；旧实现逐步迁移。不要仅凭 class 搜索把所有 border 判为违规。

## 验证

运行目录 Memoh-ui-live-preview，前端 localhost:18083。

- Desktop 实际路由：共享外壳浅色 1px、深色 0px；画面比例 4:3；真实桌面内容正常渲染。侧栏身份卡深色外边框也为 0px。
- 本次改动文件 ESLint 通过；UI contract 通过并保留 3 条已有警告。
- 平台/MCP 详情头及空状态已检查模板与 lint，没有逐个完成浏览器交互验收。
- 未执行新的全量类型检查；未 commit、push；没有新增真人 QA 结论。
