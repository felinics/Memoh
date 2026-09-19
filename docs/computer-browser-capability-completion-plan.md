# Computer Use / Browser Use 能力与契约补齐计划

状态：待实施。本文只定义目标、接口、实施顺序和验收条件，不代表功能已经交付。

日期：2026-09-18。源码基线：`d579c9d06`。Codex 对照依据为本次会话实际读取的本地 Computer Use 运行时公开 API，不推断其闭源内部实现。

## 1. 目标和边界

补齐 Memoh Computer Use / Browser Use 已有但未声明、声明但行为不完整，以及缺少的操作能力。完成标准是模型能发现、正确调用、观察并验证真实效果，而不是仅增加 schema 字段。

本轮明确不加入 JS 编排：

- 不新增 `computer_use_exec`、`computer_use_reset`、持久化 REPL、QuickJS/WASM 或 Node 执行器。
- 不新增 `cua` JS 对象、顶层 await、跨调用 JS 变量、Promise 调度或脚本恢复机制。
- Codex 的 `emit`、`nodeRepl.write`、`nodeRepl.emitImage` 属于 JS 输出设施，本轮不机械复制成 JSON 参数。
- 保留现有 `browser_observe.evaluate` 页面表达式执行和 `browser_remote_session` CDP 连接出口；两者不承诺跨调用 JS 编排语义。

本轮保留五个现有工具，新增一个上下文发现工具：

| 工具 | 责任 |
| --- | --- |
| `computer_context`（新增） | 发现和选择应用、浏览器，返回目标身份、初始观察和能力说明 |
| `computer_action` | 应用或整个工作区桌面的输入与操作 |
| `computer_observe` | 桌面或应用的结构、截图和诊断 |
| `browser_action` | 指定浏览器标签页的操作及标签页管理 |
| `browser_observe` | 指定浏览器标签页的观察和诊断 |
| `browser_remote_session` | 代码客户端连接工作区浏览器的 CDP 会话 |

执行范围仍是 Bot 的 native Server Workspace。应用发现仅覆盖该工作区，浏览器发现仅覆盖已注册且当前 Bot 可访问的端点。用户已连接电脑、宿主 macOS/Windows 的原生控制和任意外部 CDP 地址接入不在本计划中；不得把这一边界描述成已支持跨电脑控制。

应用和浏览器发现接口必须支持多个目标；验收至少使用两个应用和两个浏览器实例，不能把当前只有一个 Chrome 当成接口设计前提。当前后端不支持的能力必须能被发现，并返回明确的不支持错误。

## 2. 已核对的当前实现及差距

主要源码入口：

- [工具定义、CDP 与 RFB 执行](../internal/agent/tool/browser.go)
- [Go AT-SPI 适配](../internal/agent/tool/computer_a11y.go)
- [Rust CLI 参数](../crates/a11y-cli/src/main.rs)
- [Rust 快照输出](../crates/a11y-cli/src/snapshot.rs)
- [Rust 可访问性动作](../crates/a11y-cli/src/action.rs)
- [原生工具媒体装饰](../internal/agent/runtime/native/read_media.go)
- [工具审批策略](../internal/agent/decision/approval/policy.go)
- [当前开发环境](development.md)

以下是静态源码核对结果，不能替代真实运行复现：

| 编号 | 当前问题 | 实际影响 |
| --- | --- | --- |
| C01 | 六个 browser action 值有描述和实现，但未进入枚举 | 严格 schema 调用无法合法表达这些操作 |
| C02 | schema 只要求 `action` / `observe`，没有按动作约束必填与互斥参数 | 缺参数、错参数组合要到执行时才失败，部分输入被静默忽略 |
| C03 | Browser 鼠标执行器有按钮和次数参数，上层固定左键、一次或两次 | 页面内无法直接表达右键、中键和更多点击次数 |
| C04 | Computer 按 ref 点击未使用 `button`；AX 双击成功路径只调用一次默认动作 | 参数和成功结果不能代表真实操作 |
| C05 | Computer `fill` 的 RFB 回退只输入，没有清空；Browser 和 Computer 拒绝空文本 | 替换和清空语义不成立 |
| C06 | Browser snapshot 从 DOM 提取最多 300 个交互元素，返回平面列表 | 不能称为完整 AX 树，层级、状态和辅助动作缺失 |
| C07 | Rust snapshot 的 `lines` 为数组，Go `a11ySnapshotOutput.Lines` 为字符串 | 两端当前形状不一致；该产物被部署时，Go 解码会失败 |
| C08 | Rust 输出 `x/y/width/height`，Go 读取 `center/center_x/center_y` | 元素中心坐标不能可靠恢复；必须与实际 workspace helper 版本一起验证 |
| C09 | Rust `snapshot --limit` 和 `probe` 没有模型工具入口 | 无法调整快照限制或主动查询诊断 |
| C10 | `timeout` / `amount` 的 schema 默认值与部分执行分支不一致 | 同一输入随调用路径产生不同等待时间 |
| C11 | 截图只返回 workspace 路径；媒体装饰针对 `read` | 新观察接口需要真实接入模型媒体输入及历史，而非只改返回 JSON |
| C12 | 普通浏览器操作依赖当前页，切换使用索引 | 多标签页和并行任务容易操作错误目标 |
| C13 | 缺少粘贴格式、精确选文、指定辅助动作及应用级上下文 | 多行富文本、局部编辑和原生应用操作不完整 |
| C14 | `browser_remote_session.status` 不按 `session_id` 筛选，session ID 与 target ID 混用 | 会话查询和关闭对象不够明确 |
| C15 | `evaluate` 接收任意页面表达式，“只读”只是文字指导 | 不能把该分支当成被执行层保证的只读观察 |

