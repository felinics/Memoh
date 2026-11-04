# 依据来源清单

`SKILL.md` 的「依据来源」规定了优先级，本文件列出具体查哪几篇。表中的链接、llms.txt 和
skill 都在 2026-11-04 实际打开或检索过；之后新增的条目同样要核实后再写入。

## 目录

- [使用方法](#使用方法), L:18
- [按领域查找](#按领域查找), L:29
- [第一级：平台规范与 Web 标准](#第一级平台规范与-web-标准), L:44
  - [Android](#android), L:46
  - [Apple](#apple), L:63
  - [Web 标准](#web-标准), L:72
- [第二级：框架与库](#第二级框架与库), L:82
- [第三级：同类框架与设计系统](#第三级同类框架与设计系统), L:111
- [维护本清单](#维护本清单), L:119

## 使用方法

1. 先在「按领域查找」里找到任务所属的领域，读列出的几篇。行为问题从第一级读起，实现问题
   从第二级读起。
2. 只读与任务相关的章节。文档站提供 `llms.txt` 时，先读它找到对应章节，再打开原页面。
3. 抓取不到正文的页面（例如由脚本渲染的 Material 3 站点），如实写「未读到原文」，不凭
   印象引用。
4. 引用时写到能定位的程度：文档标题加小节名，或规范的条款号，并附链接。规则里的引用写进
   该条的「原因」（见 `upkeep.md` § 规则的组成部分）。
5. 「官方 skill」指库的维护方自己发布的 skill。社区 skill 在表中单独标出，只当线索。

## 按领域查找

| 领域 | 查这些来源 |
|---|---|
| 返回、手势与页面栈 | Android 导航原则、自定义返回、预测式返回；Apple HIG 的 Modality；WHATWG HTML 的 Session history 与 Close requests；Vue Router 的历史模式；Capacitor App API；对照 Ionic 硬件返回键文档 |
| 系统栏、全面屏与安全区 | Android edge-to-edge；Apple HIG 的 Layout；Capacitor SystemBars API |
| WebView 与套壳 | Android WebView 指南；Capacitor 文档；对照 Ionic |
| 无障碍、对比度与焦点 | WCAG 2.2；WAI-ARIA APG；Reka UI 文档的无障碍说明 |
| 浮层、菜单与弹窗 | WAI-ARIA APG 的对应模式；Reka UI；Apple HIG 的 Modality；Material 3 |
| 加载与反馈时限 | web.dev 的 RAIL 模型；Material 3 |
| 通知与 Toast | Material 3；对照 IBM Carbon |
| 服务端数据与缓存 | Pinia Colada |
| 表单与校验 | vee-validate；Zod |
| 桌面端窗口、托盘与菜单 | Electron；electron-vite |

## 第一级：平台规范与 Web 标准

### Android

| 来源 | 内容 |
|---|---|
| [Principles of navigation](https://developer.android.com/guide/navigation/principles) | 固定起始页、返回栈、Up 与 Back 一致、深链的合成返回栈 |
| [Custom back navigation](https://developer.android.com/guide/navigation/custom-back) | 返回回调的顺序：后注册的先处理 |
| [Predictive back gesture](https://developer.android.com/guide/navigation/custom-back/predictive-back-gesture) | 预测式返回、回调的启用状态、Android 16 的行为变化 |
| [WebView](https://developer.android.com/develop/ui/views/layout/webapps/webview) | 在应用里承载网页、返回与 WebView 历史 |
| [Edge-to-edge](https://developer.android.com/develop/ui/views/layout/edge-to-edge) | 绘制到系统栏下方、insets、键盘 |
| [Material 3](https://m3.material.io) | 组件与交互规范。页面由脚本渲染，抓取工具常读不到正文 |

官方 skill：[android/skills](https://github.com/android/skills)，由 Google 安卓团队维护，
用 Android CLI 安装（`android skills add <name>`，说明见
[Android skills](https://developer.android.com/tools/agents/android-skills)）。内容以
Jetpack Compose 为主；移动壳是 WebView，只取其中讲平台行为的部分，例如 `edge-to-edge` 与
`navigation-event`。没有 llms.txt。

### Apple

| 来源 | 内容 |
|---|---|
| [Human Interface Guidelines](https://developer.apple.com/design/human-interface-guidelines) | 总入口；[Modality](https://developer.apple.com/design/human-interface-guidelines/modality) 讲弹层的进出，[Layout](https://developer.apple.com/design/human-interface-guidelines/layout) 讲安全区 |
| [UIKit](https://developer.apple.com/documentation/uikit) | 导航栈与弹层的系统行为（`UINavigationController`、`present`） |

未发现 Apple 官方 skill；没有 llms.txt。

### Web 标准

| 来源 | 内容 |
|---|---|
| [WHATWG HTML](https://html.spec.whatwg.org/multipage/) | 会话历史、[Close requests 与 CloseWatcher](https://html.spec.whatwg.org/multipage/interaction.html)、`<dialog>` 与 popover |
| [WCAG 2.2](https://www.w3.org/TR/WCAG22/) | 对比度、点击热区、键盘、缩放与重排（条款号写进 U8） |
| [WAI-ARIA APG](https://www.w3.org/WAI/ARIA/apg/) | 菜单、对话框、标签页等控件的键盘与读屏模式 |
| [MDN](https://developer.mozilla.org/en-US/docs/Web) | 浏览器 API 的说明与兼容性数据。它是标准的参考文档，不是标准本身，两者冲突时以标准为准 |
| [RAIL](https://web.dev/articles/rail) | Google 的响应时限模型（U4 引用） |

## 第二级：框架与库

版本以 `apps/web/package.json`、`apps/desktop/package.json` 等锁定的为准，打开对应大版本
的文档。llms.txt 一列写「有」的，路径是文档站根目录下的 `/llms.txt`；部分站点另有
`/llms-full.txt` 全文版。

| 库 | 用在哪 | 官方文档 | llms.txt | 官方 skill |
|---|---|---|---|---|
| Vue | 全部 | [vuejs.org](https://vuejs.org/guide/introduction) | 有，另有全文版 | 未发现；社区的 [vuejs-ai/skills](https://github.com/vuejs-ai/skills) 自述为非官方实验，只当线索 |
| Vue Router | 路由与历史 | [router.vuejs.org](https://router.vuejs.org/guide/) | 有 | 未发现；同上的社区 skill 含 `vue-router-best-practices` |
| Pinia | 客户端 store | [pinia.vuejs.org](https://pinia.vuejs.org/introduction) | 有 | 未发现；同上的社区 skill 含 `vue-pinia-best-practices` |
| Pinia Colada | 服务端数据 | [pinia-colada.esm.dev](https://pinia-colada.esm.dev/) | 有 | 未发现 |
| Reka UI | `@felinic/ui` 内部 | [reka-ui.com](https://reka-ui.com/docs/overview/getting-started) | 有，另有全文版 | 未发现 |
| VueUse | 通用 composable | [vueuse.org](https://vueuse.org/functions) | 无 | [vueuse/skills](https://github.com/vueuse/skills)（`vueuse-functions`） |
| Tailwind CSS | 样式 | [tailwindcss.com](https://tailwindcss.com/docs) | 无 | 未发现 |
| vee-validate | 表单 | [vee-validate.logaretm.com](https://vee-validate.logaretm.com/v4/) | 无 | 未发现 |
| Zod | 校验 | [zod.dev](https://zod.dev/) | 有 | 未发现 |
| vue-i18n | 三语文案 | [vue-i18n.intlify.dev](https://vue-i18n.intlify.dev/) | 有 | 未发现 |
| markstream-vue | Markdown 渲染 | [文档站](https://markstream-vue-docs.simonhe.me) | 有 | 未发现 |
| Shiki、Mermaid、KaTeX | 代码高亮、图表、公式 | [shiki.style](https://shiki.style/guide/)、[mermaid.js.org](https://mermaid.js.org/intro/)、[katex.org](https://katex.org/docs/) | 无 | 未发现 |
| dockview | 工作区面板 | [dockview.dev](https://dockview.dev/docs) | 有 | 未发现 |
| ECharts、xterm.js、Monaco | 图表、终端、编辑器 | [echarts.apache.org](https://echarts.apache.org/en/index.html)、[xtermjs.org](https://xtermjs.org/docs/)、[Monaco](https://microsoft.github.io/monaco-editor/) | 无 | 未发现 |
| lucide | 图标 | [lucide.dev](https://lucide.dev/guide/) | 有 | 未发现 |
| Vite、Vitest | 构建、测试 | [vite.dev](https://vite.dev/guide/)、[vitest.dev](https://vitest.dev/guide/) | 都有 | 未发现 |
| Electron、electron-vite | 桌面端 | [electronjs.org](https://www.electronjs.org/docs/latest)、[electron-vite.org](https://electron-vite.org/guide/) | 无 | 未发现 |
| Capacitor | 移动壳（开发中，尚未合入） | [capacitorjs.com](https://capacitorjs.com/docs)，返回见 [App](https://capacitorjs.com/docs/apis/app)，系统栏见 [System Bars](https://capacitorjs.com/docs/apis/system-bars) | 无 | [ionic-team/capacitor-skills](https://github.com/ionic-team/capacitor-skills)，由 Capacitor 的维护方 Ionic 团队发布，覆盖插件生成、Cordova 插件迁移与 UIScene 迁移，不涉及导航与返回。Capgo 发布的 Capacitor skills 是第三方的，只当线索 |

「未发现」的检索方法：在维护方的 GitHub 组织或账号下检索名称含 skill 的仓库，没有结果。

## 第三级：同类框架与设计系统

| 来源 | 适用的情况 |
|---|---|
| [Ionic Framework](https://ionicframework.com/docs) | 同样在 WebView 里做应用，返回与页面栈可对照[硬件返回键](https://ionicframework.com/docs/developing/hardware-back-button)与 [Vue 导航](https://ionicframework.com/docs/vue/navigation)。Memoh 不使用 Ionic 组件，只借鉴行为 |
| [IBM Carbon](https://carbondesignsystem.com) | 通知、表单等通用交互的取舍（U3 引用） |
| [Radix Colors](https://www.radix-ui.com/colors)、[shadcn/ui](https://ui.shadcn.com) | 弱边线等视觉惯例（U8 引用） |

## 维护本清单

- 库升级大版本、新增依赖，或维护方发布了 skill 或 llms.txt 时，更新对应的行，并更新文件
  开头的核实日期。
- 写入前实际打开链接、检索 skill；没核实的写「未核实」，不写「无」。
- 本文件只列来源，不写规则。某个来源推出的具体要求写进对应规则的「原因」和「本仓库的
  实现」。
