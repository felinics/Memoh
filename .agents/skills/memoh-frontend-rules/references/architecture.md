# Web 前端架构与样板页地图

`apps/web` 与 Desktop 渲染层的目录结构、认证样板参考页、三层常驻布局、状态分工与
流式传输契约。

## 目录

- [技术栈与模块边界](#技术栈与模块边界), L:20
- [目录地图](#目录地图), L:39
- [认证样板参考页（抄代码只抄这些）](#认证样板参考页抄代码只抄这些), L:66
- [宿主页面局部原语（Surface-scoped owners）](#宿主页面局部原语surface-scoped-owners), L:79
- [页面重构与交接自检流程](#页面重构与交接自检流程), L:106
- [路由与三层常驻布局](#路由与三层常驻布局), L:117
- [数据层与客户端 Store 分工](#数据层与客户端-store-分工), L:145
- [流式传输（WebSocket 与活动 SSE）](#流式传输websocket-与活动-sse), L:167
- [工作区依赖操作契约](#工作区依赖操作契约), L:182
- [工作区面板、远程桌面与 Browser / Computer Use](#工作区面板远程桌面与-browser--computer-use), L:189
- [本地开发与路径别名](#本地开发与路径别名), L:196

## 技术栈与模块边界

`@memohai/web`（`apps/web`）是 Memoh 的管理与聊天前端，使用 Vue 3（`<script setup>`）+ Vite 8 构建。
在服务端部署模式下，它作为独立浏览器应用运行；原生桌面客户端（`apps/desktop`）直接复用 `@memohai/web`
导出的页面、store、路由工厂、i18n 和 API 客户端，自身只负责 Electron 窗口、托盘、菜单和预加载 IPC。
任何时候都必须保证 `apps/web` 能作为纯浏览器应用独立运行（M6）。

| 领域 | 技术选型 |
|---|---|
| 框架与构建 | Vue 3（Composition API）+ Vite 8 + TypeScript 严格模式 |
| 样式与组件库 | Tailwind CSS 4（纯 CSS 配置，无 `tailwind.config.*`）+ `@felinic/ui`（基于 Reka UI，严禁业务直接引 Reka UI） |
| 服务端状态 | Pinia Colada（`@pinia/colada`）+ 自动生成的 `@memohai/sdk` |
| 客户端状态 | Pinia 3 + `pinia-plugin-persistedstate` / `@vueuse/core` `useStorage` |
| 表单校验 | vee-validate + `@vee-validate/zod` + Zod |
| 国际化 | vue-i18n（`en`、`zh`、`ja` 三语齐全） |
| 图标 | 通用 UI 图标用 `lucide-vue-next`；模型/渠道品牌图标用 `@memohai/icon`（严禁 FontAwesome） |
| 通知提示 | `@felinic/ui` 内置 `toast` / `Toaster`（样式已包含在 `@felinic/ui/style.css` 中） |
| 富内容与终端 | Markdown：`markstream-vue` + Shiki + Mermaid + KaTeX；图表：ECharts；终端：`@xterm/xterm`；编辑器：Monaco |

## 目录地图

以下路径均相对于 `apps/web/src/`：

- **入口与基础设施**：`main.ts`（插件挂载、API 客户端初始化）、`App.vue`（常驻聊天层 + 设置层 + `Toaster`）、
  `routes.ts`（Web 与 Desktop 共用的唯一业务路由工厂）、`router.ts`（Web 浏览器路由与鉴权守卫）、
  `lib/api-client.ts`（`VITE_API_URL`、Bearer 注入、401 统一清缓存跳登录）、`lib/query-cache-persistence.ts`
  （查询缓存落盘与密钥剥离）。
- **页面（`pages/`）**：页面专属子组件放 `pages/<feature>/components/`，专属逻辑放 `pages/<feature>/composables/`：
  - `main-section/` 与 `home/`：聊天与多面板工作区（`chat-workspace.vue` 承载 dockview 分屏：单实例 `chat` 面板，
    以及 `file`、`terminal`、`browser`、`display` 面板）。
  - `settings-section/`：设置全屏层（带 `SettingsSidebar` 与 `<KeepAlive>`）。
  - `bots/`：Bot 列表、创建向导与创建进度流（`new-progress.vue`）、多 Tab 详情页（概览、设置、桌面、渠道、记忆、
    MCP、定时任务、容器、网络、工具审批、技能、访问控制、依赖与 App）。
  - 其他设置页：`providers/`（服务商总页，通过 `?tab=` 切换 `models-panel.vue`、`web-search/`、`voice/`、`video/` 四个子范围面板；旧路径 `/settings/web-search`、`/settings/voice`、`/settings/video`、`/settings/speech`、`/settings/transcription` 均为重定向）、`runtimes/`（远程运行时与计算节点 Computers）、`supermarket/`、`usage/`、`people/`、`appearance/`、`keyboard-shortcuts/`、`profile/`（Account）、`about/`。独立 `/settings/memory` 已移除并入 Bot 详情的记忆 Tab（旧路径重定向至 `/settings/bots`）。
  - 独立全屏页：`login/`、`onboarding/`、`oauth/`、`providers/connect.vue`（通用 Provider 深度链接导入确认页）、
    `dev/components/`（开发组件墙）。
- **业务共享组件（`components/`）**：跨两个以上页面复用的业务 owner（如 `sidebar/`、`master-detail-sidebar-layout/`、
  `file-manager/`、`terminal/`、`monaco-editor/`、`loading-button/`、`form-dialog-shell/` 等）。选型前先查
  `packages/ui/skills/ui-owners/SKILL.md`。
- **组合式函数（`composables/`）**：接口与流封装在 `composables/api/`（`useChat.*`、`sse-error.ts`、
  `useContainerStream.ts`、`useWorkspaceDependencyStream.ts`）；通用交互封装在顶层（`use-server-synced-form.ts`、
  `useDialogMutation.ts`、`useRetryingStream.ts`、`useSyncedQueryParam.ts`、`useViewSwap.ts`）。
- **客户端状态（`store/`）**：只放纯客户端状态（见下方 Store 分工表）。
- **多语言与工具（`i18n/locales/`、`utils/`）**：`en.json`、`zh.json`、`ja.json`；错误解析 `utils/api-error.ts`（`resolveApiErrorMessage` 按 `errors.<code>` → HTTP 状态码兜底 → 流错误事件顺序取本地化文案，绝不直接向用户暴露服务端原始 `message`/`detail`）、
  时间格式化 `utils/date-time.ts`。

## 认证样板参考页（抄代码只抄这些）

仓库老页面良莠不齐，动笔写新页面或重构前，只准参照下面整理干净的样板页：

| 你要写的页面类型 | 标准参考样板页 | 反面教材（严禁照抄） |
|---|---|---|
| 内容精简、只有几张卡片的页面 | `pages/about/index.vue`（居中分组 + 底部元信息） | — |
| 设置页（带标题的分组卡片 + 行列表） | `pages/bots/components/bot-overview.vue`、`pages/usage/index.vue` | `pages/bots/components/bot-tool-approval.vue`（堆砌底色、裸透明度、自造指标卡的典型脏页面） |
| 列表与详情左右推拉切换 | `pages/web-search/index.vue`（`useViewSwap` + `SwapTransition` + `BackendCard` + `DetailPane`） | — |
| 带统计数值块与图表的概览页 | `pages/bots/components/bot-overview.vue`（细线分隔数值块 + 黑白灰 ECharts） | `bot-tool-approval.vue` |
| 表单或新建/编辑弹窗 | `pages/bots/components/bot-schedule.vue`（新建任务弹窗：`DialogTitle`、字段间距、折叠更多选项、底部按钮排布） | — |
| 全部原子组件与业务 owner 活体目录 | `pages/dev/components/`（开发模式下设置 `localStorage.setItem('memoh:dev-tools', '1')`，按 `Cmd/Ctrl+Shift+D` 打开） | — |

## 宿主页面局部原语（Surface-scoped owners）

跨领域共享的原语全部位于 `@felinic/ui`（字典见 `packages/ui/skills/ui-owners/SKILL.md`）。以下 7 组原语只服务于
`apps/web` 的特定功能面，就近存放在对应业务目录中；修改相关功能面时必须直接组合它们，不得手写同形结构：

- **初始引导（`pages/onboarding/components/`）**：
  - `step-exit-shell.vue`：向导退出动画的唯一外壳（统一管理缩放+透明度退场过渡，每个 `Step*.vue` 模板根节点都包裹它；位于 `StepFrame` 外层以便 `Step3` 的三种框架变体共用同一外壳）。
  - `step-frame.vue`：`35rem` 正文宽度与标题骨架（各步标题下边距通过 `titleClass` 按页面独立微调，勿抹平；`bodyClass` 仅用于 `Step3` 模式切换过渡）。
  - `footer-nav.vue`：上一步/下一步按钮组（下一步设最小宽度随长文案自适应；`#next` 插槽仅用于需要带 `key` 的 `Transition` 切换文案的按钮）。
  - `choice-tile.vue`：`h-16` 图标+标签网格磁贴（支持实线/虚线）。
  - `hint-box.vue`：单行 `text-xs` 表单提示（`muted`/`warning` 语气，可选 `#icon`；不等同于页面级的 `CalloutBanner`，不要强行合并）。
- **应用侧边栏（`components/sidebar/`）**：
  - `panel-header.vue`：侧栏分组小标题行（统一收口 `files-pane`/`schedule`/`sessions` 共享的 `text-xs` 标题字号与字重，外边距、高度和缩进列对齐由调用方控制；`recents` 的标题是可交互下拉菜单 `TextButton`，空间关系不同，保持局部手写）。
  - `nav-button.vue`：`h-9` 图标列动作行（`active` 控制选中高亮，调用方传入 `size-[18px]` 线宽 `1.75` 的图标）。
  - *刻意不合并不等距行*：`session-item`、`bot-switcher`、`search-dialog` 的行高、圆角和悬停 Token（`sidebar-hover` vs `bot-row-tint` vs `ui-hover`）有意区分，且 `bot-switcher` 因接入 `sortablejs` 必须保持裸 `div`，不强抽通用 `ListRow`。
- **应用市场（`pages/supermarket/components/`）**：
  - `market-item-card.vue`（列表卡片：前导图标盒 + 标题 + 主页链接 + `#actions`）、`market-detail-header.vue`（详情页头部：返回/安装动作行 + `#icon` 盒 + 标题 + 标签徽标）、`info-item.vue`（详情页信息区的上标签下数值条目）。
- **编辑器文件对比（`components/diff-title-bar/index.vue`）**：
  - `DiffTitleBar`：Monaco Diff 上方的标题条，仅管理容器布局、细线、内边距与静音字色，内容走默认插槽与可选 `#actions`（目前由文件对比与 Monaco Diff 两处复用；若第三处需要不同容器应局部实现或拆分，不要继续往里加 prop）。
- **Dockview 分屏面板外壳（`pages/home/components/dockview/`）**：
  - `panel-frame.vue`（`DockPanelFrame`）：所有 `panel-*.vue` 根节点的统一外壳，封装 `relative flex h-full w-full flex-col` 与 `min-h-0 flex-1` 内容层；编辑器底色面板（文件/预览/资源）传 `editorSurface`；顶部面包屑条走 `#header`。新建 dock 面板必须组合它，严禁手写外层 flex 溢出容器。
- **聊天输入框上方停靠区（`pages/home/components/`）**：
  - 聊天输入框上方的全部停靠表面均由三层结构接管：`composer-dock.vue`（`ComposerDock` 编排器：管理底部主槽互斥——默认显示输入框、Agent 提问时替换为 `ask_user` 胶囊，以及背景遮罩几何尺寸与显隐高度）、`composer-panel.vue`（`ComposerPanel` 堆叠层：把审批、命令结果、发送报错作为同一个胶囊内以细线分隔的多个分区）、`composer-capsule.vue`（`ComposerCapsule`：唯一的胶囊外壳，独占输入框同款描边、`surface-composer` 底色与圆角）。
  - 新增**堆叠层（Stack-tier）**成员（贴在输入框上方的决策/状态条）：只需新建只写内容的 `composer-panel-*.vue` 并在 `ComposerPanel` 的 `sections` 计算属性里注册一个分支；新增**主槽互斥层（Box-tier）**成员（替代输入框本身的胶囊）：扩展 `ComposerDock` 的主槽互斥与高度逻辑，不进 `sections`。严禁在输入框上方手写胶囊外壳、遮罩测量或零散 `v-if` 堆叠。
- **聊天工具调用详情（`pages/home/components/tool-detail/`）**：
  - `empty-row.vue`、`preview-box.vue`（`max-h-48`）、`header-row.vue`（可折叠行，支持语气轴与按钮套按钮的 `nested` 模式）、`expand-chevron.vue`、`capsule.vue`（紧凑/详情双密度）。新增工具详情面板前先读各文件头部注释中的局部例外说明。

## 页面重构与交接自检流程

新建或重构业务页面时，按以下 6 步推进（跨组件视觉与交互细节见 `packages/ui/skills/ui-patterns/SKILL.md`）：

1. **先读规范与认证样板页**：阅读 `packages/ui/AGENTS.md`、`packages/ui/skills/ui-owners/SKILL.md`、以及 `packages/ui/skills/ui-patterns/SKILL.md` 中与当前页面对应的参考篇目，并打开上方表格中最相近的认证样板页对照。
2. **盘点旧页面全部行为与进出路径**：动笔前把旧页面的每个 `v-model`、点击事件、加载/空状态分支、表单校验、快捷键和路由进出路径列成回归清单；重构后逐条核销，绝不静默丢功能。
3. **先画字符线框图再写标签**：在草稿中画出页面外壳、分组卡片、行与控件层级，砍掉“卡套卡”（带框大卡套带框小卡）、卡片内装饰图标和无意义的嵌套层。
4. **组合原语重建页面**：从 `<PageShell>` 往下用 `SettingsSection`、`SettingsRow`、`FieldStack`、`DialogPanel` 等原语拼装，绝不手写同形 `div`。
5. **排查未导入的 `@felinic/ui` PascalCase 标签退化（高频隐蔽故障）**：Vue 会把忘记在 `<script setup>` 中 `import` 的 PascalCase 标签静默回退为浏览器原生同名标签或未知元素——例如未导入的 `<Progress>` 会直接退化成操作系统原生的粗灰条 `<progress>`，`model-value` 完全失效，而 ESLint 和 `vue-tsc` 均不报错。交付前必须逐个核对模板里用到的 `@felinic/ui` 组件是否都已在 `import { ... } from '@felinic/ui'` 中显式导入，并在真实运行页面中确认像素级外观。
6. **执行 lint 与实机多态检查**：运行 `mise run lint`（含 `scripts/check-ui-contract.mjs` 守卫检查），并在运行态下走完空状态、加载态、深色模式、最窄分屏宽度与 `zh`/`en`/`ja` 三语检查。

## 路由与三层常驻布局

业务路由只在 `src/routes.ts` 中通过 `createAppRoutes('web' | 'desktop')` 定义一次；路由实例创建与宿主专属守卫留在
Web 和 Desktop 各自的入口里。

`App.vue` 把整个界面拆成互不干扰的三层，**绝不能把聊天区塞回普通 `<RouterView>`**：

1. **常驻聊天层（`MainSection`）**：只要当前路由属于聊天（`/`、`/bot/:botName?`）**或**设置（`/settings/*`），
   `MainSection` 就一直保持全尺寸挂载在底层。聊天路由在 `routes.ts` 里只注册空占位（null stub）。这样从聊天切去
   设置再切回来时，dockview 的分屏结构、终端连接和消息滚动位置完全不会卸载重排（Vue 的 `KeepAlive` 保不住
   dockview 布局，卸载必闪屏，见 U5）。
2. **设置覆盖层（`pages/settings-section/`）**：全屏固定层，靠可见性切换盖在聊天层上方；离开设置页时会等离场过渡
   `after-leave` 播完才放行路由守卫。内部用 `MainLayout` + `SettingsSidebar` + `<KeepAlive>` 包裹设置子页面；浏览器空闲时由 `prefetchSettingsPages()` 逐个串行预热设置页分包。
   Web 端直接访问 `/settings` 会展示移动端设置导航列表；Desktop 端会重定向到 `/settings/bots`。
3. **独立全屏页（`/login`、`/onboarding`、`/oauth/*`、`/dev/*`）**：处于这些路由时，上面两层都不挂载，由顶层
   `<RouterView>` 单独全屏渲染。

**路由守卫规则**：除 `/login`、`/oauth/*` 和 `/providers/connect` 外均需本地 token；未完成初始引导的登录用户自动跳 `/onboarding`（浏览器按账号在 `store/user.ts` 记住已确认完成的引导记录，在 `store/chat-selection.ts` 记住上次打开的 Bot，因此老账号冷启动或重新登录时**不在首屏渲染前等待 `/users/me` 或 Bot 列表请求**，直接放行进入对话并在后台校验）；带
`meta.adminOnly` 的路由（如 `/settings/people`）要求 `user.role === 'admin'`；动态分包加载失败（chunk load error）
自动触发页面刷新以拉取新版本静态资源。

**对话输入框首帧直出契约（Composer-First Entry，Web / 移动端 Web / Desktop 共用）**：
- **首帧即渲染且可立即输入**：任何进入对话的路径（冷启动、刷新、新建对话、点击历史会话、切换 Bot、深链跳转）都在第一帧画出输入框并接受打字、粘贴与全局失焦打字捕获（`useUnfocusedComposerInput`）。**严禁用任何网络或 store 加载状态（当前用户、Bot 列表、会话列表、会话摘要、历史消息、运行时快照、模型目录、WebSocket）控制输入框的挂载、显隐或 `disabled` 状态**。
- **加载期间只拦发送，不拦输入**：前置数据未就绪时由 `handleSend` 暂缓发送；仍在加载中的前置项（如模型偏好）显示为“加载中”，绝不误报成错误或“未选择模型”。
- **仅两个未知目标 Bot 例外允许等待一次请求**：当前浏览器从未登录过的新账号首次进入（需拉取 Bot 列表），或 URL 指向本标签页未记住的另一个 Bot（需查找该 Bot）；等待期间绝不显示旧 Bot 的输入框，请求失败时展示错误与重试。
- **位置从首帧起确定，移动绝不丢内容**：居中欢迎态还是底部停靠态仅由本地同步状态决定（冷启动默认停在新对话，桌面端按本地同步恢复的 dockview 标签决定），首帧之后只有用户自己的操作（如发出第一条消息）才能移动输入框；位置变化时已输入的文字、光标、附件与焦点完整保留，后台工作绝不切换用户正在打字的标签页。
- **桌面端自动聚焦，手机端由用户决定键盘**：桌面端主动进入对话时自动聚焦输入框（从菜单或弹窗进入时等浮层关闭归还焦点后再聚焦）；手机端绝不自动聚焦、不自动弹出软键盘。

## 数据层与客户端 Store 分工

服务端数据一律走 `@memohai/sdk` + Pinia Colada（U4、M3）：
- 首次无数据时的骨架屏一律绑 `isPending`；`isLoading` 在每次后台静默刷新时也会变 `true`，只能做轻量刷新指示，绝不能拿来藏正文。
- 拿查询数据喂给表单时一律包一层 `useServerSyncedRecord` / `useServerSyncedScalar`（U2）。

Pinia store 只管纯客户端状态：

| Store 名称 | 职责边界 |
|---|---|
| `user` | 登录态 token、当前用户信息；登录、登出及收到 401 时负责清空内存与 localStorage 查询缓存 |
| `settings` | 主题（浅色/深色，联动 `useColorMode`）、界面语言（`en`/`zh`/`ja`）与排版字号档位 |
| `capabilities` | 从 `getPing()` 初始化一次的服务端能力标志（容器后端类型、快照支持等） |
| `chat-selection` | 当前选中的 Bot ID 与会话 ID（持久化到 localStorage） |
| `chat-list` | 聊天消息投影、会话列表实时排序、WebSocket 与活动 SSE 事件归并（唯一允许独立管理流数据的 store） |
| `workspace-tabs` | Dockview 面板句柄、各 Bot 分屏布局持久化、左侧活动栏开关与宽度（严禁后台事件调激活函数抢焦点，见 U7） |
| `display-snapshots` | 各 Bot/会话最新的远程桌面预览截图缓存 |
| `bot-create-progress` | 创建 Bot 时的 SSE 进度流与断点恢复状态 |
| `app-operations` / `dependency-operations` | 工作区 App 与依赖安装/更新/移除的后台 SSE 任务流状态 |
| `keyboard-shortcuts` / `workdirs` | 全局快捷键绑定状态、Bot 工作目录状态 |
| `update` | 桌面端与应用更新提示状态 |

## 流式传输（WebSocket 与活动 SSE）

聊天是全站唯一不走普通 REST 查询缓存的数据流，严格拆成两条各司其职的通道：

1. **实时对话轮次走 WebSocket（`/bots/{bot_id}/local/ws`）**
   - 封装在 `composables/api/useChat.ws.ts`，支持发送、中止（abort）和断线重连。
   - **没有单会话 SSE**：某个会话的消息流和运行态全部来自该会话运行时的 WebSocket，保证当前标签页、其他标签页和其他设备读到的是同一份实时投影。

2. **Bot 级会话动态与缓存失效走 SSE（`GET /bots/{bot_id}/sessions/events`）**
   - 只传轻量级事件 ID 和缓存失效信号，**绝不传输聊天正文**（由 `useRetryingStream` 驱动指数退避重连）。
   - **首帧握手（`activity_ready { cache_invalidation }`）**：连上后立刻下发。`cache_invalidation: true` 代表服务端承诺对每一次运行接纳和历史删除都推 `session_invalidated`；如果是 `false` 或连的是老服务端没发首帧，代表没有失效覆盖保障，切回任何旧会话都必须重新拉取。
   - **失效与溢出事件**：收到 `session_invalidated { session_id }` 时把对应会话（为空则该 Bot 下全部会话）标为过期；收到 `dropped`（服务端订阅缓冲区溢出）时保守地把全部会话标为过期。
   - **后台任务触达**：`session_touched` 带 `reason: background_task` 时，对已加载的会话走增量归并路径刷新持久化消息（合并连续通知并在收尾补拉一次），避免冲掉正在流式输出的内容。
   - **乐观秒开契约（Optimistic revisit）**：切走再切回某个已缓存会话时，**只有同时满足“首帧声明支持失效通知 + SSE 连接未断过 + 没出过 `dropped` + 该会话没收到过 `session_invalidated`”四个条件**，才允许第一帧直接渲染本地缓存记录；只要中途断过线，覆盖期立刻作废（客户端不设宽限窗口）。

## 工作区依赖操作契约

- 安装、更新、重装、卸载工作区依赖前，必须先预览即将执行的脚本并经用户明确确认；确认请求和后续重试都必须携带预览时的 `definition_revision`，重试绝不能偷偷换成新版脚本。
- 聊天里提示“缺少某外部 Agent 依赖”时，绝不能自动开装；跳转管理链接只带上目标依赖 ID 和来源 `session_id`，只有用户确认安装该依赖时才把 `session_id` 传给通知，其他依赖不得继承该会话关联。
- 操作相关的共享 store 一律使用宿主注入的 router（兼容 Desktop 内存路由），严禁直接 import Web 端独立 router（M6）。
- 连接中断或返回 `workspace_dependency_operation_unknown` 时属于结果未知状态：在服务端对账确认真实结果前，绝不展示“重试”按钮（U3）。完整日志放在供用户主动滚动查看的区域，读屏 live region 只播报简短的阶段变化。

## 工作区面板、远程桌面与 Browser / Computer Use

- 中央工作区由 `store/workspace-tabs.ts` 驱动 dockview 分屏，支持 `chat`（全局单例，内容跟随当前选中会话）、`file`、`terminal`、`browser`、`display` 五种面板（均开启 `renderer: 'always'`，切 tab 不销毁终端、iframe 与 WebRTC 连接）；工作区文件树固定在左侧边栏（`components/sidebar/files-pane.vue`），不进 dock。
- `terminal` 和 `file` 是常规工作区功能；`display`（远程桌面）仅在容器工作区下可用，当 Bot 绑定远程运行时（remote runtime）时隐藏。
- `display-pane.vue` 连接的是带真实图形界面和浏览器的有头容器桌面（WebRTC 视频流 + 键鼠转发 + 预览截图），`bot-desktop.vue` 负责开关桌面服务与查看活跃会话。
- Agent 的 Browser Use（`browser_action` / `browser_observe` / `browser_remote_session`）操控的是上面那台有头容器里的 Chrome 实例；Computer Use（`computer_observe` 调 `a11y-cli` 无障碍树或落盘截图，`computer_action` 按引用或 RFB 坐标点击输入）操控的是整个有头桌面。写文案或注释时绝不要把它们说成“无头 Playwright”（无头 Playwright 只是容器里普通命令行跑的脚本，跟这套桌面栈无关）。

## 本地开发与路径别名

- Vite 开发端口默认 `8082`（读 `config.toml`，可通过 `MEMOH_CONFIG_PATH` / `CONFIG_PATH` 指定），`/api` 代理到后端（默认 `http://localhost:8080`）。多 worktree 联调先读 `memoh-local-dev` skill。
- 路径别名：`@` 指向 `apps/web/src`，`#` 指向 `packages/ui/src`。