共享 CDP 执行器里的 `snapshot/get_content/screenshot_annotate/screenshot/get_html/evaluate/get_url/get_title/pdf/tab_list` 已在 `browser_observe` schema 中声明；`script` 和 `full_page` 也在那里。它们不是整个工具集的遗漏，不重复搬进 `browser_action`。

## 3. 公共调用契约

### 3.1 目标身份

- `app_id`：发现接口返回的工作区应用实例 ID，不是 shell 命令。
- `browser_id`：发现接口返回的浏览器实例 ID，不允许将任意 URL 作为 ID。
- `tab_id`：标签页生命周期内稳定的 ID；浏览器重启后旧 ID 失效。
- `snapshot_id`：一次观察的标识，包含服务端可验证的目标及 generation 关联。
- `ref`：观察返回的元素引用，必须绑定目标及快照。新 ref 应采用可验证的不透明格式；兼容旧 `eN` 时只在当前会话的明确快照中解析，不能重新扫描后按同一序号误操作其他元素。
- `session_id`：CDP 连接会话 ID，与 `tab_id` 分开。

显式目标优先；缺省目标按会话保存，不在整个 Bot 或进程范围保存。动作开始后冻结目标，不得因别的请求切页而改用另一目标。目标身份始终按 Bot、当前执行身份、工作区实例和权限验证，不能仅凭字符串 ID 访问。

### 3.2 定位规则

- Browser 元素操作支持 `ref` 或 `selector`，支持坐标的动作另可用 `x/y`；三者互斥。
- `x/y`、`to_x/to_y` 必须成对；拖动的源和目标分别检查，不允许不完整组合。
- Computer 元素操作优先 `ref`，坐标为显式定位或后端受控回退。`type` 未传 ref 时作用于当前焦点；传入无效 ref 必须报错，不能静默输入到当前焦点。
- `tab_id` 与兼容参数 `tab_index` 互斥，索引从 0 开始，仅针对所选 browser 的当前列表。
- `app_id` 与 ref 指向的应用、`tab_id` 与 ref 指向的标签页必须一致，否则报错。
- Browser 坐标使用标签页视口 CSS 像素；Computer 坐标使用截图声明的桌面逻辑像素。截图必须返回像素尺寸、缩放及原点，提供唯一换算规则；不能把整页截图坐标直接当作视口坐标。

### 3.3 参数与校验

使用一个规范化动作定义同时生成工具 schema、执行前校验和 capability/documentation 输出。避免三处手工维护枚举与默认值；不为此引入独立 DSL 或插件框架。

每个动作要定义：必填字段、允许字段、类型、范围、默认值、定位方式、别名、结果、错误及是否产生副作用。

模型 provider 支持时使用有判别字段的分支 schema；若 provider 不支持该结构，输出兼容 schema 和精确描述，但服务端仍执行完整校验。未知参数、无效枚举、互斥组合和 action 不适用的参数均报错。

建议规范值：

| 参数 | 规范 |
| --- | --- |
| `button` | `left/middle/right`，默认 `left`；短别名如 `l/r/m` 若保留必须显式声明和归一化 |
| `click_count` | 1–3，默认 1；`double_click` 固定为 2，冲突值报错；更高次数只有后端明确支持后才能公布 |
| `direction` | `up/down/left/right`，默认 `down`；如接收 `u/d/l/r` 必须同样声明 |
| `timeout` | 毫秒，1–45000；导航、刷新和等待元素默认 30000 |
| `duration_ms` | 纯等待时长，1–10000，默认 1000 |
| `amount` / `pages` | 滚动时二选一；`amount` 为像素意图，默认 500；`pages` 为正的可有限表示数值，按目标可见区域换算；后端实际精度须返回 |
| `format` | `text/md/html`，默认 `text` |
| `selection_type` | `text/cursor_before/cursor_after`，默认 `text` |
| `limit` | 快照返回节点数，建议默认 300、最大 2000；底层遍历预算独立并可诊断 |
| `disable_diffing` | 默认 false；没有有效基线时返回完整快照并说明原因 |
| `image_mode` | `auto/path`，默认 auto；auto 在模型支持图片时注入媒体，否则返回路径和不可注入原因 |

`timeout` 控制等待上限，`duration_ms` 控制固定等待时长，两者不得混用。旧 Computer `wait.amount` 与 Browser 无目标 `wait.timeout` 在兼容阶段映射到 `duration_ms`；新旧字段同时出现时拒绝，不静默选一个。

### 3.4 错误、结果和中断

