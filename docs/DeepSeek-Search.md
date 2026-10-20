# DeepSeek 联网搜索（Search API）

DeepSentry 通过内置 `web_search` 工具调用 DeepSeek 官方 Search API，为 Agent 补充实时网页、新闻、版本发布和漏洞公告证据。

官方文档：

- 使用指南：<https://api-docs.deepseek.com/zh-cn/guides/search>
- API 参考：<https://api-docs.deepseek.com/zh-cn/api/search>
- 发布说明：<https://api-docs.deepseek.com/zh-cn/news/news261017>

## 接口

```http
POST https://api.deepseek.com/v1/tools/search
Authorization: Bearer <DeepSeek API Key>
Content-Type: application/json
```

请求字段：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `query` | string | 是 | 1–128 个 Unicode 字符，支持 `site:`、`-site:`、`-词`、`"精确短语"`、`lang:`、`after:`、`before:` |
| `count` | integer | 否 | 1–50，默认取 `search.count`（8）；不影响单次计费 |
| `ori_question` | string | 否 | 用户原始问题，1–128 字符，帮助理解搜索意图 |
| `localization` | string | 否 | 优先本地化结果的 IP 地址 |
| `need_content` | boolean | 否 | 是否返回网页正文；默认取 `search.need_content`（false） |

响应中的每条结果包含 `title`、`url`、`snippet`、`summary`、`content`、`site_name`、`site_icon`、`date`、`rerank_score` 和 `authority`。DeepSentry 会把标题、来源 URL、摘要、评分、权威度和可选正文转换成模型可读的结构化文本。

## 配置

```yaml
search:
  enabled: true
  base_url: ""        # 留空：DeepSeek 主模型使用 https://api.deepseek.com
  api_key: ""         # 留空：复用 DeepSeek 主模型 api_key
  count: 8
  need_content: false
  timeout_sec: 20
  localization: ""
```

凭据规则：

- 主模型 `provider: deepseek` 时，默认复用其 `api_key`。
- 主模型是其它厂商时，必须显式设置 `search.api_key`，不会把其它厂商的 Key 发到 DeepSeek。
- 自定义网关可同时设置 `search.base_url` 和 `search.api_key`；base URL 可填到 `/v1`，DeepSentry 会自动补 `/tools/search`。

## 开关与按需加载

- 初始化向导会询问是否启用 DeepSeek 联网搜索。
- TUI `输入 /search` 查看状态，`/search on` 开启，`/search off` 关闭；开关写入 `config.yaml` 并立即生效。
- `web_search` 不占用固定原生 schema 槽位，只有任务需要实时网页证据时，Agent 才通过 `tool_catalog(name="web_search")` 发现并加载。
- 关闭或缺少凭据时，`web_search` 会从工具目录和原生 schema 中消失，`agent_action` 兼容路径也无法调用。

## 错误、重试与边界

- `408`、`429`、`5xx` 自动重试，最多 3 次，使用短退避并遵守上下文超时。
- `400` 参数错误、`401` 鉴权失败、`402` 余额不足、`403` 安全策略拒绝、`404` 无可提取内容、`429` 限速会返回可操作的中文错误。
- Search 与 Fetch 分别有账号级 QPS 限制：Search 100 QPS，Fetch 25 QPS。
- 按成功请求计费：5 元/1000 次；无结果、失败或超时不计费，`count` 不影响单价。
- 搜索查询会发送到 DeepSeek。涉密环境请关闭 `search.enabled`，或改用受控检索服务。
- 单次输出默认截断到 64 KiB，单条正文最多 8000 字符；需要完整原文时缩小 `count` 并只对关键结果开启 `need_content`。

## 验证

启用后可以让 Agent 执行：

> 用 web_search 查一下 DeepSeek V4.1 Flash 1017 的发布说明，保留来源 URL。

也可以直接验证接口：

```bash
curl https://api.deepseek.com/v1/tools/search \
  -H "Authorization: Bearer $DEEPSEEK_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"query":"DeepSeek V4.1 Flash 1017 发布 site:api-docs.deepseek.com","count":3}'
```
