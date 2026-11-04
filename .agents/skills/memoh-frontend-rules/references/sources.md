# 依据来源清单

`SKILL.md` 的「依据来源」规定了优先级。本文件先列出官方的权威来源和只作对照的来源，再说明
清单里没有时怎么找，最后按领域列出该读哪几篇。表中的链接、llms.txt 和 skill 都在 2026-11-04
实际打开或检索过。

## 目录

- [官方来源](#官方来源), L:18
  - [平台](#平台), L:27
  - [Web 标准](#web-标准), L:40
  - [框架与库](#框架与库), L:50
- [只作对照的来源](#只作对照的来源), L:82
- [清单里没有时怎么找](#清单里没有时怎么找), L:93
- [按领域查找](#按领域查找), L:120
- [维护本清单](#维护本清单), L:135

## 官方来源

拿不准一个行为或一个库的用法时，先到这里列出的地方查。平台和 Web 标准决定行为对不对，
框架与库的文档决定在本仓库怎么实现。

表中「llms.txt」写「有」的，路径是文档站根目录下的 `/llms.txt`，「另有全文版」指还有
`/llms-full.txt`。「官方 skill」指维护方自己发布的 skill；「未发现」指在维护方的 GitHub
组织或账号下检索名称含 skill 的仓库，没有结果。

### 平台

| 名称 | 地址 | llms.txt | 官方 skill |
|---|---|---|---|
| Android Developers | https://developer.android.com | 无 | [android/skills](https://github.com/android/skills)（见下） |
| Material Design 3 | https://m3.material.io | 无 | 未发现 |
| Apple Developer Documentation | https://developer.apple.com/documentation | 无 | 未发现 |
| Apple Human Interface Guidelines | https://developer.apple.com/design/human-interface-guidelines | 无 | 未发现 |

android/skills 由 Google 安卓团队发布，用 Android CLI 安装（`android skills add <name>`，见
[Android skills](https://developer.android.com/tools/agents/android-skills)）。内容以 Jetpack
Compose 为主；移动壳是 WebView，只取讲平台行为的部分，例如 `edge-to-edge`、`navigation-event`。

### Web 标准

| 名称 | 地址 |
|---|---|
| WHATWG HTML | https://html.spec.whatwg.org/multipage/ |
| WCAG 2.2 | https://www.w3.org/TR/WCAG22/ |
| WAI-ARIA Authoring Practices Guide | https://www.w3.org/WAI/ARIA/apg/ |
| MDN Web Docs（标准的参考说明，不是标准本身；与标准冲突时以标准为准） | https://developer.mozilla.org/en-US/docs/Web |
| web.dev（Google 的 Web 性能与体验指南） | https://web.dev |

### 框架与库

版本以 `apps/web/package.json`、`apps/desktop/package.json` 等锁定的为准，打开对应大版本
的文档。

| 名称 | 用在哪 | 地址 | llms.txt | 官方 skill |
|---|---|---|---|---|
| Vue | 全部 | https://vuejs.org | 有，另有全文版 | 未发现（见下） |
| Vue Router | 路由与历史 | https://router.vuejs.org | 有 | 未发现（见下） |
| Pinia | 客户端 store | https://pinia.vuejs.org | 有 | 未发现（见下） |
| Pinia Colada | 服务端数据 | https://pinia-colada.esm.dev | 有 | 未发现 |
| Reka UI | `@felinic/ui` 内部 | https://reka-ui.com | 有，另有全文版 | 未发现 |
| VueUse | 通用 composable | https://vueuse.org | 无 | [vueuse/skills](https://github.com/vueuse/skills)（`vueuse-functions`） |
| Tailwind CSS | 样式 | https://tailwindcss.com/docs | 无 | 未发现 |
| vee-validate | 表单 | https://vee-validate.logaretm.com/v4/ | 无 | 未发现 |
| Zod | 校验 | https://zod.dev | 有 | 未发现 |
| vue-i18n | 三语文案 | https://vue-i18n.intlify.dev | 有 | 未发现 |
| markstream-vue | Markdown 渲染 | https://markstream-vue-docs.simonhe.me | 有 | 未发现 |
| Shiki / Mermaid / KaTeX | 代码高亮、图表、公式 | https://shiki.style 、https://mermaid.js.org 、https://katex.org | 无 | 未发现 |
| dockview | 工作区面板 | https://dockview.dev | 有 | 未发现 |
| ECharts / xterm.js / Monaco | 图表、终端、编辑器 | https://echarts.apache.org 、https://xtermjs.org 、https://microsoft.github.io/monaco-editor/ | 无 | 未发现 |
| lucide | 图标 | https://lucide.dev | 有 | 未发现 |
| Vite / Vitest | 构建、测试 | https://vite.dev 、https://vitest.dev | 都有 | 未发现 |
| Electron / electron-vite | 桌面端 | https://www.electronjs.org/docs/latest 、https://electron-vite.org | 无 | 未发现 |
| Capacitor | 移动壳（开发中，尚未合入） | https://capacitorjs.com/docs | 无 | [ionic-team/capacitor-skills](https://github.com/ionic-team/capacitor-skills)（见下） |

- Vue、Vue Router、Pinia：社区的 [vuejs-ai/skills](https://github.com/vuejs-ai/skills) 含这三者的
  best practices，但它在 README 中自述为非官方实验，只当线索。
- Capacitor：ionic-team/capacitor-skills 由 Capacitor 的维护方 Ionic 团队发布，覆盖插件生成、
  Cordova 插件迁移与 UIScene 迁移，不涉及导航与返回。Capgo 发布的 Capacitor skills 是第三方的，
  只当线索。

## 只作对照的来源

下面这些不是本仓库所用技术的维护方，不属于官方来源，只在同类问题上作对照。引用时说明为什么
适用于本仓库。

| 名称 | 对照什么 | 地址 |
|---|---|---|
| Ionic Framework | 同样在 WebView 里做应用：返回与页面栈 | https://ionicframework.com/docs |
| IBM Carbon | 通知、表单等通用交互（U3 引用） | https://carbondesignsystem.com |
| Radix Colors / shadcn/ui | 弱边线等视觉惯例（U8 引用） | https://www.radix-ui.com/colors 、https://ui.shadcn.com |

## 清单里没有时怎么找

遇到清单没覆盖的平台行为、新引入的库，或一个库里清单没写到的部分，按下面的顺序找：

1. **先判断这件事归谁管。** 用户在别的应用里也会碰到的行为（返回、手势、系统栏、键盘、
   通知、权限、无障碍）归平台；浏览器提供的能力和语义归 Web 标准；某个库怎么用、有什么
   限制归这个库的维护方。
2. **平台的问题去平台厂商的站点查。** Android 在 developer.android.com 的 Guide 与
   Design 下，Apple 在 developer.apple.com 的 Documentation 与 Human Interface
   Guidelines 下。Web 的问题先查 WHATWG 或 W3C 的规范，再用 MDN 看说明和浏览器支持。
3. **库的问题先找到它的维护方。** 运行 `pnpm view <包名> homepage repository.url`，或看包
   自己 `package.json` 里的这两个字段。文档站和源码仓库属于同一个维护方（同一个 GitHub
   组织或账号、同一个域名），它的文档才算官方。打开与锁定版本相同大版本的文档。
4. **官方文档没写清时，读维护方自己的源码、issue 和发布说明。** 这些可以确认实际行为，
   引用时注明出处是源码、issue 还是发布说明，不当成文档的承诺。平台组件同样如此：WebView
   和 Chrome 的行为看 Chromium 的源码与 issue（https://source.chromium.org 、
   https://issues.chromium.org），Android 系统行为看 AOSP 源码（https://cs.android.com）。
5. **找给 agent 读的入口。** 在文档站根目录试 `/llms.txt` 和 `/llms-full.txt`；在维护方的
   GitHub 组织或账号下找 skill 仓库。发布方不是维护方的 skill 一律算第三方，只当线索。
6. **以下都不算官方来源**：第三方教程和博客、问答帖、文档镜像和聚合站、其他产品的源码与
   界面、模型自己的总结。它们可以提示去查哪一篇，但结论要回到上面几类来源核对。
7. **找到之后写回本清单。** 核实链接后补进对应的表，并更新文件开头的核实日期。

引用时写到能定位的程度：文档标题加小节名，或规范的条款号，并附链接。规则里的引用写进
该条的「原因」（见 `upkeep.md` § 规则的组成部分）。抓取不到正文的页面（例如由脚本渲染的
Material 3 站点），如实写「未读到原文」，不凭印象引用。

## 按领域查找

| 领域 | 读这几篇 |
|---|---|
| 返回、手势与页面栈 | Android：[Principles of navigation](https://developer.android.com/guide/navigation/principles)、[Custom back navigation](https://developer.android.com/guide/navigation/custom-back)、[Predictive back](https://developer.android.com/guide/navigation/custom-back/predictive-back-gesture)；Apple HIG：[Modality](https://developer.apple.com/design/human-interface-guidelines/modality)；WHATWG HTML：Session history 与 [Close requests](https://html.spec.whatwg.org/multipage/interaction.html)；Vue Router：[History modes](https://router.vuejs.org/guide/essentials/history-mode)；Capacitor：[App](https://capacitorjs.com/docs/apis/app)；对照 Ionic：[Hardware Back Button](https://ionicframework.com/docs/developing/hardware-back-button)、[Vue Navigation](https://ionicframework.com/docs/vue/navigation) |
| 系统栏、全面屏与安全区 | Android：[Edge-to-edge](https://developer.android.com/develop/ui/views/layout/edge-to-edge)；Apple HIG：[Layout](https://developer.apple.com/design/human-interface-guidelines/layout)；Capacitor：[System Bars](https://capacitorjs.com/docs/apis/system-bars) |
| WebView 与套壳 | Android：[WebView](https://developer.android.com/develop/ui/views/layout/webapps/webview)；Capacitor 文档；对照 Ionic |
| 无障碍、对比度与焦点 | WCAG 2.2（条款号写进 U8）；WAI-ARIA APG 中对应控件的模式；Reka UI 的 Accessibility 说明 |
| 浮层、菜单与弹窗 | WAI-ARIA APG 的 Dialog、Menu 模式；Reka UI 对应组件；Apple HIG：Modality；Material 3 |
| 加载与反馈时限 | web.dev：[RAIL](https://web.dev/articles/rail)；Material 3（U4 引用） |
| 通知与 Toast | Material 3；对照 IBM Carbon（U3 引用） |
| 服务端数据与缓存 | Pinia Colada |
| 表单与校验 | vee-validate；Zod |
| 桌面端窗口、托盘与菜单 | Electron；electron-vite |

## 维护本清单

- 库升级大版本、新增依赖，或维护方发布了 skill 或 llms.txt 时，更新对应的行，并更新文件
  开头的核实日期。
- 写入前实际打开链接、检索 skill；没核实的写「未核实」，不写「无」。
- 本文件只列来源，不写规则。某个来源推出的具体要求写进对应规则的「原因」和「本仓库的
  实现」。
