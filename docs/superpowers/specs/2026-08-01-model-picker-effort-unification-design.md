# 模型选择器统一与 reasoning_enabled 下线

日期：2026-08-01

## 背景

Memoh 目前有两套并行的模型选择器实现，能力互补但互不相通：

| 能力 | `chat-model-picker.vue`（Chat 组合框） | `model-options.vue`（Bot 页） |
|---|---|---|
| 搜索 + 虚拟列表 | 有（独立实现） | 有（独立实现） |
| effort 二级菜单 | 有 | 无 |
| 键盘导航（`useListboxKeyboard`） | 无 | 有 |
| aria listbox 语义 | 无 | 有 |
| 「无」空选项（`noneLabel`） | 无 | 有 |
| 选中项置顶排序 | 有 | 无 |

与此同时，推理档位由两个字段共同表达：`bots.reasoning_enabled`（布尔开关）与
`bots.reasoning_effort`（档位）。这个二元组制造了一个无法表达的第三态——设置页
的下拉里「关闭」本身就是一个选项，所以数据层没有「未设置」，而建库默认
`reasoning_enabled = false` 让新 bot 一开机就长得像「用户明确选了关闭」。

现在几乎所有在用的模型都自带 effort 档位，且档位列表里普遍已有 `none` 这类
「基本不思考」的档位，独立的开关字段不再有存在价值。

## 目标

1. 推理档位收敛为单一字段 `bots.reasoning_effort`，删除 `bots.reasoning_enabled`。
2. 合并两套选择器为一个组件，Chat、New Bot、Bot 设置页共用，且带 effort 选择。
3. Chat 组合框不再显示占位的「默认」，改为显示具体模型；无模型时显示「无」并禁用发送。
4. 前后端使用同一套「模型不支持 medium 时的回退」规则。

## 非目标

- 不改 Onboarding 的建 Bot 步骤（`Step4Bot.vue`）。
- 不改 ACP composer 的模型／effort 语义——它的档位由外部 agent 上报，继续走
  `reasoningOptions` prop 这条既有通路。
- 不把存量 bot 的推理批量打开（见「存量迁移」）。

## 设计

### 1. 数据模型

`reasoning_effort` 成为唯一真相，取值三类：

- `'disable'` —— 关闭推理。
- 具体档位（`none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max`）。
- 建库默认 `'medium'` —— 即「没设过」等价于 medium。

该列是 `TEXT NOT NULL DEFAULT 'medium'`，原 CHECK 约束已在 0093 移除
（`0001_init.up.sql:271` 有注释），可直接存 `'disable'`；写入侧
`internal/settings/service.go` 的 `isValidReasoningEffort` 只判非空，不拦。

新增迁移 `0128_drop_reasoning_enabled`：

```sql
-- up
UPDATE bots SET reasoning_effort = 'disable' WHERE reasoning_enabled = false;
ALTER TABLE bots DROP COLUMN IF EXISTS reasoning_enabled;

-- down
ALTER TABLE bots ADD COLUMN IF NOT EXISTS reasoning_enabled BOOLEAN NOT NULL DEFAULT false;
UPDATE bots SET reasoning_enabled = (reasoning_effort <> 'disable');
UPDATE bots SET reasoning_effort = 'medium' WHERE reasoning_effort = 'disable';
```

同步删除 `0001_init.up.sql` 中的列定义，更新
`db/postgres/queries/{bots,settings}.sql`（含 `DeleteSettingsByBotID` 的重置值），
执行 `mise run sqlc-generate`。

#### 存量迁移

建库默认值是 `false`，所以绝大多数存量 bot 会被映射成 `'disable'`：**升级后老
bot 的推理行为保持不变，只有新建 bot 拿到 `medium` 默认值。** 这是刻意选择的
保守映射——升级不静默改变现有部署的推理行为与成本。若日后判断需要把存量抬到
medium，应作为独立的一次迁移，而不是混在本次结构变更里。迁移文件内以注释记录
这一取舍。

### 2. 后端语义

`internal/agent/application/service.go` 的 `resolveReasoningConfig` 只改分支条件：

```go
case reasoningEffortDisabled(botSettings.ReasoningEffort):
    return &models.ReasoningConfig{Disabled: true, OffEffort: offEffort}
default:
    return &models.ReasoningConfig{Active: true, Adaptive: adaptive,
        Effort: pickEffort("", botSettings, effortLevels), OffEffort: offEffort}
```

`pickEffort` 的档位优先级（override → bot 默认 → medium）本就正确，只改最后的
兜底：原先取 `effortLevels[0]`（模型宣称的第一档，通常是最弱档），改为新函数
`nearestToMedium(effortLevels)`。

#### 回退规则：nearestToMedium

