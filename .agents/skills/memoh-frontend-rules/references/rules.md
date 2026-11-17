# Memoh 前端规则：完整条目

先查看 `../SKILL.md` 规则索引，按本目录行号按需加载对应小节。

## 目录

- [1. 面向用户的承诺](#1-面向用户的承诺), L:33
  - [U1. 不泄露，不越权](#u1-不泄露不越权), L:35
  - [U2. 用户输入不丢失](#u2-用户输入不丢失), L:48
  - [U3. 界面显示的状态属实](#u3-界面显示的状态属实), L:61
  - [U4. 已获取的数据立即显示；加载反馈遵守固定时限](#u4-已获取的数据立即显示加载反馈遵守固定时限), L:75
  - [U5. 过渡中的每一帧都是有效画面](#u5-过渡中的每一帧都是有效画面), L:89
  - [U6. 每个状态都提供后续操作；确认强度与后果相称](#u6-每个状态都提供后续操作确认强度与后果相称), L:102
  - [U7. 用户控制](#u7-用户控制), L:114
  - [U8. 在所有运行环境下成立](#u8-在所有运行环境下成立), L:126
  - [U9. 同一概念在各处一致](#u9-同一概念在各处一致), L:142
  - [U10. 克制：每屏一个重点，高级功能按需显示](#u10-克制每屏一个重点高级功能按需显示), L:153
- [2. 面向维护者的承诺](#2-面向维护者的承诺), L:166
  - [M1. 优先使用平台，其次使用库，不自行实现](#m1-优先使用平台其次使用库不自行实现), L:168
  - [M2. 一个概念一个 owner](#m2-一个概念一个-owner), L:180
  - [M3. 服务端数据只有一条访问路径](#m3-服务端数据只有一条访问路径), L:192
  - [M4. 视觉取值来自档位](#m4-视觉取值来自档位), L:206
  - [M5. 修复根因](#m5-修复根因), L:218
  - [M6. 保持分发边界](#m6-保持分发边界), L:232
  - [M7. 性能有预算，失败可追溯](#m7-性能有预算失败可追溯), L:244
  - [M8. owner 保持可修改](#m8-owner-保持可修改), L:256
- [3. 完成的定义](#3-完成的定义), L:271
  - [D1. 只改动需求范围内的内容](#d1-只改动需求范围内的内容), L:275
  - [D2. 逐项列出行为、外观和默认值的改动](#d2-逐项列出行为外观和默认值的改动), L:279
  - [D3. 声明不超过证据](#d3-声明不超过证据), L:283
  - [D4. 检查全部通过；每个豁免都写明理由](#d4-检查全部通过每个豁免都写明理由), L:287

## 1. 面向用户的承诺

### U1. 不泄露，不越权

**适用场景：** 显示、缓存、持久化或打印服务端、模型、工具、渠道或其他用户数据；按权限控制控件。

**契约与原因：** 密钥绝不出现在界面、`console` 或浏览器本地存储；模型/工具/渠道内容一律作为不可信输入处理；权限只由服务端校验。防止通过截图、投屏、导出存储或外部污染 HTML 造成凭据泄露与 XSS。

**本仓机制：**
- 查询缓存持久化至 `localStorage`（`memoh:query-cache`，`lib/query-cache-persistence.ts`）：`deepStripSecrets` 剥离敏感字段，`EXCLUDED_QUERY_KEY_HEADS` 排除整条返回凭据或原始配置字符串的查询（新加同类查询须列入）。
- 模型/工具/渠道文本统一经 `components/markdown/`（`markstream-vue`）渲染；仅经 Shiki 转义的高亮代码块保留带行内 eslint 豁免注释的 `v-html`。
- `meta.adminOnly` 守卫与按权限隐藏入口（如无 `workspace_read` 隐藏 Files 视图）仅作交互减噪，服务端承担最终鉴权。

**边界与保障：** 前端拿不准权限标志时保留控件由服务端拦截；隐藏无权入口遵循 U10。保证方式：**结构**（缓存脱敏与整条排除）+ **文字**（其余，来源：用户确认；无法仅靠 AST 判定任意字段是否为敏感凭据）。

### U2. 用户输入不丢失

**适用场景：** 表单、选择器、上传或配置草稿，尤其同屏展示会自动刷新的服务端数据时。

**契约与原因：** 用户填了一半的草稿、选中的筛选项与主动配置，在后台刷新、重绘、切窗口或切 tab 后保持原样。Pinia Colada 切回窗口会重拉并返回新对象，直接覆写表单会冲掉用户输入。

**本仓机制：**
- 服务端初始化表单统一使用 `useServerSyncedRecord` / `useServerSyncedScalar`（`composables/use-server-synced-form.ts`）：仅在切换目标实体时重置，同实体后台刷新只更新未修改字段；保持 `refetchOnWindowFocus` 开启供只读视图刷新。
- 聊天输入框按 Bot 记住已选模型与推理强度（`pages/home/components/chat-pane-send.ts`），不被默认值覆盖；发送成功后才清空输入。
- 多步创建流程落盘已建 Bot ID（如 `bot-create-progress`），刷新后接续同一实体而非重复创建。

**边界与保障：** 主动切换实体重置表单属于正常行为；密码/密钥草稿绝不落盘（U1）。保证方式：**结构**（表单同步 helper）+ **文字**（其余，来源：用户确认）。

### U3. 界面显示的状态属实

**适用场景：** 显示状态、进度、成功、报错（含 toast）、数量、“实时”标识或未知结果。

**契约与原因：** 状态必须与真实后端落库或真实媒体帧一致；报错留在当前视线附近并给出下一步出路。普通 toast 默认 `DEFAULT_DURATION = 4000`（4 s，悬停/聚焦暂停），而带操作按钮（`action`）的 toast 至少停留 `8 s` 或常驻（`duration: Infinity`），避免按钮在用户反应前消失（依据 Material Design 3、IBM Carbon，负责人于 2026-10-28 确认）。

**本仓机制：**
- 报错展示统一走 `resolveApiErrorMessage(error, fallback)`：按 `errors.<code>`（带 `args` 插值）→ HTTP 状态码兜底（4xx 含 429 走 `errors.http.*`、5xx 走 `errors.internal`、499 静默）→ 流错误兜底（`errors.internal`，`fault === 'canceled'` 静默）→ `fallback`；除显式 `UserFacingError` 外不展示服务端原始 `message`/`detail`。业务分支只用 `isApiErrorCode(error, 'x.y')`（`.agents/skills/memoh-error-handling/SKILL.md`；流式错误走 `error` 事件，旧 `processing_failed` 已移除）。
- 弹窗提交统一走 `useDialogMutation`（失败自动 `toast.error`、返回 `false` 并保留弹窗与输入）；自动保存成功静默，失败报错并回滚乐观更新（`ui-patterns` `references/saving-and-notifications.md`）。
- `@felinic/ui` 的 `toast`（`packages/ui/src/components/sonner/toast.ts`）供应用/依赖 store 为带按钮 toast 设 `ACTIONABLE_TOAST_MS = 8000` 或 `duration: Infinity`；**当前缺口**：`toast.ts` `create()` 尚未根据 `options.action` 自动切换长驻，调用方须显式传 `duration`。
- 实时桌面以实际收到首帧为准（WebRTC 首帧前即报 `connected`，`degraded-and-destructive.md`）；断流依赖操作在服务端对账前显示未知且不给重试（`references/architecture.md` § 工作区依赖操作契约）。

**边界与保障：** 后台刷新期可先展示旧缓存（U4），依赖最新态的操作需标明“正在刷新”或刷新完再放行。保证方式：**结构**（报错 helper、`useDialogMutation`）+ **文字**（其余）→ 目标 **结构**（`toast.ts` 自动延长带按钮通知）。

### U4. 已获取的数据立即显示；加载反馈遵守固定时限

**适用场景：** 渲染服务端数据（列表、详情、选择器、侧栏），进入对话页，或显示请求/操作加载态。

**契约与原因：** 已缓存数据打开即首帧渲染并后台静默刷新。无缓存等待按时长分档：**100 ms 内**给出按压/忙碌等视觉响应；**200 ms 内完成**直接出结果，不闪 spinner/骨架屏；**超过 200 ms** 展示同形骨架屏或按钮内转圈，且出现后至少稳住 **200 ms**；**可能超过 10 s** 显示阶段或进度且允许切走或取消（依据 Nielsen 0.1 s/10 s、Google RAIL 100 ms、MD3 200 ms、spin-delay 200 ms，负责人于 2026-10-27 确认）。

**本仓机制：**
- 服务端查询走 Pinia Colada `useQuery`（Colada 0.21 默认 5 s 新鲜、卸载保留 5 min、重聚焦后台重拉并持久化 `localStorage`）。骨架屏只绑 `isPending`；`isLoading` 仅作后台刷新轻量指示。
- 四档加载原语见 `packages/ui/skills/ui-owners/SKILL.md` § 加载与空占位原语（`ui-patterns` `references/loading-and-empty-states.md`），脚本拦截手写裸 `animate-spin`。
- **延时现状与豁免**：`LoadingButton`（`components/loading-button`，`loadingDelay` 默认 200 ms）与 `treeSpinnerDelayMs` 已延迟显示，新按钮请求一律用 `LoadingButton`；存量 78 处 `Button :loading` 与现有骨架屏暂不逐个改写，后续收口 `@felinic/ui` 底层。
- **Composer-First**：回访账号路由守卫本地放行并在后台校验；所有进入对话的路径第一帧渲染输入框并允许立即打字，不用用户信息/Bot/会话/历史/模型/WebSocket 加载态隐藏或 `disabled` 输入框（加载期仅拦 `handleSend`，位置从首帧起固定，见 `references/architecture.md`）。聊天记录切回渲染受 SSE 失效覆盖期约束。

**边界与保障：** 不可用过期值做决定的敏感数据（如 ACL 权限）不落盘；仅“全新浏览器首次登录未知账号”或“URL 指向本标签未记住的另一 Bot”两种未知目标 Bot 例外允许等待一次查询，且不显示旧 Bot 输入框。保证方式：**结构**（`LoadingButton`、回访守卫）+ **机械检查**（拦 `animate-spin`）+ **文字**。

### U5. 过渡中的每一帧都是有效画面

**适用场景：** 路由切换、列表与详情切换、弹窗/面板开关、加载态切换与组件挂载卸载。

**契约与原因：** 过渡期间任意一帧都应排版完整，不闪白屏、不新旧叠屏、不因高度或滚动条突现导致横向/纵向跳动。

**本仓机制：**
- 聊天主区 `MainSection` 常驻挂载于 `App.vue`，在聊天与 `/settings/*` 间切换不卸载（`KeepAlive` 保不住 dockview 分屏，详见 `references/architecture.md`）。
- 离开 `/settings` 时 `pages/settings-section/index.vue` 等 `after-leave` 动画结束后再放行路由守卫。
- 页内“列表 ↔ 详情”用 `useRoutedViewSwap`（`composables/useViewSwap.ts`）配 `SwapTransition` 同步交叉推拉；同级滚动容器加 `[scrollbar-gutter:stable]`；容器固定宽度、长文本截断，弹窗高度用 `AutoHeight` + `DialogPanel`（不手写 `max-height`）；动效与 Tailwind v4 过渡约束见 `packages/ui/AGENTS.md`。

**边界与保障：** 拿不准动画时直接切换（无动画）也是合格画面（`ui-patterns` `references/layout-and-navigation.md`）。保证方式：**文字**（来源：用户确认）。

### U6. 每个状态都提供后续操作；确认强度与后果相称

**适用场景：** 空、错误、失败、拦截或部分完成状态；可逆开关 vs 删除/不可撤回操作。

**契约与原因：** 异常状态说明原因并留出体面出口（重试、去配置、跳过）；低后果可逆操作直接执行不滥弹确认框，破坏性操作确认一次并写明损失对象，防止确认疲劳。

**本仓机制：**
- 参考样板：`pages/bots/new-progress.vue`（`bot-create-progress`）在容器创建失败时对同一已建 Bot 提供“重试工作区”与“稍后处理”，重名直达同名 Bot，刷新接续进度。
- 删除确认统一使用 `ConfirmDeleteDialog`（`packages/ui/skills/ui-owners/SKILL.md` § 弹窗与确认原语）。

**边界与保障：** 网络抖动可由 `useRetryingStream` 后台指数退避重试；带业务副作用的重试须保证幂等或由用户触发。保证方式：**文字**（来源：用户确认）。

### U7. 用户控制

**适用场景：** 后台事件、agent 工具流、定时器、自动保存，或涉及焦点、面板、tab、滚动、历史栈与快捷键。

**契约与原因：** 只有用户主动交互才能切换视图、面板或焦点；刷新与浏览器后退应还原位置；保留原生快捷操作（`Cmd/Ctrl+Click` 新标签打开、前进后退、复制、页内搜索、缩放）。

**本仓机制：**
- `store/workspace-tabs.ts`：后台事件只静默更新对应 tab 数据或清理已关闭 tab，只有用户点击路径可调 `focusPanel`、`activateChatSession` 或激活型 `open*`；需“跟随 agent 视角”做成显式按钮。
- 刷新后须保留的页内状态（tab、筛选、列表/详情）用 `useSyncedQueryParam`（`router.replace` 不污染历史栈）与 `useRoutedViewSwap` 写入 URL；返回按钮用 `useBackOr(fallback)`；跳转用真链接（`RouterLink` / `<a href>`）；向导下一步确认后焦点落入下一输入框（`ui-patterns` `references/forms.md`）。

**边界与保障：** 响应用户当次点击允许移动焦点；静默且可逆的自动保存不在此限。保证方式：**文字**（来源：用户确认；合法点击路径与后台事件路径难以静态跨栈穷尽）。

### U8. 在所有运行环境下成立

**适用场景：** 全站改动，尤其新增文案、颜色、可交互控件、布局、新页面、长连接或跨 tab 状态。

**契约与原因：** 须同时在手机窄屏/桌面宽屏、浅色/深色、中英日三语、200% 缩放、弱网/断网、久开标签页、多 tab 同账号、纯键盘与读屏器下成立。依据 WCAG 2.2（1.4.3、1.4.11、2.5.8、2.1.1、2.1.2、2.4.7、1.4.4、1.4.10、4.1.3 A/AA 及 2.3.3，结合 Radix/shadcn 弱边线惯例，负责人于 2026-10-28 确认）：
- **对比度**：正文/次要文字 `≥ 4.5:1`，大号文字（`≥ 24 CSS px` 或 `≥ 18.66 CSS px` 粗体）`≥ 3:1`；聚焦边线/焦点环/选中态/必要图标 `≥ 3:1`，未聚焦静止细边采用弱边线（深色实色灰 `1.54–1.76:1`）。
- **热区与重排**：可点击目标 `≥ 24×24 CSS px`（中心 24 px 圆不重叠，正文行内链接除外）；文字缩放 200% 内容不丢；320 CSS px 视口除数据表格外无横向滚动；支持全键盘无焦点陷阱、状态播报与 `prefers-reduced-motion`。

**本仓机制：**
- **三语、Token 与字号**：`i18n/locales/{en,zh,ja}.json` 同步补齐；语义 Token 适配深浅色（`packages/ui/AGENTS.md`）；文字相关尺寸一律用 `rem`（脚本拦截违规 `px`）。
- **断点与长连接**：`< 768px` 由 `useIsMobile` 切换移动外壳（全站唯一 JS 断点，桌面端最小宽 `960px`），其余排版用 `md:`；流连接走 `useRetryingStream`。因浏览器与 Electron（默认 `backgroundThrottling`）会对后台超 5 min 标签页限流定时器，`visibilitychange` 切回前台必须直接向服务端对账同步。
- **无障碍**：使用 `@felinic/ui` 原生按钮/链接；toast 自带 `role="status"` + `aria-live="polite"`；自定义动画加 `motion-reduce:` / `motion-safe:`。
- **对比度实测与待调基线（`scripts/check-contrast.mjs`）**：深色模式全达标（正文 `15.0:1`、次要 `6.6–7.6:1`、占位 `5.7–6.5:1`、聚焦边 `4.5–5.1:1`、静止细边 `1.54–1.76:1`）；浅色已达标正文 `16.8:1`、次要 `5.0–5.5:1`、聚焦边 `11.3–11.7:1`；**浅色待调（留待组件库统一调色）**：`--field-placeholder`（`oklch(0.66 0.004 95)`）为 `2.94–3.11:1`（降亮度 `L ≤ 0.56` 可达 `4.5:1`）、`--field-edge-rest`（11% 黑）为 `1.28:1`、键盘焦点环 `ring-ring/30` 浅 `1.4:1`/深 `2.0–2.2:1`。

**边界与保障：** 手机横屏（`≥ 768px`）按设计走桌面外壳。保证方式：**机械检查**（`px`/颜色拦截及 `check-contrast.mjs`）+ **文字**（多语折行/320px/跨 tab 需运行态验证）。

### U9. 同一概念在各处一致

**适用场景：** 实现全站已有或相似的列表行、按钮、标签、空状态或保存交互。

**契约与原因：** 同一概念全站只有一种外观与交互，职责不同外观须有区分，降低用户学习成本。

**本仓机制：**
- 区分对照控件（`Tabs` vs `SegmentedControl`、`TextButton` vs `Button`、`InfoHint` vs `ActionCard`，见 `ui-owners` § 易混基础原子选型）；`ui-owners` 字典已收录结构直接组合；保存位置遵从 `saving-and-notifications.md`，同行输入框与按钮等高。

**边界与保障：** 业务关系确有不同时保留差异，个人审美微调统一向 owner 收拢。保证方式：**结构**（`@felinic/ui` 与 owner）+ **机械检查**（拦截注入样式与卡片外 `min-h-[3.75rem]`）+ **文字**。

### U10. 克制：每屏一个重点，高级功能按需显示

**适用场景：** 新增区块、徽标、提示语、按钮、设置项、强调色或动画。

**契约与原因：** 每屏只突出一个核心用途，低频或复杂配置收进命名明确的二级入口，减少全站用户的视觉噪音。

**本仓机制：**
- 首屏只回答“运行是否正常”，复杂动作收进 `ActionCard` + 单任务弹窗（`ui-patterns` `references/progressive-disclosure.md`）；正常不挂横幅，异常才提示（`degraded-and-destructive.md`）；全站 90% 中性黑白灰，品牌紫仅作点睛（`packages/ui/AGENTS.md`）；旁已写清的文案不在副标题或 tooltip 复读。

**边界与保障：** 概览或确认界面应清晰亮出当前生效默认值；精简首屏是移入二级入口而非删功能。保证方式：**文字**（来源：用户确认）。

---

## 2. 面向维护者的承诺

### M1. 优先使用平台，其次使用库，不自行实现

**适用场景：** 需要弹层、焦点锁定、拖拽、虚拟滚动、防抖、媒体查询、剪贴板、日期格式化或过渡能力。

**契约与原因：** 原生平台 > 仓库已装标准库 > 经评估的新依赖；成熟实现已覆盖键盘导航、IME 组合键、无障碍与动画中断时序。

**本仓机制：**
- 标准库：`@felinic/ui`（封装 Reka UI，业务禁直接 import）、VueUse、vee-validate + Zod、Pinia Colada、`markstream-vue`（+ Shiki/Mermaid/KaTeX）、`lucide-vue-next`、`@memohai/icon`、`utils/date-time.ts`。
- 桌面内置 Chromium 版本以 `apps/desktop/package.json` Electron 为准，旧内核不支持的新 CSS 特性用库兜底；引入新依赖须在 PR 说明原生/现有库为何不足、体积健康度与全站复用面。

**边界与保障：** 可封装第三方库为业务 owner，或按需写三五行的纯函数工具并说明理由。保证方式：**机械检查**（ESLint 禁直接 import `reka-ui`、`@fortawesome/*`）+ **文字**。

### M2. 一个概念一个 owner

**适用场景：** 新建或修改组件、composable、store 或样式。

**契约与原因：** 已有 owner 直接复用或直接改善该 owner 使全站受益；首次出现的新结构留在页内目录，第二次跨页复用时再抽为共享 owner 并保持最窄必需 API，避免两份分叉或过早过度抽象（M8）。

**本仓机制：**
- 三层归属：`@felinic/ui`（无业务语义基础原子，位于 `packages/ui`）→ `components/`（业务共享 owner，见 `ui-owners`）→ `pages/<feature>/components/`（页内私有组件，仍须用 `@felinic/ui` 原子和 token）。
- 改共享层须先经负责人确认；改 `@felinic/ui` 在 `felinics/ui` 独立仓提 PR 后回主仓更新 submodule；跨页有益的间距/交互改善直接沉进 owner；新增业务 owner 同步登记 `ui-owners` 并补 `/dev/components` 示例。

**边界与保障：** 空间关系截然不同（`ui-owners` § 何时保留手写）或仅单页使用的块保留在页内。保证方式：**结构** + **机械检查**（拦截组件注入违规 class）+ **文字**。

### M3. 服务端数据只有一条访问路径

**适用场景：** 拉取或变更服务端数据、定义查询 key、管理缓存与流通道。

**契约与原因：** 统一走 `@memohai/sdk` + Pinia Colada（`useQuery` / `useMutation` + `invalidateQueries`），Pinia store 只存纯客户端状态。避免同一数据双源不同步；查询 key 必须包含全部影响结果的参数（TanStack Query / SWR / Colada 硬约束）。

**本仓机制：**
- 优先使用 SDK 生成的查询配置与 key 工厂（如 `getBotsQuery()`）；手写 `useQuery` key 形如 `['bot-settings', botId]`。
- 登录、登出与 401 时由 `store/user.ts`、`lib/api-client.ts` 清空内存与 `localStorage` 查询缓存（Bearer 与 401 统一在此处理）。
- 写操作在 `onSettled` 调 `queryCache.invalidateQueries`；保存后立即强刷须可见最新值时补调 `saveQueryCacheToDiskNow()`。
- 聊天数据唯一走专用长连接（WebSocket 传对话 + SSE 传列表/失效信号），收口于 `store/chat-list.ts`（`references/architecture.md` § 流式传输）。

**边界与保障：** 非后端 API 特殊传输（大文件进度上传、外部第三方 URL）可用专用通道，但错误处理须守 U3。保证方式：**机械检查**（ESLint 拦截非白名单裸 `fetch`）+ **文字**。

### M4. 视觉取值来自档位

**适用场景：** 编写颜色、字号、尺寸、间距、圆角、边框、阴影、透明度、z-index 或动效时长。

**契约与原因：** 所有视觉取值必须来自 Token/scale（`packages/ui/src/style.css`）；动效遵守高频交互 `≤ 150 ms`（Atlassian 规范）、退场短于进场、`> 600 ms` 仅用于非触发背景动画（MD3 规范，负责人于 2026-10-27 确认），确保深浅色一致与操作跟手。

**本仓机制：**
- 完整契约见 `packages/ui/AGENTS.md`；`scripts/check-ui-contract.mjs`（`mise run lint`）机械拦截裸色、任意圆角、手写阴影、文字关联 `px`、裸 `z-index`、斜杠透明度与组件样式覆盖，存量记录于 `scripts/ui-*-baseline.json`。
- **注意缺口**：脚本暂不拦截 `apps/web` 普通 `div`/`span` 上的裸色类名及 CSS 动效毫秒数，须自查深色模式与 DevTools Animations 面板。`@felinic/ui` 既有 255 ms 按钮弹簧回弹与 160 ms 开关松开保留至下次 UI 大改版，业务新写高频反馈一律压在 150 ms 内。

**边界与保障：** 组件可通过语义 Token 表达自身特征。保证方式：**机械检查**（UI 契约守卫）+ **文字**。

### M5. 修复根因

**适用场景：** 修复 bug、视觉错位、lint/测试失败，或处理外部数据、边界校验与兼容性。

**契约与原因：** 顺着调用链从根源修复，使同类问题一次绝迹；不靠下游默认兜底（`?? fallback`）掩盖上游缺陷或靠特判绕过检查。同时严格区分外部边界与内部契约，防止“防假设万一”的防御性样板传染扩散。

**本仓机制：**
- **一处根因集中修**：多处怪象并发时先找共同上游源头（`references/architecture.md` § 页面重构与交接自检流程）；跨模块前提用 TS 类型或测试锁死，锁不住的前提直接删掉。
- **守住系统边界，内部直接信任**：只在用户输入以及 `utils/`、`composables/api/` 专用 normalizer/adapter 中，对 `Record<string, unknown>`（如 `metadata`）、未校验输入、旧版服务端响应或旧格式做**一次**类型收窄、兼容转换（注释标明兼容目标）与 `.trim()`。进入 store、composable 与 `.vue` 组件后直接信任既有 TS 类型与内部 ID（如 `botId`、`sessionId`、`messageId`），不在内部跨层重复 `.trim()`、写冗余 `typeof` 兜底，也不在组件内直解裸 `metadata`。
- **纯客户端环境，禁写假 SSR 守卫**：`@memohai/web` 与 Desktop 渲染层均无 SSR。单测缺少 `window` / `document` / `localStorage` 时配置测试环境，不在业务代码里写 `typeof window === 'undefined'`。
- **禁掩耳盗铃修法**：不关全局后台刷新（违 U2）、不调大 `staleTime`（违 U4）、不用 `setTimeout` 遮闪烁（违 U5）；`ui-allow-*` 与 baseline 豁免须写明设计理由（`ui-allow-shape` 已机械校验）。

**边界与保障：** 根因跨越更大领域且超出授权范围时，允许在正确位置做最小安全止血，并在注释与交接中注明“临时止血方案”与根因立项。保证方式：**文字**（来源：用户确认）。

### M6. 保持分发边界

**适用场景：** 修改被 Web 与 Desktop 共用、或 OSS 与 Cloud 涉及边界的代码。

**契约与原因：** 多端共用代码只保留一份并可在任意宿主运行，宿主差异由外层启动注入；开源仓（`felinics/Memoh`）绝不混入 Cloud 专有逻辑或内部信息，避免分叉维护与 OSS→Cloud 同步冲突。

**本仓机制：**
- 路由唯一定义于 `src/routes.ts` `createAppRoutes('web' | 'desktop')`；Desktop（`apps/desktop`）复用 `@memohai/web` 模块，差异通过桌面引导或显式子路径导出实现，**绝不复制分叉 `apps/web` 页面**（见 `apps/desktop/AGENTS.md`）。
- 共享 store 消费宿主注入的 router（Desktop 使用 `createMemoryHistory`）；组件读取注入的 `DesktopShellKey`（不用 `isElectron` 或 `navigator.userAgent`）；`apps/web` 随时须能作为纯浏览器应用独立运行。

**边界与保障：** 通过正规注入开关（`DesktopShellKey`）做局部小适配完全合规。保证方式：**结构**（统一路由工厂与注入）+ **机械检查**（ESLint 禁共享 store 引 `@/router`）+ **文字**。

### M7. 性能有预算，失败可追溯

**适用场景：** 引入依赖、编写重型组件、长列表、定时器、高频输入处理或新增错误分支。

**契约与原因：** 交互与首屏加载遵守 Google Core Web Vitals p75 良好线（分端测量）：**INP ≤ 200 ms**、**LCP ≤ 2.5 s**、**CLS ≤ 0.1**、单次输入回调 **≤ 50 ms**（RAIL 模型留出 100 ms 绘帧）；所有错误链保留追踪根因的原始对象与 `request_id`。

**本仓机制：**
- 重型库动态 `import()`（如 `useShikiHighlighter` 中 `import('shiki')`）；`routes.ts` 全路由懒加载；`setInterval` 轮询监听 `document.hidden` 暂停并在卸载清理（`degraded-and-destructive.md`）；无限长列表必须分页或虚拟滚动；错误捕获与重抛保留原始 error（带服务端 `request_id`，见 `memoh-error-handling`）。
- **当前缺口**：未接入 `web-vitals`/`PerformanceObserver` 自动埋点、无构建体积硬报警、界面报错未显式亮出 `request_id`；改复杂交互前用 Performance 录帧并对比 `pnpm --filter @memohai/web build` 产物体积。

**边界与保障：** 按需拆分微计算确保不阻塞主线程。保证方式：**文字**（来源：用户确认；动态耗时无法静态测定）。

### M8. owner 保持可修改

**适用场景：** 扩展共享 owner prop/分支、处理超大页面/Store，或重构腐化组件与半截迁移目录。

**契约与原因：** 一个 owner 只负责一个清晰概念，靠不断堆布尔 prop/特判或跨层覆盖维持适配会让调用方不敢触碰、退回复制粘贴（M2）。同时避免往已过载的巨型文件继续堆状态，并在替换/合并后彻底收尾。

**本仓机制：**
- **腐化信号**：外层绑 class 覆盖样式（入 `scripts/ui-app-baseline.json`）、仅单一调用方使用的 prop、`variant`/`mode` 切出完全不同的两套 DOM 布局、改一行需测五六页（`@felinic/ui` 透传 Reka 属性除外）。
- **标准五步清理法**（每步单独开单提 PR，不混入业务 PR，守 D1）：`1. 冻结接口 → 2. 盘点分类（同概念变体 / 另一概念 / Token 数值 M4 / 历史包袱）→ 3. 拆解瘦身（标准入口配基础原子组合，新概念拆独立 owner）→ 4. 分批迁移（TS @deprecated + ui-owners 标注 + 基线拦截新增，老调用方分批或脚本迁走）→ 5. 彻底清理（清零后删旧代码与 baseline）`。
- **控制超大文件膨胀与迁移收口**：触碰 `chat-pane.vue`、`workspace-tabs.ts` 与千行级 `bot-*.vue` 等超大文件时，新增独立弹窗、协议解析或独立子状态机应拆成聚焦子组件或 composable；合并路由或替换页面后同步收拢跨目录遗留子组件（如 `pages/speech`、`pages/transcription`）并更新文档。

**边界与保障：** 同一概念的合理档位（如 `size`、`variant`）用 prop 表达；拆分后每块应能用一句话说清单一职责。保证方式：**文字** → 目标 **机械检查**（`@deprecated` 新调用基线拦截）与 `ui-owners` 状态标注（`[稳定]`/`[弃用中]`）。

---

## 3. 完成的定义

通用的提 PR、截图与 QA 披露遵循根目录 `AGENTS.md`，前端交接同时核对以下四条：

### D1. 只改动需求范围内的内容

**规则与保障：** Diff 仅包含完成当前需求必需的改动；重构须保持外部行为一致；顺路发现的无关老问题记入交接说明另行立项。因遵守 M2 沉入共享 owner 的必要联动在交接说明中报备。保证方式：**文字**（来源：用户确认）。

### D2. 逐项列出行为、外观和默认值的改动

**规则与保障：** 在交接说明与 PR 中逐条列出用户可见交互、外观、默认值、功能开关与新守卫的变化，防止漏审。保证方式：**文字**（来源：用户确认）。

### D3. 声明不超过证据

**规则与保障：** 报告严格匹配实测覆盖范围（`build`/单测不代表 UI 可用，单环境截图不代表深浅色/三语/窄屏通过，agent 自检不勾选真人 QA）。运行态启动、交互、截图与 QA 复选框遵循根目录 `AGENTS.md`（[Mandatory Runtime and UI Verification](../../../../AGENTS.md#mandatory-runtime-and-ui-verification)、[Pull Request QA Status](../../../../AGENTS.md#pull-request-qa-status)），页面重构逐项核对 `references/architecture.md` § 页面重构与交接自检流程 与 `ui-patterns`。保证方式：**文字**（来源：用户确认）。

### D4. 检查全部通过；每个豁免都写明理由

**规则与保障：** 交接前 `mise run lint`（ESLint + UI 契约守卫）、类型检查与相关测试全绿；所有 `eslint-disable`、`ui-allow-*` 与 baseline 条目须在代码旁写明不了解上下文也可信服的理由。保证方式：**机械检查**（`mise run lint`、`check-ui-contract.mjs` 强制豁免理由非空；来源：用户确认）。
