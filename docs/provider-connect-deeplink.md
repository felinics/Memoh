# Provider Connect Deep Link

第三方服务（API 网关、聚合平台、中转站等）可以通过一条 deep link，把 provider 预填进 Memoh。用户在确认页看到将要添加的内容，点击确认后才会保存——Memoh 不会静默接受 URL 里的凭据。

## URL 格式

```
https://<memoh-host>/providers/connect#payload=<base64url(json)>
```

- `memoh-host`：Memoh Web 的地址（Memoh Cloud 用固定域名，自托管用实例自己的域名）。
- payload 放在 **fragment**（`#` 之后）：浏览器不会把 fragment 发给服务器，API Key 不会进入 access log 或 Referer。
- payload 是 **UTF-8 JSON 的 base64url 编码**（无 padding，`-`/`_` 替换 `+`/`/`）。

## Payload schema（v1）

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `v` | 是 | 固定为 `1`。未知版本会被拒绝。 |
| `kind` | 是 | 固定为 `memoh-provider-import`。 |
| `base_url` | 是 | provider 的 base URL，仅接受 `http:` / `https:`。 |
| `api_key` | 是 | 明文 key，确认页只显示后 4 位。 |
| `client_type` | 否 | API 格式，缺省 `openai-completions`。可选：`openai-completions` / `openai-responses` / `anthropic-messages` / `google-generative-ai`。OAuth 类（`openai-codex`、`github-copilot`）不支持预填。 |
| `name` | 否 | 显示名，缺省取 `base_url` 的 host。 |
| `template` | 否 | 注册表模板 key（YAML 文件名，如 `newapi`，大小写不敏感）；命中时继承其 icon 与默认配置，不命中则创建自定义 provider。`client_type` 永远以 payload 为准：模板仅在其 driver 与 `client_type` 一致时才算命中，不一致时降级为自定义创建。 |

示例（解码后）：

```json
{
  "v": 1,
  "kind": "memoh-provider-import",
  "name": "My New API",
  "base_url": "https://api.example.com/v1",
  "api_key": "sk-xxx",
  "client_type": "openai-completions",
  "template": "newapi"
}
```

## 确认后的行为

1. 按 `template` 命中情况走模板创建或自定义创建；
2. 自动触发一次 import-models（从 `base_url` 的 `/models` 拉取模型列表），失败不阻塞，可在详情页手动重试；
3. 跳转到该 provider 的详情页。

## 建议：第三方如何生成链接

网关侧建议为用户**新建一个专用令牌/key**（限定分组、额度、过期时间），而不是复用已有 key，再拼链接跳转。

## 已知限制

- 未登录用户会被重定向到登录页，登录后 fragment 不会回带——生成方应假定用户已登录目标 Memoh。
- 确认页解析完 payload 后会用 `history.replaceState` 抹掉地址栏 fragment（避免 key 留在浏览器历史/历史同步里），因此**刷新页面会报「链接缺少 payload」**——这是有意行为，链接是一次跳转用的，第三方应能随时重新生成。
- payload 不应携带模型清单等大数据：模型由 Memoh 创建后自行拉取，URL 保持短。

## 安全说明

链接即凭据：payload 只在 fragment 中传输，不进服务端日志和 Referer，但会短暂出现在浏览器地址栏。残余风险是浏览器历史和链接转发，缓解方式：确认页展示掩码 key 并要求显式确认；第三方应为用户**新建限分组/限额度的专用 key**，泄露时吊销即可。
