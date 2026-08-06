# Web 移动端壳 — 实施 Plan

> 读者:零上下文接手者。本文是**架构裁决 + 执行计划**;产品拍板过程见同目录
> `HANDOFF.md`(评估 → 三个产品问题 → 拍板 → worktree)。两文互补:HANDOFF 记"为什么做",
> 本文记"怎么做"。

| 项 | 值 |
|---|---|
| Worktree | `/Users/qqqqqf/Documents/Memoh-web-mobile-shell` |
| Branch | `feat/web-mobile-shell`(base `origin/main` @ `b8b694ceb`) |
| 范围 | OSS `apps/web`(+ 必要时 `packages/ui`);不碰 Memoh Cloud |
| 状态 | 架构已裁决,实现未开始 |
| 验证纪律 | AI 只做 lint/typecheck/build + 代码层 spike;渲染效果与 happy path 归人类 QA |

## 1. 产品硬约束(拍板,勿推翻)

- 手机一等公民;**双壳**:桌面 = 工作台多面板,手机 = 栈导航一屏一事。
- 手机禁并排多屏;二级能力(Files/Terminal/Browser/Display)全屏 + 返回。
- ≡ 开主导航(Sheet/全屏),主区 100% 聊天;桌面继续 PanelLeft 推拉。
- Settings 手机端 = 导航列表主界面 + 详情全屏 push;桌面仍双栏。
- 桌面零回归。

## 2. 架构裁决

### 2.1 切入层:main-section 内部分叉,共享 dock 实例

**否决了 App.vue 级分叉**(`mobile ? MobileShell : MainSection`):分叉会在跨越断点
(旋转屏、桌面拖窗)时卸载重建 dockview,终端 / WebRTC / 聊天滚动全丢;fromJSON 恢复
是为持久化设计的,不承担运行时换壳。且 desktop 与 web 共享 MainSection,而 Electron
窗口 `minWidth: 960`(`apps/desktop/src/main/index.ts:45`)——任何 `<768px` 分支在
desktop **物理上永不触发**,内部分叉对 desktop 零接触,无需 App.vue 级隔离。

目标形态(`pages/main-section/index.vue`,当前是 flex 行:SideBar + MainContainer,见
:10-15):

```
MainSection(v-if=isAppArea,持久挂载,挂载逻辑不动)
├─ <SideBar v-if="!isMobile">          桌面 rail;手机不渲染、不消费其持久化状态
└─ 右区(flex-1)
   ├─ <MobileTopBar v-if="isMobile">   新组件:左 ≡/← · 中 标题 · 右 +
   └─ <MainContainer>                  dock 宿主,两端共享同一 dockview 实例
```

桌面侧 DOM 变化只允许多一个布局中性的 wrapper(零回归由人类 QA 按 §5 清单确认)。

### 2.2 移动栈导航 = 单 group + 隐藏 tab 条 + 激活即选择

不发明第二个面板系统。给现有 dock 加**运行时约束**,全部 keyed 于单一 `isMobile`,
集中在 `store/workspace-tabs.ts` + `chat-workspace.vue`:

1. **split 否决**:`onWillDrop` / `onWillShowOverlay` 在 mobile 时 preventDefault 一切
   产生新 group 的拖拽。先例:终端组排他(workspace-tabs.ts:540-549)。
2. **程序化 split 收口**:`splitGroup` / `openFileToSide` / `openPreview` /
   `addTerminalPanel` / `openDisplayForAgentUse` 在 mobile 时忽略 direction,
   同 group addPanel + activate。
3. **hideHeader**:唯一 group 运行时隐藏 tab 条。sanitizeLayout 本就把 hideHeader
   从持久化剥除(workspace-tabs.ts:228-247),天然不污染布局存档。
   **这是 Phase 1 的第一个 spike**:验证唯一 group 无 header 时水印、测量
   (auto-resizing 已禁,自管 ResizeObserver,chat-workspace.vue:65-118)、激活、
   `ensureDraftChatPanel`(:1291-1321)行为正常。不行则回退"保留 header、精简移动
   菜单项",并上报后再定。
4. **进入 mobile 归并**:groups > 1 时并入第一 group(仅发生在桌面窗拖窄的边缘场景;
   手机 tab-scoped storage 按设备隔离,天然单 group)。
5. **布局持久化守卫**:mobile 时**不读不写** `workspace-layout`(tab-scoped,per bot,
   workspace-tabs.ts:49-74)——既不拿桌面多 group 布局反序列化,也不拿单 group 覆盖它。
   冷启动 = `ensureDraftChatPanel` + chat-selection 持久化的活跃会话重绑。