- 使用现有 `apperror`、Problem Details / SSE envelope 和稳定 code 规范；实施前读取 `memoh-error-handling` skill。
- 至少区分参数无效、目标不存在、引用过期、能力不支持、权限拒绝、超时、取消、执行结果未知和后端不可用。最终 code 纳入现有目录，不在本计划另造平行错误协议。
- 结果包含实际目标、执行方式（AX/CDP/RFB）、已完成动作及必要的观察信息。失败后不得报成功，不承诺跨 GUI 动作事务回滚。
- 超时后停止后续输入，并释放此次动作按住的鼠标键和修饰键。已发生但结果未知的副作用不得自动重试。
- 不把后端原始日志、总线地址、CDP 敏感连接信息或凭据放进公开错误。高级连接工具的连接信息按其既有授权边界返回。

## 4. 新增 `computer_context`

| action | 输入 | 输出与行为 |
| --- | --- | --- |
| `get_state` | 无 | 应用、浏览器、标签页清单与分域发现错误；一域失败不伪装为完整空列表 |
| `list_apps` | 无 | 工作区可发现应用的实例 ID、平台应用 ID、名称、运行状态、可用元数据 |
| `get_app` | `app` | 接受精确实例 ID、平台 ID、可解析名称或允许的应用路径；选择应用并返回 app_id 和初始观察；可启动已安装的未运行应用 |
| `list_browsers` | 无 | browser_id、名称、family、backend、可用 profile 信息及 capabilities |
| `get_browser` | `browser_id?`、`url?` | 选择已有实例，不导航、不新建标签页；URL 只用于匹配现有目标；歧义返回候选，不猜测 |
| `documentation` | `app_id?` 或 `browser_id?` | 返回当前后端真实支持的动作、参数、限制与版本；无目标时返回公共契约 |

应用路径必须作为独立 argv 处理，不能拼成 shell 命令；路径不存在或有多个同名候选时明确失败。名称解析、选择、必要启动和初始观察是一条可追踪操作，不能把发现应用等同于允许安装软件。

`last_used_at`、使用次数、profile 等仅在后端有可靠数据时返回；时间统一为有时区的 ISO 8601，缺失为省略或 null，不能把没有数据报告成 0 次或当前时间。应用发现与浏览器发现不暴露其他 Bot 或宿主机清单。

## 5. `computer_action` 完整目标清单

公共目标参数为 `app_id?`；使用元素引用时可显式提供 `snapshot_id`，同时校验 ref 自身的归属。以下定位均遵守第 3 节。

| action | 必填 / 条件必填 | 可选参数 | 补齐要求 |
| --- | --- | --- | --- |
| `click` | `ref` 或 `x/y` | `button`、`click_count` | ref 与坐标路径均正确执行按钮和次数 |
| `double_click` | `ref` 或 `x/y` | `button` | 真正双击；AX 默认动作不等于双击，必要时有依据地回退到坐标输入 |
| `type` | `text` | `ref` | 指定目标时先确保目标可编辑并获得正确焦点；按光标位置输入，不宣称总是追加到末尾 |
| `fill` | `ref`、`text`；兼容旧无 ref 调用必须明确当前焦点 | 无 | 替换全部内容；空字符串合法；RFB 回退必须先清空，无法保证则报错 |
| `set_value`（新增） | `ref`、`value` | 无 | 直接设置可编辑值，空字符串合法；不支持的控件类型明确报错 |
| `paste`（新增） | `text` | `ref`、`format` | 按指定格式粘贴；处理多行、中文、HTML 与剪贴板生命周期 |
| `select_text`（新增） | `ref`、`text` | `prefix`、`suffix`、`selection_type` | 精确匹配；重复匹配未消歧报错；支持选中及光标前后定位 |
| `secondary_action`（新增） | `ref`、`name` | 无 | name 必须来自最近有效观察中该元素暴露的辅助动作 |
| `key` | `key` | 无 | 明确组合键语法；正确处理修饰键的按下和释放 |
| `scroll` | 无 | `ref` 或 `x/y`、`direction`、`amount` 或 `pages` | 无定位时使用所选应用可见区域，未选择应用时为桌面；报告 RFB 离散滚轮精度 |
| `drag` | `x/y`、`to_x/to_y` | `button` | 完整按下、移动、释放；取消和失败也释放按钮 |
| `wait` | 无 | `duration_ms` | 与输入工具取消链路一致；兼容旧 amount，但不用于新增调用文档 |
| `mouse_move` | `x/y` | `button_mask` | 清楚定义当前按钮状态；与 pointer 共享底层语义 |
| `pointer` | `x/y` | `button_mask` | 原始 RFB 状态，范围 0–255；0 表示释放所有由该状态控制的按钮 |

粘贴的原生应用路径应保存并恢复原剪贴板；恢复时检测期间是否被用户修改，不能覆盖用户新复制的内容。剪贴板操作按工作区串行协调，异常和取消时也执行清理。富文本无法被后端正确表达时返回不支持，不静默转成普通文本。

## 6. `browser_action` 完整目标清单

页面动作增加 `browser_id?`、`tab_id?`。显式 tab ID 能唯一确定浏览器时可省略 browser ID；同时传入时必须匹配。管理动作按表中范围选择目标，不对所有 action 无条件接受全部公共字段。

