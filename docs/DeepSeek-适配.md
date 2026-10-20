# DeepSeek 深度适配

本文记录 DeepSentry 对 DeepSeek 最新 API 文档的适配边界。官方文档：

- API 文档：<https://api-docs.deepseek.com/zh-cn/>
- 模型与价格：<https://api-docs.deepseek.com/zh-cn/quick_start/pricing>
- 思考模式：<https://api-docs.deepseek.com/zh-cn/guides/thinking_mode>
- 工具调用：<https://api-docs.deepseek.com/zh-cn/guides/tool_calls>
- Responses API：<https://api-docs.deepseek.com/zh-cn/guides/responses_api>
- Anthropic API：<https://api-docs.deepseek.com/zh-cn/guides/anthropic_api>
- 图像理解：<https://api-docs.deepseek.com/zh-cn/guides/vision>

## 模型能力

| 模型 | 上下文 | 最大输出 | 图片 | 思考强度 | 默认 effort |
| --- | --- | --- | --- | --- | --- |
| `deepseek-flash`（V4.1 Flash 1017） | 1,048,576 | 393,216 | 支持 | low / high / max | high |
| `deepseek-v4-pro` | 1,048,576 | 393,216 | 不支持 | low / high / max | high |

旧 ID `deepseek-v4-flash`、`deepseek-v4-flash-vision-exp`、`deepseek-v4.1-flash` 仍可调用，按 Flash 能力处理。Anthropic 端点还会把 `claude-opus*` 映射到 `deepseek-v4-pro`，其它 `claude-*` 映射到 `deepseek-flash`。

## 配置

```yaml
thinking_mode: auto       # auto|enabled|disabled；DeepSeek 默认开启
reasoning_effort: auto    # auto|none|minimal|low|medium|high|xhigh|max|ultra
user_id: ""               # 可选，[A-Za-z0-9_-]{1,512}
reserved_output_tokens: 0 # 0=DeepSeek 默认 64K/128K；显式值优先
```

映射规则：

- `minimal` → `low`
- `medium`、`xhigh` → `high`
- `max`、`ultra` → `max`
- `auto` 不发送 `reasoning_effort`，使用服务端默认 `high`
- `none` 或 `thinking_mode: disabled` 关闭思考

TUI 可用 `/thinking status|on|off|low|high|max|auto` 查看和切换思考模式/强度；`/thinking budget 96k` 显式设置单次输出预算，`/thinking budget auto` 恢复按文档默认（思考 64K、max 128K、非思考 16K）。所有切换写入 `config.yaml` 并立即生效。

DeepSeek 可能让请求保持连接最长 10 分钟才真正开始推理。默认 `llm_timeout_sec: 120` 适合普通任务；使用 `reasoning_effort: max` 或高峰时段长任务时，建议显式设为 `300` 或更高，避免客户端过早超时重试。

## 三种协议的请求差异

| 能力 | Chat Completions | Responses API | Anthropic Messages |
| --- | --- | --- | --- |
| 端点 | `/chat/completions` | `/responses` | `/anthropic/v1/messages` |
| 思考开关 | `thinking: {"type":"enabled"}` | `reasoning: {"effort":"low/high/max/none"}` | `thinking: {"type":"enabled/disabled"}` |
| 强度 | `reasoning_effort` | `reasoning.effort` | `output_config.effort` |
| 用户隔离 | `user_id` | `user` | `metadata.user_id` |
| 原生工具 | `tools[].function` | `tools[]` 扁平 function | `tools[].input_schema` |
| 工具结果 | `role:"tool"` | `function_call_output` | `tool_result` |
| 推理回传 | `reasoning_content` | `reasoning` item | `thinking` block |
| 流式结束 | `data: [DONE]` | `response.completed/incomplete/failed` | SSE `message_stop` |