6. **"返回"语义**:顶栏 ← = 激活 chat 面板,**不关闭**二级面板(`renderer: 'always'`
   让终端 / WebRTC 后台保活,再进还在)。

不做的状态变更:`workspace-workbench-open` 等四个 localStorage 键
(workspace-tabs.ts:1966-2001)在 mobile **不消费也不改写**——rail 不渲染即天然关闭,
桌面偏好零接触。Mod+B(main-section/index.vue:61-65)mobile no-op。

### 2.3 ≡ 主导航 = Sheet left 复用现有面板

- 容器:`@felinic/ui` Sheet `side="left"`(SheetContent.vue;w-3/4 sm:max-w-sm,
  z `--z-overlay`;mobile nav 是其头注释预留用途)。生产先例:
  master-detail-sidebar-layout(bot detail)的 max-md 汉堡 + Sheet(:58-93)。
- 内容:bot-switcher(row 变体)+ 视图切换 + 现有 PanelSessions / PanelFiles /
  PanelSchedule 组件(桌面即 v-show 切换的纯 Vue 组件,sidebar/index.vue:111-126)
  + 底部 Settings 入口。权限门(如 Files 的 `canWorkspaceRead`)与桌面一致。
- **选中即关**(master-detail 的 Sheet 选中不自关是其已知缺口,此处不复制该行为)。
- 状态:组件内 ref,不持久化。不做 swipe-to-dismiss(reka Dialog 无此能力;
  留 Phase 3 议 vaul,引入新依赖需按惯例报备)。

### 2.4 断点唯一事实源

新建 `apps/web/src/composables/useIsMobile.ts`:`useMediaQuery('(width < 768px)')`,
与 Tailwind `md:`(≥768)严格互补。纪律:**JS 断点只用于"选哪个壳 / 导航形态";
页内微调继续用 CSS 前缀**。现状事实:聊天壳本体零断点;768 存于 ui SidebarProvider
(近死 API);1024 watcher 是死代码(Phase 2 清)。

**已拍板的边缘决策:Phase 1 只做 width-only。** 手机横屏(≥768px)落回桌面壳,
不劣于现状;`pointer: coarse` 组合判定(会波及 iPad 竖屏 768px)留 Phase 3 再议。

### 2.5 顶栏(MobileTopBar)

app 层 composition 组件(master-detail 汉堡是先例),不是新 ui 原语:

- 左槽:活动 panel 是 chat → ≡;是二级 panel → ←(激活 chat 面板)。
- 中:**跟会话标题走**(已拍板);无会话时回退 bot 名。
- 右:"+" 菜单(New Terminal / Open Browser / Open Desktop,权限门同桌面);
  活动为二级 panel 时附 Close panel。

### 2.6 iOS 键盘 = Phase 1 的 P0 验收项(已拍板)

`h-dvh` 不随 iOS 键盘收缩,全仓无 `visualViewport` 处理——composer 大概率被键盘遮挡,
不处理则手机聊天不可用、Phase 1 切片不成立。列为 P0 验收:若人类 QA 复现遮挡,
则在 Phase 1 内补 visualViewport 处理,达标才准交付。

## 3. Phase 1 — Chat 壳(一个 PR,垂直切片)

| # | 工作项 | 验收 |
|---|---|---|
| 1.1 | `useIsMobile()` composable | 唯一 JS 断点;改宽度实时翻转 |
| 1.2 | spike:hideHeader 单 group | 水印/测量/激活/draft 重生正常;否则回退并上报 |
| 1.3 | MainSection 分叉 + MobileTopBar | 桌面仅多中性 wrapper;← 回 chat 不杀二级面板 |
| 1.4 | 移动导航 Sheet(复用三面板 + bot 切换 + Settings) | 选中即关;权限门同桌面 |
| 1.5 | store 移动约束(§2.2 全部 6 条) | 手机不可 split;桌面布局存档不被读写;拖窗跨断点不崩 |
| 1.6 | Mod+B / PanelLeft / split 菜单项 mobile 失效 | 桌面快捷键与按钮零变化 |
| 1.7 | i18n en/zh/ja 三语新 key | 三语 locale 文件同改,引用处全解析 |

明确不做:设置区、safe-area、消息操作触屏化、横屏策略、swipe 手势、PWA。

## 4. Phase 2 — Settings 壳(一个 PR)

前提(调查结论):页内"列表 ↔ push 详情"已是成品(useViewSwap + SwapTransition +
DetailPane,六个集成页 + bot 三 tab 在用,URL query 镜像)。**Phase 2 只做壳层**:

- mobile 时 SettingsSidebar 升格为全屏列表首页(coreNavItems /
  integrationsNavItems / accountNavItems 已是纯数据 computed,
  settings-sidebar/index.vue:290-312);点项 → 内容全屏 push(SwapTransition 同族
  动件);返回用现有 `useBackAffordance`(composables/useBackOr.ts)。
  "列表首页"是壳状态不是路由,**路由表不动**;页内一层零改动。
- 清死账:main-layout 1024 死 watcher(layout/main-layout/index.vue:33-42)、
  ui SidebarProvider `openMobile`/`setOpenMobile` 死 API(全仓零消费者)、
  master-detail Sheet 选中不自关。
- `platform` 孤儿路由(在路由表但无 nav 入口、无跳转方):补 nav 还是删路由,
  开工时由人拍板。

## 5. Phase 3 — Polish(按项小 PR,按价值排序)

1. safe-area + `viewport-fit=cover`(index.html:6 当前无;顶栏/Sheet/composer)。
2. 消息操作条触屏可达(message-actions.vue:22-25 当前 hover-only,focus-within 兜底)。
3. PageShell gutter 阶梯(现固定 px-6;SettingsShell/DetailPane 已有 px-4 md:px-6)。
4. Sheet safe-area / overscroll-behavior / swipe(vaul 决策)。
5. Display 触摸输入转发(现纯鼠标键盘)、灯箱触摸手势。
6. 横屏断点策略(§2.4)。

## 6. 验收与 QA

**Phase 1 桌面零回归清单(人类 QA)**:rail 开合 / 拖拽宽度 / Mod+B / PanelLeft、
split 右/下、tab 拖拽与右键菜单、布局刷新恢复、settings 滑入滑出、desktop 壳。

**Phase 1 手机清单(人类 QA)**:开导航 / 选会话 / 新建会话 / 开终端 / 浏览器 /
Display、← 返回、终端后台保活、刷新后会话重绑、**iOS 键盘不遮 composer(P0)**。

新 UI 先在 dev 组件墙验证视觉;`mise run lint` 过 UI 合同守卫;新文案三语(en/zh/ja);
注释只写 why、不提外部产品名。

## 7. 关键代码事实速查(调查证据,行号以 base b8b694ceb 为准)

- 挂载链:App.vue:25-27(isAppArea = chat ∪ settings)、:70-78(settings fixed 层
  visibility 切换,不 v-if 的原因在注释);chat 路由是 null stub(router.ts:27-45)。
- 侧栏状态机:workspace-tabs.ts:1966-2001;`workspace-sidebar-open` 是只写不读的死键。
- rail 推拉:sidebar/index.vue:200-209(marginLeft);resize 只有 mousedown(:147-148)。
- 折叠入口:dock 首组 tab 条 PanelLeft 按钮(dockview/prefix-header-actions.vue:11-31)
  + Mod+B(main-section/index.vue:61-65)。
- dockview 配置:chat-workspace.vue:7-23(disable-floating-groups / tabs-overflow-list /
  auto-resizing 全 true);panel 8 型;`renderer: 'always'`;chat 面板多实例 + ephemeral
  预览槽(workspace-tabs.ts:81-95);dock 激活 ↔ 全局选择双向同步(:481-484, 2180-2206)。
- Settings:settings-section/index.vue:96-104(整体滑入滑出,已是 iOS push 手感);
  SettingsSidebar `collapsible="none"` 静态渲染 → 1024 watcher 是死代码。
- 触控底子:dockview-core 全 Pointer Events;useChatScroll 有触摸物理;reka
  ContextMenuTrigger 内置长按;会话 rename/delete 有 DropdownMenu 兜底。
- 缺口(全仓 grep 为零):safe-area / viewport-fit / visualViewport / pointer:coarse。

## 8. 未确认项(实现期保持警觉)

- dockview 唯一 group hideHeader 的运行时行为(spike 1.2)。
- Sheet 内嵌入 PanelFiles 的适配成本(files-pane 内部未深读)。
- chat 多 tab + ephemeral 预览槽在隐藏 header 下的交互(单击复用 preview tab 的逻辑
  不依赖 header 可见性,待 spike 确认)。
- iOS 键盘遮挡的严重程度(人类 QA 实测;若 scroll-into-view 恰好够用则降级处理)。

---

*架构裁决日期 2026-08-03;证据来自三路只读代码调查(聊天壳 / 设置壳 / 移动基础设施)。
实现偏差记在 PROGRESS 或 PR 描述,不改本文的历史裁决;方向变更先改本文再动工。*