| action | 必填 / 条件必填 | 可选参数 | 补齐要求 |
| --- | --- | --- | --- |
| `navigate` | `url` | `timeout` | 打开所选标签页，等待失败不得吞掉；返回导航后目标身份 |
| `click` | `ref/selector` 或 `x/y` | `button`、`click_count` | 接通底层按钮与次数；支持元素和视口坐标 |
| `double_click` | 同 click | `button` | 固定两次；与通用 click_count 契约一致 |
| `focus` | `ref/selector` | 无 | 返回实际聚焦结果，不允许错误目标静默成功 |
| `type` | `ref/selector`、`text` | 无 | 聚焦目标后在光标处输入 |
| `fill` | `ref/selector`、`text` | 无 | 支持清空及 contenteditable；验证输入事件、受控表单和真实值变化 |
| `set_value`（新增） | `ref/selector`、`value` | 无 | 直接设置支持的可编辑控件值，空字符串合法 |
| `paste`（新增） | `text` | `ref/selector`、`format` | 无定位作用于当前焦点；md 按 Markdown 源文本输入，html 支持实际富文本粘贴 |
| `select_text`（新增） | `ref/selector`、`text` | `prefix`、`suffix`、`selection_type` | input、textarea 与 contenteditable 的选区和光标语义一致 |
| `secondary_action`（新增） | `ref`、`name` | 无 | 基于后端真实暴露能力；不支持时明确返回，不用臆测 DOM click 替代 |
| `press` | `key` | 无 | 当前目标页组合键输入；与 desktop key 保持共同语法 |
| `keyboard_type` | `text` | 无 | 正式进 schema，直接向当前焦点输入 |
| `keyboard_inserttext` | `text` | 无 | 正式声明兼容别名，归一化到当前焦点输入 |
| `keydown` | `key` | 无 | 正式进 schema，跟踪由当前调用链持有的按键状态 |
| `keyup` | `key` | 无 | 正式进 schema，发送释放并清理持有状态 |
| `hover` | `ref/selector` 或 `x/y` | 无 | 同时支持元素和视口坐标 |
| `select` | `ref/selector`、`value` | 无 | 允许空 value；验证选项存在及选中结果 |
| `check/uncheck` | `ref/selector` | 无 | 验证控件类型、最终状态及真实 change/input 行为 |
| `scroll` | 无 | 定位、`direction`、`amount` 或 `pages` | 支持页面、元素容器和指定坐标，不能错误滚动外层页面 |
| `scroll_into_view` | `ref/selector` | 无 | 目标滚入视口；处理嵌套容器 |
| `drag` | 源定位、目标定位 | `button` | 源用 ref/selector 或 x/y，目标用 target_ref/target_selector 或 to_x/to_y；验证真实拖放效果 |
| `upload` | `ref/selector`、`files` | 无 | 保留 workspace 文件路径数组；检查路径权限、文件存在及控件结果 |
| `wait` | 无 | 目标定位与 `timeout`，或 `duration_ms` | 有目标等待其出现，无目标固定等待；禁止两种语义混合 |
| `go_back/go_forward` | 无 | `timeout` | 返回目标页及导航结果，不能操作别的标签页历史 |
| `reload` | 无 | `timeout` | 与 navigate 使用一致就绪和超时契约 |
| `tab_new` | 无 | `browser_id`、`url`、`visible`、`session_name` | 返回稳定 tab_id 和初始状态；不支持的展示选项必须明确报错 |
| `tab_get`（新增） | `tab_id` | `browser_id` | 取得标签页信息和初始状态，不依赖当前页 |
| `tab_select` | `tab_id` 或兼容 `tab_index` | `browser_id` | 更新当前会话缺省选择；激活目标的实际行为需可观察 |
| `tab_close` | 无 | `tab_id` 或兼容 `tab_index`、`browser_id` | 无参数关闭当前会话所选页；不存在目标不改关其他页 |
| `tab_mark_deliverable`（新增） | `tab_id` | `browser_id` | 持久化任务交付标记并在 UI 提供可打开结果 |
| `tab_mark_handoff`（新增） | `tab_id` | `browser_id` | 持久化需用户接手标记并在 UI 展示；不暗示任务已完成 |

兼容别名 `dblclick` 和 `scrollintoview` 必须进入允许值并在统一规范化层映射，不能只写在 description。`keyboard_inserttext` 也是明确声明的别名。新模型使用规范名称，历史调用仍可理解。

`visible` 表示后端支持的标签页展示策略，不自动关闭 Bot 的显示能力，也不隐式创建另一个无头浏览器。`session_name` 是任务组织元数据，不创建独立账号或浏览器配置目录。

## 7. 观察工具、AX 树与图片

### 7.1 `computer_observe`

