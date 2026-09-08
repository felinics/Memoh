# Memoh UI 工作交接 — 2026-09-08

## 工作位置与恢复边界

- 应用 worktree：`/Users/qqqqqf/Documents/Memoh-ui-live-preview`。
- 应用分支：`codex/ui-live-preview`。
- UI 子模块目录：`/Users/qqqqqf/Documents/Memoh-ui-live-preview/packages/ui`。
- UI 分支：`codex/ui-design-checkpoint`；本轮 UI 提交为 `2a8a7ba`，主仓库 gitlink 指向它。
- 本文随本轮应用提交保存；应用的准确提交 ID 以包含本文的 Git commit 为准。
- 本地预览：`http://localhost:18083`，Bot 示例 `/bot/111`，设置示例 `/settings/bots/111?tab=desktop`。
- 前端指向已有 OSS 后端 `http://localhost:18080`。只恢复前端，不重启或改占后端槽位。
- 主目录 `/Users/qqqqqf/Documents/Memoh` 属于其他工作；独立 `Memoh-prompt-motion` worktree / `codex/prompt-motion` / 18085 也不属于这轮交付。

## PR 与保存状态

记录时两个 PR 都是 Open Draft：

| 仓库 | PR | 远端 head（本次提交前核实） |
| --- | --- | --- |
| Memoh | https://github.com/felinics/Memoh/pull/1177 | `efb0f6821a575d883fa0de50064176f9e9615f57` |
| UI | https://github.com/felinics/ui/pull/19 | `4f199290df1f1a00478109a3631dc695d88a2406` |

本次用户要求提交，未要求推送或合并。因此本轮只创建本地提交，PR 尚不包含全部最新代码。此前本地 checkpoint `7768dec6d`（Memoh）和 `6015560`（UI）也尚未推送。以后若授权推送，先推 UI 子模块分支，确保 gitlink 对应 commit 在远端可获取，再推主仓库分支；不要仅推主仓库。

## 这轮总体改动

### 电脑选择与管理

- 精简 Run on、无其他电脑提示等文案与菜单元素。
- 改用显示器、显示器加云的共享图标。
- 最终菜单保留 Cloud Computer 与 Manage computers；较重的介绍卡片试验已撤回。
- 管理 dialog 展示可访问电脑及状态，提供 Add computer 进入既有连接流程；没有实现另一套连接后端。

### 菜单、Picker 与动画

- 统一 Select、DropdownMenu、ContextMenu 的滚动容器和行布局；模型虚拟列表仍保留自己的虚拟化逻辑。
- 修子菜单首行对齐、鼠标过渡相关行为，补根右键菜单动画。
- 统一设置页 Default Agent、Web Search、Web Fetch、Memory Provider 等选择器的对齐方向。
- 调整菜单基础宽度、分割线与 hover 的间距、分割线内缩和颜色；降低 dropdown 阴影。
- 顶部 Terminal / Browser / Desktop 与 Split 菜单、Recents 右键菜单一起参与验证。

### 图标职责与工具栏层次

- `@memohai/icon/ui` 管理界面图标的几何和默认笔画（1.75）。菜单组件管理图标槽尺寸与文字间距。
- Button 的 ghost `tone="muted"` 承接较淡的工具栏 action；菜单身份图标保留更清晰的层次。
- Composer 和顶部工作区的加号、电脑、侧栏开关、前进后退等接入共享图标；顶部加号使用按钮默认尺寸。
- Recents 的 Open 使用共享 OpenInTabIcon；增加共享图标与调用方样式约束测试。
- 这不是全应用图标迁移，也没有完成系统性的文字和主题颜色重调。

### 远程 Connector 图标

- 原方案只用独立 Image 预加载，但显示组件仍创建新的远程 img；快速切换时已抓到部分图片首帧 complete=false、naturalWidth=0，约 200–600ms 后 load。
- 曾尝试将固定品牌图标打包并按 URL 替换；用户指出绕过通用问题，该方案已完整撤销，无新增品牌资源或 URL 特判。
- 最终在 ProviderIcon 的共享 URL 缓存中保存图片数据，预加载和渲染复用同一结果；使用解码完成的 data URL，避免重新挂载时再次获取远程图片。
- 缓存最多 128 项，每项仅缓存不超过 512 KiB 的 image 响应；并发同 URL 共享结果。淘汰不使已挂载消费者失效。
- 跨域读取失败、解码失败及不适合缓存的响应回退原始 URL，后续挂载允许重试。这些回退地址仍受浏览器缓存限制；首次获取也并非无需等待。
- `VITE_MOCK_CONNECTORS=1` 仅用于 DEV 的目录 fixture；不要把这些 UI 验证当作真实 OAuth 连接通过。

### 深色卡片边界

- SettingsSection、MetricReadout、Table、ActionCard、BackendCard 的普通深色外壳去掉描边，嵌套边界保留 bordered 入口。
- Workspace 无指标及生命周期提示、Desktop Live view、平台和 MCP 详情头、Bot 侧栏身份卡、平台和 MCP 空状态改为复用 SettingsSection。
- 平台空状态也从虚线框改为普通卡片；添加按钮虚线、输入框、表格内部线、弹窗内必要分组仍保留。
- 组件库底层 Surface 收敛尚未实施。详细原因、遗漏过程与后续建议见 `ui-card-boundary-diagnosis-2026-09-08.md`。

## 验证及未完成事项

本次提交准备阶段：

- 全量 `pnpm run lint` 通过：0 errors，2 条 model-options.test.ts 的 prop 类型警告。
- `preload.test.ts`、`ui-icon-contract.test.ts`、`useConnectorLogos.test.ts` 共 10 个测试通过。
- `node scripts/check-ui-contract.mjs` 通过，保留 3 条已有警告；`git diff --check` 通过。
- 实际 Desktop 路由验证浅色边框 1px、深色 0px、画面 4:3，真实桌面内容正常显示。
- 图标浏览器检查记录到 50 次图片挂载，均使用共享数据，未出现首帧未完成或自然宽度为 0；包含菜单重新打开。此计数是图片节点数，不是 50 轮操作。
- 平台/MCP 详情头及空状态没有逐页完成视觉交互验收。
- 没有新增真人 QA 结论；没有执行新电脑真实连接或 Connector OAuth happy path。
- 全量 Web typecheck 此前失败（含控件值类型与 UI `#/` 解析问题），本次没有重跑或修复，不能声称所有静态检查全绿。
- `patches/reka-ui@2.10.1.patch` 是 host 提供的依赖修复；独立 UI 消费方的分发仍需处理。恢复时保留 lockfile 与 pnpm-workspace.yaml 的补丁声明。
- 尚需确认全站其他卡片 owner、键盘/无障碍、窄屏碰撞和深浅主题。字体及主题配色重调被用户明确延后。

首次 checkpoint 的验证和恢复命令见 `ui-checkpoint-2026-09-08.md`；其提交钩子记录描述的是当时，不代表本轮检查状态。