按 `none < minimal < low < medium < high < xhigh < max` 的序号，取距 medium
最近的档位，平手取低的一侧。例：`[minimal, low]` → `low`；`[high, max]` → `high`；
`[low, high]` → `low`。前端 `reasoning-effort.ts` 实现同名同规则的函数，替换
`chat-pane.vue` 中那个回退到 `efforts[0]` 的分支——由于
`availableEffortsForMode` 会把 `'disable'` 放在列表首位，该分支的回退值恒为
「关闭」，是既有缺陷。

#### 连带改动

- `internal/settings/{types,service}.go`：删 `ReasoningEnabled` 字段与其
  upsert 分支。
- `internal/command/reasoning.go`：`/reasoning set off` 写
  `reasoning_effort = 'disable'`；`validEffort` 接受 `disable`。
- `internal/command/{settings,model_picker,handler,result}.go`、
  `internal/channel/inbound/result_render.go`：展示逻辑改为只读 effort，
  `'disable'` 渲染为「关闭」。
- `internal/botbackup/import.go`：导入旧档案时 `reasoning_enabled: false` 映射为
  `reasoning_effort: 'disable'`；导出不再写该字段。
- Go i18n（`internal/i18n/locales/*.json`）：`/settings update` 用法文案去掉
  `--reasoning_enabled`，`--reasoning_effort` 取值补 `disable`。
- 重新生成 swagger 与 SDK。`SettingsSettings.reasoning_enabled` 从 SDK 消失是
  破坏性变更。

### 3. 共用选择器组件

不新建第三个组件。`model-select.vue` 负责「trigger 按钮 + Popover」，而
`chat-pane.vue` 自管 Popover，因此可共用的是 **popover 内容**——正好就是
`model-options.vue` 的定位。

在 `model-options.vue` 上扩展：

- 新增可选的 effort 底栏（`PopoverAnchor` + 右侧 flyout），由 prop 开关；未传
  effort 相关 prop 时组件行为与现在完全一致。
- 移入 `chat-model-picker.vue` 的「打开时把当前选中项及其分组置顶」排序。
- 样式统一到 `model-options.vue` 一侧，即 `@felinic/ui` 的 `menuItemClass` /
  `menuLabelClass` 等设计令牌。`chat-model-picker.vue` 中手写的
  `bg-[var(--overlay-hover)]` 等裸值不予保留，合并后该组件满足
  `scripts/check-ui-contract.mjs` 的令牌契约。

随后删除 `chat-model-picker.vue`。消费方：

- `chat-pane.vue` 直接使用 `ModelOptions`。
- `model-select.vue` 透传 effort 的 `v-model`，供 `new.vue` 与
  `settings-interaction-card.vue` 使用。

ACP composer 继续通过 `reasoningOptions` prop 提供外部上报的档位，语义不变。

### 4. Chat 组合框

- `selectedModelLabel` 不再回退到 `chat.modelDefault`（「默认」）。非 ACP 场景下
  `chat_model_id` 为空时显示「无」，并禁用发送按钮。
- 「无模型」的判定只看 `chat_model_id` 是否为空。已配置但模型已被删除时，仍显示
  原始 ID 且不禁用发送——避免因模型列表加载时序造成误禁。
- `initFromBotSettings` 去掉 `reasoning_enabled` 判断，直接取
  `botSettings.reasoning_effort`（迁移后必定有值）。

### 5. New Bot 页与 Bot 设置页

- `new.vue`：表单新增 `reasoning_effort`（初值 `'medium'`），
  `createStartOptions().settings` 携带该字段；`bot-create-progress.ts` 的
  `hasSettings` 与 `settingsBody` 白名单同步放行。未选模型时 effort 部分禁用，
  因为可选档位由模型能力决定。
- `settings-interaction-card.vue`：删除独立的 effort `SettingsRow` 与读写
  `reasoning_enabled` 的 `reasoningFormValue` computed，改为直接绑定
  `form.reasoning_effort`。

## 测试

Go 侧需同步修改的既有用例：`internal/agent/application/service_model_selection_test.go`、
`internal/settings/service_test.go`、`internal/command` 相关用例、
`internal/botbackup/backup_flow_test.go`、`internal/channel/inbound/result_render_test.go`、
`internal/bots/service_test.go`、`internal/agent/runtime/session/acceptance/api_test.go`。

新增用例：

- `nearestToMedium` 的档位选取（Go 与 TS 各一份，覆盖平手取低）。
- `reasoning_effort = 'disable'` 时 `resolveReasoningConfig` 返回 Disabled。
- botbackup 导入含 `reasoning_enabled: false` 的旧档案，落到 `'disable'`。
- Chat 组合框在无模型时显示「无」且发送禁用。

TS 侧：`reasoning-effort.test.ts` 补回退规则，`chat-model-picker.test.ts` 迁移到
`model-options`，`settings-interaction-card.test.ts` 随表单绑定调整。

## 验收

1. `mise run lint` 与全量测试通过。
2. 迁移在既有库上可正向应用并可回滚。
3. 开发环境重启后，settings API 响应不再包含 `reasoning_enabled`；将
   `reasoning_effort` 置为 `disable` 后该 bot 的回合不携带推理配置。