| observe | 输入 | 输出 |
| --- | --- | --- |
| `snapshot` | `app_id?`、`limit?`、`scope_ref?`、`cursor?`、`disable_diffing?` | 结构化树、可读文本、snapshot_id、完整或增量标识、截断/分页及诊断 |
| `screenshot` | `app_id?`、`image_mode?` | 截图媒体或路径、尺寸、缩放、原点、时间及目标 |
| `state_and_screenshot`（新增） | 快照参数、`image_mode?` | 同一目标下协调采集的结构与截图及各自时间 |
| `probe`（新增） | `app_id?` | 可访问性、截图、输入通道健康状态与能力，不返回私有诊断原文 |

### 7.2 `browser_observe`

所有分支按需接受 `browser_id?`、`tab_id?`，`tab_list` 按浏览器限定。

| observe | 输入 | 输出与要求 |
| --- | --- | --- |
| `snapshot` | `limit?`、`scope_ref?`、`cursor?`、`disable_diffing?` | 真实浏览器 AX 树；包含角色、名称、值、状态、层级、边界和实际可用辅助动作 |
| `get_content` | `ref/selector?` | 可读文本，保留范围选择 |
| `get_html` | `ref/selector?` | HTML；明确整页 outerHTML 和局部内容的契约 |
| `screenshot` | `full_page?`、`image_mode?` | 截图；full_page 默认 false；报告截图坐标范围 |
| `screenshot_annotate` | `image_mode?` | 元素标注与可操作 ref；标注层始终清理，不能污染后续截图或页面 |
| `state_and_screenshot`（新增） | 快照参数、`full_page?`、`image_mode?` | 目标一致的 AX 状态与截图 |
| `evaluate` | `script` | 保留页面表达式能力；按可执行脚本处理权限和日志，不承诺任意 JS 可强制只读 |
| `get_url/get_title` | 无 | 指定页 URL / 标题 |
| `pdf` | 无 | 保留导出能力，处理输出大小，文件归属和可读取结果 |
| `tab_list` | `browser_id?` | 稳定 tab_id、当前顺序、名称、URL、会话标记及可用性 |
| `probe`（新增） | `browser_id?`、`tab_id?` | CDP、目标生命周期、AX 与截图能力诊断 |

### 7.3 快照与引用规则

1. 修复 Go/Rust 两端 `lines`、geometry 和版本信息的不一致，用真实 helper 输出验证解析。协议不匹配要返回版本错误，不能返回成功空树。
2. Browser 使用 CDP 可访问性数据作为语义来源，DOM 仅补充几何、定位和必要回退；DOM 扁平扫描必须标明降级来源。
3. 快照返回 `role/name/value/states/children/bounds/actions` 中可获得的真实字段；密码和敏感控件遵循现有隐私边界，不把密码值暴露为普通 value。
4. ref 绑定目标、节点及 generation，导航和重建失效；不得通过重新编号把旧 ref 映射到新元素。
5. 差异基线按会话、目标和观察配置隔离，包含 added/updated/removed；切换 app、tab、scope、工作区实例或基线失效时重新返回完整状态。
6. `cursor` 只读取同一 snapshot 的后续节点，与刷新参数互斥；遍历预算耗尽必须报告 `truncated` 和原因。分页不可覆盖的区域允许用 `scope_ref` 重新观察，不能宣称获得了整棵树。
7. 完整模式是当前范围内的完整快照，不是无限遍历承诺。按节点数、深度、时间和输出字节预算保护 Calc 等巨大树。
8. `state_and_screenshot` 不承诺跨操作系统的原子快照；固定目标并标注采集时间，目标或 generation 在采集期间改变时报错或显式标记，不组合不同页面的证据。

### 7.4 图片到模型和 UI 的链路

`image_mode: auto` 默认在模型支持图片时将截图作为媒体内容注入；同时保留 workspace 文件路径、类型、尺寸与归属。`path` 只返回文件信息，允许后续 `read`。模型不支持图片时返回路径及明确状态，不假装已经看过图片。

复用现有附件、模型媒体适配、历史持久化和消息投影通路，扩展目前只装饰 `read` 的入口。验收必须证明实时 UI 能显示、刷新历史仍能显示、下一次模型输入实际有图；只看到附件事件不能算模型已收到图片。

## 8. CDP 会话与任务标签页标记

### 8.1 `browser_remote_session`

| action | 参数 | 契约 |
| --- | --- | --- |
| `create` | `browser_id?`、`tab_id?` 或 `url?` | 指定 tab 时复用且不导航；指定 URL 时明确新建，不能修改无关页；无参数复用当前会话所选页，缺少时新建空白页 |
| `status` | `session_id?`、`browser_id?` | 指定 session_id 只返回该会话，省略时列出当前授权范围会话 |
| `close` | `session_id`、`close_tab?` | 默认撤销本次会话及受控代理入口；仅在显式 close_tab 且满足目标权限时关闭标签页 |

返回 `session_id/browser_id/tab_id/created_tab/status` 和连接信息。旧 session_id=target_id 与旧 close 直接关闭标签页的行为需要显式协议版本或迁移说明，不能悄悄改变语义。

要实现“撤销会话即失效”，必须有可撤销的代理/路由能力；直接返回永久 Chrome CDP 地址时不能声称可撤销现有连接。若采用原始直连，结果必须明确其权限范围和无法独立撤销的限制；受控会话完整交付仍须补代理生命周期，而非仅删除数据库记录。