Chat 思考模式不支持 `temperature`、`presence_penalty`、`frequency_penalty`。DeepSentry 在思考模式下不发送 `temperature`；关闭思考后才会发送采样温度。`top_p` 默认不发送，交给模型自身规则（Flash 思考模式 0.95–1.0，非思考固定 1.0）。

## 原生工具调用

DeepSentry 会为 DeepSeek 的 Chat、Responses、Anthropic 三种协议发送真实函数 schema，并把返回的：

- Chat `tool_calls`
- Responses `function_call`
- Anthropic `tool_use`

统一转换为内部的 `ToolCall`，再走同一套风险审批和执行器。工具结果分别回传为 `role=tool`、`function_call_output`、`tool_result`，不会退化成纯文本。

如果服务端仍拒绝 `tools`，运行时会自动降级到 JSON 动作协议并重试；降级只影响传输格式，不绕过授权和风险确认。

## 思考模式历史

- Chat 带 `tools` 时，assistant 工具调用消息会完整回传 `reasoning_content`。
- Responses 会把 `reasoning_content` 转成 `reasoning` item（`reasoning_text`）。
- Anthropic 会把 `reasoning_content` 转成 `thinking` block，并保持 `tool_use` / `tool_result` 的配对。
- checkpoint 保存 `analyzer.Message.ReasoningContent`，恢复后仍可继续工具链。

## 图片与请求体

DeepSeek 文档限制：

- 格式：JPEG、PNG、GIF、WebP，按文件内容判断
- 单张内联图片 32 MiB；Files API file_id 64 MiB
- 单次请求体 48 MiB；图片只能出现在 user 消息
- 单请求最多 600 张；单边最长 8192 像素

DeepSentry 当前使用本地图片 base64 内联，单张限制 20 MiB、单条全局限制 40 MiB；DeepSeek 请求在发送前会按 base64 后的实际 JSON 大小检查 48 MiB 上限。图片只允许出现在 user 消息；system/assistant 图片会在本地被拒绝，避免 DeepSeek 400。

## 结束原因与 usage

- 识别 `stop`、`length`、`content_filter`、`tool_calls`、`insufficient_system_resource`、`aborted`
- `length` / `content_filter` / `insufficient_system_resource` / `aborted` 返回带 usage 和已生成内容的部分结果错误，便于上层判断重试或报告
- 解析 `prompt_cache_hit_tokens`、`prompt_cache_miss_tokens`、`prompt_tokens_details.cached_tokens`、`completion_tokens_details.reasoning_tokens`
- Responses 解析 `input_tokens_details.cached_tokens`、`output_tokens_details.reasoning_tokens`
- Anthropic 解析 `cache_read_input_tokens`、`cache_creation_input_tokens`

## 已验证

使用真实 DeepSeek API 验证：

- Chat Completions 原生工具调用
- Responses API 原生 `function_call` 与工具结果回传
- Anthropic Messages 原生 `tool_use` / `tool_result`
- Anthropic 端点从 `https://api.deepseek.com` 自动补全为 `/anthropic/v1/messages`
- 思考模式与 `output_config.effort` 参数被接受
- Search API 真实检索

对应 opt-in 测试：

```bash
DEEPSENTRY_LIVE_DEEPSEEK=1 go test ./internal/analyzer -run TestDeepSeekLiveNativeToolProtocols -count=1
DEEPSENTRY_LIVE_SEARCH=1 go test ./internal/builtin -run TestWebSearchLiveDeepSeek -count=1
```

这些测试会真实计费，默认跳过。

## 尚未接入的能力

以下能力不影响当前 Agent 主路径，但属于可选深化项：

- DeepSeek Files API（上传/复用大图片、file_id 引用）
- Chat 前缀续写（Beta `prefix: true`）与 FIM `/completions`
- Responses / Anthropic 的服务端 `web_search`（目前使用独立的 `/v1/tools/search`）
- `strict` 工具模式（Beta，要求所有参数 required 且 `additionalProperties: false`）
- `logprobs` / `top_logprobs` 输出
