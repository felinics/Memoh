# Web 前端（`apps/web`）

`@memohai/web` 是 Memoh 的 Vue 3 + Vite 管理与聊天前端，同时由 `apps/desktop` 原生桌面端直接复用。

## 必读规范入口（四层正交体系）

在 `apps/web` 或 Desktop 渲染层编写、修改或审查代码前，按任务加载以下规范：

1. **`.agents/skills/memoh-frontend-rules/SKILL.md`（主仓工程与行为总纲）**：5 条核心原则、10 条红线、冲突处理顺序、22 条规则索引（`U1–U10`、`M1–M8`、`D1–D4`），以及宿主架构地图（`references/architecture.md`）、完整规则（`references/rules.md`）与审查清单（`references/review.md`）。
2. **`packages/ui/AGENTS.md`（组件库基础 Token 与视觉契约）**：`@felinic/ui` 的颜色、叠色梯、三态选中、圆角、细线边框、浮层阴影、5 级 z 梯、字号缩放与动效档位。
3. **`packages/ui/skills/ui-owners/SKILL.md`（原子与布局原语字典）**：页面外壳、分组卡片、行、表单字段、统计磁贴、四级加载与弹窗原语的选型字典与手写边界。
4. **`packages/ui/skills/ui-patterns/SKILL.md`（跨组件交互与场景规范库）**：表单与校验、保存模型与 Toast、空状态与加载、首屏 99/1 渐进披露与聚焦弹窗、分割线与推拉导航、实时状态面板与反模式速查。

请勿在本文件中重复抄写具体规则，所有架构与视觉规范均在上方四处单一出处维护。