CDP 浏览器级端点通常可访问多个标签页，不能把返回的 tab_id 当作权限隔离保证。目标级限制需要在代理层验证，或明确仅提供授权工作区范围的连接。

### 8.2 交付与接手标记

- 标记绑定 Thread、工作区实例、browser/tab 身份，支持幂等更新。
- 工具返回标记后，Web 和 Desktop 必须能在真实任务中展示并打开对应目标；过期或已关闭目标显示失效状态。
- `deliverable` 表示交付入口，`handoff` 表示用户接手；互斥状态如何切换由明确动作决定，不从标签页是否激活推断。
- 复用现有任务事件/产物持久化能力；若现有结构无法表达则增加必要 schema，按数据库规则生成迁移和 sqlc，不能把需要持久化的状态只存内存。

## 9. 实现组织和权限

将当前大文件按有独立责任的模块整理：目标发现与解析、Browser CDP、Computer AX/RFB、观察与引用、工具契约适配。具体包名在实现时依照仓库惯例确定，不先建立大量通用抽象。

JSON 工具适配层负责参数规范化与结果包装；后端能力层负责真实操作。应用/浏览器上下文、目标 generation 和引用生命周期由共同组件管理，避免每个工具各自保存当前目标。

- 用实际动作分类进行权限、审批、取消和事件记录；不能因工具名叫 observe 就将 evaluate 或 CDP 连接视为只读。
- 保留当前授权政策，不在此次补齐中引入每个点击都弹确认的新流程，也不因为新增工具而绕过已有权限。
- 特别检查新的 `computer_context.get_app` 可启动应用；新工具不能因未加入权限分类而默认放行本应受限的能力。
- 每次调用记录实际 app/browser/tab、动作、执行方式和结果；私有诊断留在日志。
- 原生桌面共享焦点和键鼠，按实际工作区显示资源协调。不同 Thread 或子 Agent 即使各有目标上下文，也不能同时破坏同一个桌面的输入。
- 同一标签页修改顺序执行；不同独立标签页的只读操作可并行，但可访问性快照、标注清理与导航要正确协调。
- 不承诺用户物理输入不会影响操作；发现焦点、目标或 generation 改变后停止，不把输入发给错误对象。
- 工具用法放在 Tool.Description 与 ToolUsage.Usage；不增加静态系统提示中的工具工作流。

## 10. 实施阶段与完成条件

各阶段均为必做范围，阶段顺序不代表后续能力可以省略。下面复选框只在实际代码、运行和证据齐全后勾选。

### P0：修复当前协议和动作错误

- [x] 对齐 Rust/Go 快照结构、几何信息、版本及诊断，核对实际 workspace helper 产物。
- [x] 补六个遗漏 action 枚举，归一化兼容别名。
- [x] 修复 ref 按钮、双击、fill 清空/回退及空值输入。
- [x] 对齐等待默认值，停止吞掉导航就绪失败。
- [x] 建立按动作校验，拒绝错误参数组合。

验收：真实 GTK/浏览器输入框能替换和清空；真实列表项双击与右键效果正确；快照可以从当前 helper 解码；错误输入在任何副作用之前被拒绝。

P0 实施记录（2026-09-18，分支 `feat/computer-browser-p0`）：

- 协议：`crates/a11y-cli` 输出带 `protocol_version`（当前 2）、`helper_version`、`limit`、`truncated`、数组形式的 `lines`、逐项 `x/y/width/height` 与 `states`；Go 侧 `computer_a11y.go` 校验版本后再解码，旧 helper 或不认识 `locate`/`--limit` 的 helper 返回「重建工作区镜像」错误而不是空树。真实 helper 产物已用重建后的开发镜像验证（`helper_version` 0.1.0，diagnostics 计数随快照返回，总线地址只进日志）。
- 契约：`gui_contract.go` 用一份动作规范同时生成工具 schema、`action` 参数说明和执行前校验；六个原本只在描述里的 action（`keyboard_type`、`keyboard_inserttext`、`keydown`、`keyup`、`dblclick`、`scrollintoview`）进入枚举，别名统一归一化。
- 动作：Browser `click/double_click/hover/drag` 支持 `button`、`click_count` 与视口坐标；Computer 按 ref 的双击、三击、中键、右键通过新增的 `a11y-cli locate` 取元素中心后回放真实指针事件（AT-SPI 默认动作只触发一次且没有按钮）；`fill` 两侧都接受空字符串，Computer 的 RFB 回退先 Select All + BackSpace 再输入。
- 等待：`timeout` 只用于就绪等待（navigate/reload/go_back/go_forward/带目标 wait，默认 30000）；`duration_ms` 用于固定等待；两者同时出现拒绝；导航与刷新的就绪失败改为返回错误。
- 实施中发现并修复：GTK 未布局的表格单元通过 AT-SPI 报告 `-2147483648,-2147483648` 的坐标，原实现会把它当成指针目标（实测双击落到了桌面左上角的 Applications 菜单）；现在两侧都把中心不在 0–32767 范围内的元素视为无几何，指针类动作明确报错。
- 验证方式：由于本机没有可用的真实模型密钥，使用脚本化的 OpenAI 兼容模型服务驱动真实的 Memoh Web 聊天 → Agent → 工具链路（工具调用与结果均为真实执行），场景与截图见 PR。

### P1：目标发现和稳定引用

- [x] 新增 computer_context 全部 action。
- [x] 增加 app_id/browser_id/tab_id 定位、会话缺省上下文及 generation。
- [x] 实现稳定 ref 与快照归属；失效引用不能命中新元素。
- [x] 支持两个应用、两个浏览器、多个标签页和同名目标歧义。

验收：切换或关闭其他标签页不改变显式 tab_id 的操作对象；不同会话互不覆盖默认目标；已连接电脑默认位置不改变 GUI 工具的 native Workspace 范围。

P1 实施记录（2026-09-19，分支 `feat/computer-browser-p1-p2`，基于 P0）：

- `computer_context`（`internal/agent/tool/computer_context.go`）：`get_state`（应用、浏览器、标签页与会话当前选择，分域报错）、`list_apps`/`get_app`（`a11y-cli apps` 通过总线守护进程解析 pid，`app_id = app:<pid>`；名称歧义返回候选 id；`launch=true` 按 desktop entry / PATH / 绝对路径解析为 argv 启动，不经 shell 拼接，并由审批策略按 exec 治理，见 `approval.OperationForCall`；启动后未注册到无障碍总线的进程如 xterm 返回 `accessible: false` 的结构化结果而不是伪装成功）、`list_browsers`/`get_browser`（按 `--remote-debugging-port` 发现 Chromium 实例，`browser_id = chrome-<port>`；URL 只用于匹配现有标签页，多个匹配返回候选）、`documentation`（返回生成 schema 所用的契约与各后端能力）。
- 目标身份：所有浏览器调用接受 `browser_id`/`tab_id`（兼容 `tab_index`），所有桌面调用接受 `app_id`；缺省来自 per-(bot, session) 的内存状态（`gui_session.go`），不同会话互不覆盖；动作开始前冻结目标，`tab_source` 标明来源。`browser_id` 必须是发现返回的 id，URL 与命令名在校验层即被拒绝。
- 稳定引用：浏览器快照把元素列表钉在页面上（`memohTakeSnapshot`）并返回 `snapshot_id`，ref 只在同一快照、同一标签页、元素仍在文档中时解析，导航/刷新/历史使之失效；桌面快照的 `snapshot_id` 写入 helper 的引用索引，动作携带 `--snapshot`，跨快照 ref 被 helper 拒绝，Go 侧还要求本会话最近快照覆盖所选应用。
- 真实验证（脚本化模型驱动真实 UI）：两台 xfce4-terminal + 一台 xfce4-appfinder、两个 Chromium 实例（9222/9223）与多个标签页；同名应用歧义、显式 tab_id 不受会话切页影响、导航后与跨快照 ref 被拒绝、跨会话默认目标隔离（每条消息新会话）均有工具记录，见 PR 截图。

### P2：补齐输入和元素操作

- [x] Computer/Browser 的 paste、select_text、set_value、secondary_action。
- [x] Browser 坐标点击/悬停/拖动、按钮与次数、pages 滚动。
- [x] 完整键盘按下/释放、取消清理与剪贴板恢复。
- [x] 对表单、contenteditable、原生控件验证实际事件和保存结果。

验收：多行中文/emoji、HTML 粘贴、重复文本消歧、选区前后光标、嵌套滚动、拖放、用户改动剪贴板和中断后的释放均有实际证据。

P2 实施记录（2026-09-19，同一分支）：

- Browser：`set_value`（原生 setter + input/change，select 校验选项存在）、`paste`（真实 `paste` 事件携带 text/plain 与 text/html，页面处理器消费则 `via: paste_event`，否则 contenteditable 走 insertHTML、字段走 insertText）、`select_text`（`prefix+text+suffix` 唯一匹配，重复与缺失分别报错；`selection_type` 支持选中及光标前后；UTF-16 偏移）、`secondary_action` 明确返回不支持（DOM 无辅助动作）、`scroll` 支持 `pages`（按目标可见区域换算，返回前后 scrollTop）、`keydown`/`keyup` 在会话内跟踪按住的键并在后续 `press` 中带上修饰键，`press` 在失败时也释放本次按下的修饰键。
- Computer：`set_value`（EditableText 内容或 Value 接口数值，超范围与非数值明确报错，不支持的控件如按钮返回明确错误）、`select_text`（AT-SPI Text 选区/光标，字符偏移）、`secondary_action`（只执行快照 `actions=` 列出的动作，未列出的名称返回可用列表）、`paste`（xclip 剪贴板：保存原剪贴板 → 写入 → 聚焦目标 → Ctrl+V → 恢复；粘贴期间被改动则保留，按 bot 串行）；工作区镜像新增 `xclip`。
- 键鼠：Browser `mouseDrag` 在中途失败时也释放按钮；Computer `drag` 失败时补发释放；`mouse_move`/`pointer` 保持的按钮在后续失败时释放。
- 真实验证：浏览器侧 set_value「设置的值 🚀」→ select_text 消歧/光标定位后输入 → html 粘贴被页面处理器消费、文本粘贴走 insertText → pages 滚动 scrollTop 0→120 → keydown Shift 后页面 keydown 事件 shiftKey=true，keyup 后为 false；桌面侧 set_value/select_text/fill 清空/剪贴板粘贴「粘贴 pasted 🚀」后原剪贴板「user clipboard」被恢复、未列出的辅助动作与按钮 set_value 被明确拒绝。

### P3：观察、真实 AX 与截图媒体

- [ ] Browser 真实 AX、Computer 结构化树与辅助动作。
- [ ] 完整/增量快照、limit、scope、cursor、截断与失效规则。
- [ ] state_and_screenshot、probe、坐标元数据。
- [ ] 截图直接媒体输入、路径回退、输出限制和历史恢复。

验收：超过 300 个节点及巨大原生表格可有界观察；截图与引用对应同一目标；模型实际收到图片；Web/Desktop 刷新后仍可查看。

### P4：浏览器会话与任务 UI

- [ ] 新标签页 visible/session_name、tab_get 及稳定 ID 管理。
- [ ] deliverable/handoff 工具、持久化、UI 展示和过期处理。
- [ ] CDP session 与 target 分离、精确 status、关闭/撤销语义及连接权限范围。
- [ ] 兼容旧 session ID 和关闭语义的明确迁移。

验收：任务页面中的标记真实可见可打开；刷新仍存在；关闭目标后状态准确；撤销测试证明连接行为与声明一致。

### P5：端到端回归与文档发布

- [ ] 更新工具 Usage、docs/agent-runtime.md 和必要 API 说明；文档与生成 schema 一致。
- [ ] 必要时更新 workspace 镜像、a11y-cli 构建产物、协议版本和部署诊断。
- [ ] 发生 REST/schema 变更时运行 swagger/sdk/sqlc 的对应生成任务。
- [ ] 执行下节真实验证矩阵，归档可审阅证据。
- [ ] OSS 实现完成后再核对 Cloud 同步范围和实际运行版本，不默认同步已覆盖。

最终完成条件：本计划所有新增 action、参数和修复均有明确结果；不支持项有真实能力说明；没有因 JS 编排被延期而遗漏普通工具能力。

## 11. 测试与真实验收矩阵

自动化测试选择行为边界和真实回归，不为字符串枚举或实现逐行镜像增加无意义测试。涉及并发使用协调点而非 sleep。通过测试不代替真实 UI。

| 场景 | 必须证明的结果 |
| --- | --- |
| helper 协议 | 使用真实 Rust 产物输出验证 Go 解码、lines、bounds、版本和诊断 |
| schema 不合法调用 | 缺参、未知参、冲突定位、错误按钮/次数在副作用之前失败 |
| ref 生命周期 | 导航、节点替换、应用重启、分页和工作区重建后不误操作 |
| 多目标 | 两应用、两浏览器、多标签页下显式 ID 和会话缺省相互隔离 |
| 文本操作 | 清空、替换、光标输入、中文、emoji、重复匹配、富文本及受控组件保存 |
| 鼠标和按键 | 右/中键、双/三击、拖动、滚动、keydown/up、取消后无残留按键 |
| AX 辅助动作 | 仅执行元素实际暴露动作；不支持时明确失败 |
| 快照 | 层级、状态、增量、完整、分页、截断、巨大树预算和 DOM 降级来源准确 |
| 截图 | 高 DPI、全页/视口坐标、图片输入、路径回退及历史刷新可见 |
| 故障 | AX 缺失、CDP 断连、目标关闭、输出过大、取消、超时和结果未知不报成功 |
| 权限 | 跨 Bot/会话目标不可访问；evaluate、应用启动和连接出口不绕过现有规则 |
| CDP 会话 | status 精确、目标所有权明确、撤销后行为一致、既有页不被误关 |
| 任务标记 | 实际任务 UI 展示、打开、持久化及过期行为正确 |

每个实现阶段遵守根 AGENTS.md 的 Mandatory Runtime and UI Verification：

1. 使用 `mise run dev` 或核实运行当前源码/配置的已有环境；记录 workspace mount、镜像和 helper 版本及服务健康。
2. 默认开发入口为 `http://localhost:18082`，Server 为 `18080`；若端口不同，记录实际值。
3. 通过真实 Memoh UI 使用配置好 display 的 Bot，触发工具并验证工作区浏览器/原生应用中的效果；直接调用后端只能补充证据。
4. 在当前运行版本截图并实际检查，关联工具记录、目标 ID、API 或运行日志。截图不能单独证明后端正确。
5. 提交 issue/PR 时提供 GitHub 可访问的截图附件；本地路径不等于已上传。Agent 验证不等于 Human QA。
6. 失败时记录精确阻塞、恢复尝试及缺少证据。没有当前任务的明确豁免，不能把未验证阶段标为完成。

## 12. 本文档交付范围

本文仅交付补齐计划。所有 P0–P5 项目均保持未完成，后续获得实施指令后按阶段执行。本次不提交、不推送、不创建 PR，不引入 JS 编排依赖，也不改动现有业务实现。
