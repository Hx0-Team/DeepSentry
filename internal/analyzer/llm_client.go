package analyzer

import (
	"ai-edr/internal/config"
	"ai-edr/internal/mcp"
	deepsentrytools "ai-edr/internal/tools"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// LLMResult LLM 调用结果
type LLMResult struct {
	Content string
	// ReasoningContent must be preserved on assistant tool-call messages for
	// thinking-mode OpenAI-compatible providers that validate the next turn.
	ReasoningContent string
	ToolCallName     string
	ToolCallArgs     string
	ToolCallID       string
	ToolCalls        []LLMToolCall
	Usage            TokenUsage
	ModelID          string
	Attempts         int
	Failovers        int
}

type LLMToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type PartialStreamError struct {
	Result LLMResult
	Err    error
}

func (e *PartialStreamError) Error() string { return "partial stream response: " + e.Err.Error() }
func (e *PartialStreamError) Unwrap() error { return e.Err }

type TokenUsage struct {
	PromptTokens       int  `json:"prompt_tokens,omitempty"`
	CompletionTokens   int  `json:"completion_tokens,omitempty"`
	TotalTokens        int  `json:"total_tokens,omitempty"`
	CacheReadTokens    int  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int  `json:"cache_write_tokens,omitempty"`
	ReasoningTokens    int  `json:"reasoning_tokens,omitempty"`
	CacheUsageReported bool `json:"cache_usage_reported,omitempty"`
}

// Normalize provider usage without treating cache reads as free or adding
// them twice to prompt_tokens. DeepSeek and OpenAI use different field names.
func (u *TokenUsage) UnmarshalJSON(data []byte) error {
	type plain TokenUsage
	var raw struct {
		plain
		Hit     *int `json:"prompt_cache_hit_tokens"`
		Miss    *int `json:"prompt_cache_miss_tokens"`
		Details struct {
			Cached *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*u = TokenUsage(raw.plain)
	if raw.Hit != nil {
		u.CacheReadTokens = *raw.Hit
		u.CacheUsageReported = true
	}
	if raw.Miss != nil {
		u.CacheUsageReported = true
	}
	if raw.Details.Cached != nil {
		u.CacheReadTokens = *raw.Details.Cached
		u.CacheUsageReported = true
	}
	if raw.CompletionDetails.Reasoning > 0 {
		u.ReasoningTokens = raw.CompletionDetails.Reasoning
	}
	return nil
}

func (u TokenUsage) HasAny() bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// CallLLMWithRetry 带重试与降级的统一 LLM 调用；onStream 非 nil 时在 OpenAI 兼容 JSON 模式下启用 SSE 流式
func CallLLMWithRetry(messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	return CallLLMWithRetryContext(context.Background(), messages, useNativeTools, onStream)
}

func CallLLMWithRetryContext(ctx context.Context, messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	return callLLMWithUsageContext(ctx, messages, useNativeTools, onStream, nil)
}

// Report every provider-returned usage record, including an empty/partial
// response that is subsequently retried. Missing usage is never estimated.
func callLLMWithUsageContext(ctx context.Context, messages []Message, useNativeTools bool, onStream func(string), onUsage func(TokenUsage)) (LLMResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withRequestToolsCache(ctx)
	baseCfg := config.GlobalConfig
	models := baseCfg.EffectiveModels()
	hasImages := MessagesHaveImages(messages)
	var lastErr error
	totalAttempts := 0
	compatibleModels := 0
	for modelIndex, model := range models {
		cfg := baseCfg.ConfigForModel(model)
		if hasImages && !cfg.EffectiveModelCapabilities().SupportsVision {
			continue
		}
		compatibleModels++
		retries := model.MaxRetries
		for attempt := 0; attempt <= retries; attempt++ {
			totalAttempts++
			if attempt > 0 {
				delay := retryDelayForError(attempt, lastErr)
				if delay > maxLLMRetryWait {
					break // Honor the server hint; do not hammer it before reset.
				}
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return LLMResult{}, ctx.Err()
				}
			}

			result, err := callLLMOnce(ctx, cfg, messages, useNativeTools, onStream)
			usage := result.Usage
			var partial *PartialStreamError
			if !usage.HasAny() && errors.As(err, &partial) {
				usage = partial.Result.Usage
			}
			if onUsage != nil && usage.HasAny() {
				onUsage(usage)
			}
			if err == nil && !hasActionableLLMOutput(result) {
				err = errors.New("empty response: no content or tool call")
			}
			if err == nil {
				result.ModelID = model.ID
				result.Attempts = totalAttempts
				result.Failovers = modelIndex
				return result, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return LLMResult{}, ctx.Err()
			}
			if !isRetryable(err) {
				break
			}
		}
		if modelIndex+1 >= len(models) || !shouldFailover(baseCfg, lastErr) {
			break
		}
	}
	if hasImages && compatibleModels == 0 {
		return LLMResult{}, errors.New("当前模型链没有启用视觉能力；请将视觉模型的 vision_mode 设为 enabled，或使用名称包含 vision/VL 的模型")
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的模型")
	}
	return LLMResult{}, fmt.Errorf("LLM 调用失败(总尝试 %d 次): %w", totalAttempts, lastErr)
}

func hasActionableLLMOutput(result LLMResult) bool {
	if strings.TrimSpace(result.Content) != "" || strings.TrimSpace(result.ToolCallName) != "" {
		return true
	}
	for _, call := range result.ToolCalls {
		if strings.TrimSpace(call.Name) != "" {
			return true
		}
	}
	return false
}

func callLLMOnce(ctx context.Context, cfg config.Config, messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	native := NativeToolTransportAvailable(cfg, useNativeTools)
	nativeResponses := useNativeTools && cfg.IsOpenAIResponses() && cfg.IsDeepSeek()
	nativeAnthropic := useNativeTools && cfg.IsAnthropic() && cfg.IsDeepSeek()
	if useNativeTools && !native && !nativeResponses && !nativeAnthropic {
		messages = withJSONActionProtocol(messages)
	}
	if cfg.IsAnthropic() {
		result, err := callAnthropic(ctx, cfg, messages, nativeAnthropic)
		if err != nil && nativeAnthropic && isToolsUnsupported(err) {
			return callAnthropic(ctx, cfg, withJSONActionProtocol(messages), false)
		}
		return result, err
	}
	if cfg.IsOpenAIResponses() {
		result, err := callOpenAIResponses(ctx, cfg, messages, nativeResponses)
		if err != nil && nativeResponses && isToolsUnsupported(err) {
			return callOpenAIResponses(ctx, cfg, withJSONActionProtocol(messages), false)
		}
		return result, err
	}
	if native {
		result, err := callOpenAICompatible(ctx, cfg, messages, true, onStream)
		if err != nil && isStreamUnsupported(err) && onStream != nil {
			result, err = callOpenAICompatible(ctx, cfg, messages, true, nil)
		}
		if err != nil && isToolsUnsupported(err) {
			if explicitlyUnsupported(err, "tool") {
				rememberUnsupported(cfg, "tools")
			}
			return callOpenAICompatible(ctx, cfg, withJSONActionProtocol(messages), false, onStream)
		}
		return result, err
	}
	if onStream != nil && cfg.IsOpenAICompatible() {
		result, err := callOpenAICompatible(ctx, cfg, messages, false, onStream)
		if err != nil && isStreamUnsupported(err) {
			return callOpenAICompatible(ctx, cfg, messages, false, nil)
		}
		return result, err
	}
	return callOpenAICompatible(ctx, cfg, messages, false, nil)
}

func NativeToolTransportAvailable(cfg config.Config, requested bool) bool {
	return requested && cfg.IsOpenAICompatible() && !cfg.IsOpenAIResponses() && !isGPT6ReasoningModel(cfg.ModelName) && !isKnownUnsupported(cfg, "tools")
}

func withJSONActionProtocol(messages []Message) []Message {
	out := append([]Message(nil), messages...)
	return append(out, Message{Role: "system", Content: `【当前实际传输：JSON动作兼容】本次请求没有提供原生函数schema；优先遵守此实际传输规则。只返回一个纯JSON动作对象，不能输出伪工具调用、Markdown或自然语言计划。执行：{"action":"execute","command":"实际命令","thought":"简短目的"}；工具：{"action":"tool","tool_name":"精确工具名","tool_args":{}}；加载Skill：{"action":"load_skill","skill_name":"精确名称"}；完成：{"action":"finish","final_report":"已核验的结果"}。遵守原有授权与安全限制。`})
}

const unsupportedFeatureTTL = 5 * time.Minute

type unsupportedFeatureKey struct {
	url, model, feature string
}

var unsupportedFeatures sync.Map

func featureKey(cfg config.Config, feature string) unsupportedFeatureKey {
	return unsupportedFeatureKey{url: config.NormalizeChatURL(cfg.ApiURL), model: cfg.ModelName, feature: feature}
}

func isKnownUnsupported(cfg config.Config, feature string) bool {
	key := featureKey(cfg, feature)
	value, ok := unsupportedFeatures.Load(key)
	if !ok {
		return false
	}
	expires, ok := value.(time.Time)
	if ok && time.Now().Before(expires) {
		return true
	}
	unsupportedFeatures.CompareAndDelete(key, value)
	return false
}

func rememberUnsupported(cfg config.Config, feature string) {
	unsupportedFeatures.Store(featureKey(cfg, feature), time.Now().Add(unsupportedFeatureTTL))
}

func explicitlyUnsupported(err error, parameter string) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, parameter) {
		return false
	}
	for _, marker := range []string{"unsupported", "not support", "unknown", "unrecognized", "not allowed", "invalid parameter", "extra inputs"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func shouldFailover(cfg config.Config, err error) bool {
	kind := classifyLLMError(err)
	if len(cfg.ModelRouting.FailoverOn) == 0 {
		return kind == "rate_limit" || kind == "timeout" || kind == "server_error" || kind == "connection" || kind == "invalid_output"
	}
	for _, allowed := range cfg.ModelRouting.FailoverOn {
		if strings.EqualFold(strings.TrimSpace(allowed), kind) {
			return true
		}
	}
	return false
}

func classifyLLMError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var quota interface{ IsQuotaExhausted() bool }
	if errors.As(err, &quota) && quota.IsQuotaExhausted() {
		return "quota_exhausted"
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) {
		switch code := status.HTTPStatusCode(); {
		case code == 401 || code == 403:
			return "authentication"
		case code == 429:
			return "rate_limit"
		case code == 408:
			return "timeout"
		case code >= 500 && code <= 599:
			return "server_error"
		default:
			return "invalid_request"
		}
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "429"), strings.Contains(s, "rate limit"):
		return "rate_limit"
	case strings.Contains(s, "500"), strings.Contains(s, "502"), strings.Contains(s, "503"), strings.Contains(s, "504"):
		return "server_error"
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "connection reset"), strings.Contains(s, "eof"), strings.Contains(s, "broken pipe"):
		return "connection"
	case strings.Contains(s, "parse"), strings.Contains(s, "empty response"):
		return "invalid_output"
	default:
		return "unknown"
	}
}

// llmRetryDelay 使并发 Agent 的重试错开，避免限流期间在整秒上同时再次冲击供应商。
func llmRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	base := time.Duration(attempt*attempt) * time.Second
	jitter, err := rand.Int(rand.Reader, big.NewInt(501))
	if err != nil {
		return base
	}
	return base + time.Duration(jitter.Int64())*time.Millisecond
}

type responsesRequest struct {
	Model           string              `json:"model"`
	Input           interface{}         `json:"input"`
	Instructions    string              `json:"instructions,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	Tools           []responsesTool     `json:"tools,omitempty"`
	User            string              `json:"user,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesTool struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type responsesInputMessage struct {
	Role    string                  `json:"role"`
	Content []responsesContentBlock `json:"content"`
}

type responsesContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutput struct {
	Type   string      `json:"type"`
	CallID string      `json:"call_id"`
	Output interface{} `json:"output"`
}

type responsesReasoningItem struct {
	Type    string                  `json:"type"`
	Content []responsesContentBlock `json:"content"`
}

type responsesResponse struct {
	Status     string `json:"status"`
	OutputText string `json:"output_text"`
	Output     []struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Role      string `json:"role"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	} `json:"error"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
		InputDetails struct {
			Cached *int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func callOpenAIResponses(ctx context.Context, cfg config.Config, messages []Message, useNativeTools bool) (LLMResult, error) {
	url := config.NormalizeAPIURL(cfg.ApiURL, config.ProtocolOpenAIResponses)
	input, err := buildOpenAIResponsesInput(messages)
	if err != nil {
		return LLMResult{}, err
	}
	reqBody := responsesRequest{
		Model:           cfg.ModelName,
		Input:           input,
		MaxOutputTokens: cfg.EffectiveModelCapabilities().ReservedOutputTokens,
	}
	if !isGPT6ReasoningModel(cfg.ModelName) && !(cfg.IsDeepSeek() && cfg.EffectiveThinkingMode() == "enabled") {
		temp := effectiveTemperature(cfg)
		reqBody.Temperature = &temp
	}
	if cfg.IsDeepSeek() {
		effort := cfg.EffectiveReasoningEffort()
		if cfg.EffectiveThinkingMode() == "disabled" {
			effort = "none"
		}
		if effort != "" {
			reqBody.Reasoning = &responsesReasoning{Effort: effort}
		}
		reqBody.User = strings.TrimSpace(cfg.UserID)
	}
	if useNativeTools {
		definitions := nativeToolDefinitionsForRequest(cfg, messages)
		reqBody.Tools = make([]responsesTool, 0, len(definitions))
		for _, definition := range definitions {
			reqBody.Tools = append(reqBody.Tools, responsesTool{
				Type: "function", Name: definition.Function.Name,
				Description: definition.Function.Description, Parameters: definition.Function.Parameters,
			})
		}
	}
	body, status, headers, err := doHTTPPostReply(ctx, url, cfg, reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if status != 200 {
		return LLMResult{}, apiResponseError(status, headers, body)
	}
	var rr responsesResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return LLMResult{}, err
	}
	usage := TokenUsage{
		PromptTokens:     rr.Usage.InputTokens,
		CompletionTokens: rr.Usage.OutputTokens,
		TotalTokens:      rr.Usage.TotalTokens,
		ReasoningTokens:  rr.Usage.OutputDetails.Reasoning,
	}
	if rr.Usage.InputDetails.Cached != nil {
		usage.CacheReadTokens = *rr.Usage.InputDetails.Cached
		usage.CacheUsageReported = true
	}
	if rr.Error != nil {
		return LLMResult{Usage: usage}, errors.New(rr.Error.Message)
	}
	var content, reasoning strings.Builder
	var calls []LLMToolCall
	for _, out := range rr.Output {
		switch out.Type {
		case "function_call":
			id := strings.TrimSpace(out.CallID)
			if id == "" {
				id = strings.TrimSpace(out.ID)
			}
			if strings.TrimSpace(out.Name) != "" {
				calls = append(calls, LLMToolCall{ID: id, Name: out.Name, Arguments: out.Arguments})
			}
		case "reasoning":
			for _, c := range out.Content {
				if c.Type == "reasoning_text" {
					reasoning.WriteString(c.Text)
				}
			}
		}
		for _, c := range out.Content {
			if c.Type == "output_text" || c.Type == "text" || c.Type == "input_text" {
				content.WriteString(c.Text)
			}
		}
	}
	if content.Len() == 0 && strings.TrimSpace(rr.OutputText) != "" {
		content.WriteString(rr.OutputText)
	}
	result := LLMResult{Content: content.String(), ReasoningContent: reasoning.String(), ToolCalls: calls, Usage: usage}
	if len(calls) > 0 {
		result.ToolCallID = calls[0].ID
		result.ToolCallName = calls[0].Name
		result.ToolCallArgs = calls[0].Arguments
	}
	if strings.EqualFold(rr.Status, "incomplete") {
		reason := strings.TrimSpace(rr.IncompleteDetails.Reason)
		if reason == "" {
			reason = "达到输出上限"
		}
		return result, &PartialStreamError{Result: result, Err: fmt.Errorf("Responses 响应未完成: %s", reason)}
	}
	if strings.EqualFold(rr.Status, "failed") {
		return result, errors.New("Responses 响应失败")
	}
	if content.Len() == 0 && len(calls) == 0 {
		return LLMResult{Usage: usage}, errors.New("empty responses output")
	}
	return result, nil
}

func buildOpenAIResponsesInput(messages []Message) (interface{}, error) {
	items := make([]interface{}, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "system", "user", "assistant":
			if role == "assistant" && strings.TrimSpace(message.ReasoningContent) != "" {
				items = append(items, responsesReasoningItem{
					Type:    "reasoning",
					Content: []responsesContentBlock{{Type: "reasoning_text", Text: message.ReasoningContent}},
				})
			}
			blocks := make([]responsesContentBlock, 0, 1+len(message.Attachments))
			text := strings.TrimSpace(message.Content)
			if text != "" {
				blockType := "input_text"
				if role == "assistant" {
					blockType = "output_text"
				}
				blocks = append(blocks, responsesContentBlock{Type: blockType, Text: text})
			}
			if len(message.Attachments) > 0 {
				if role != "user" {
					return nil, fmt.Errorf("Responses 图片附件只能出现在 user 消息中，当前 role=%s", role)
				}
				images, err := materializeImages(message.Attachments)
				if err != nil {
					return nil, err
				}
				for _, image := range images {
					detail := image.Attachment.Detail
					if detail == "" {
						detail = "auto"
					}
					blocks = append(blocks, responsesContentBlock{
						Type:     "input_image",
						ImageURL: "data:" + image.Attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
						Detail:   detail,
					})
				}
			}
			if len(blocks) > 0 {
				items = append(items, responsesInputMessage{Role: role, Content: blocks})
			}
			if role == "assistant" {
				for _, call := range message.ToolCalls {
					items = append(items, responsesFunctionCall{
						Type: "function_call", CallID: call.ID,
						Name: call.Function.Name, Arguments: call.Function.Arguments,
					})
				}
			}
		case "tool":
			if strings.TrimSpace(message.ToolCallID) == "" {
				items = append(items, responsesInputMessage{
					Role: "user",
					Content: []responsesContentBlock{{
						Type: "input_text",
						Text: fmt.Sprintf("Tool result (%s):\n%s", message.Name, message.Content),
					}},
				})
			} else {
				items = append(items, responsesFunctionCallOutput{
					Type: "function_call_output", CallID: message.ToolCallID, Output: message.Content,
				})
			}
		}
	}
	if len(items) == 0 {
		return nil, errors.New("Responses 输入为空")
	}
	return items, nil
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var quota interface{ IsQuotaExhausted() bool }
	if errors.As(err, &quota) && quota.IsQuotaExhausted() {
		return false
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) {
		code := status.HTTPStatusCode()
		return code == 408 || code == 409 || code == 425 || code == 429 || (code >= 500 && code <= 599 && code != 501 && code != 505)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, code := range []string{"429", "500", "502", "503", "504", "timeout", "connection reset", "eof"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}

func isToolsUnsupported(err error) bool {
	if !canRejectRequestFeature(err) {
		return false
	}
	return explicitlyUnsupported(err, "tool") || explicitlyUnsupported(err, "function")
}

func isStreamUnsupported(err error) bool {
	if !canRejectRequestFeature(err) {
		return false
	}
	return explicitlyUnsupported(err, "stream")
}

func isStreamOptionsUnsupported(err error) bool {
	if !canRejectRequestFeature(err) {
		return false
	}
	return explicitlyUnsupported(err, "stream_options") || explicitlyUnsupported(err, "include_usage")
}

func isMaxTokensUnsupported(err error) bool {
	if !canRejectRequestFeature(err) {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "max_tokens") && !strings.Contains(s, "max tokens") {
		return false
	}
	return strings.Contains(s, "unknown") || strings.Contains(s, "unrecognized") ||
		strings.Contains(s, "unsupported") || strings.Contains(s, "not support") ||
		strings.Contains(s, "extra inputs")
}

func canRejectRequestFeature(err error) bool {
	if err == nil {
		return false
	}
	var status interface{ HTTPStatusCode() int }
	if !errors.As(err, &status) {
		return true
	}
	code := status.HTTPStatusCode()
	return code == 400 || code == 404 || code == 422
}

func callOpenAICompatible(ctx context.Context, cfg config.Config, messages []Message, withTools bool, onStream func(string)) (LLMResult, error) {
	url := config.NormalizeChatURL(cfg.ApiURL)
	useStream := onStream != nil
	chatMessages, err := buildOpenAIChatMessages(messages)
	if err != nil {
		return LLMResult{}, err
	}
	reqBody := ChatRequest{
		Model:       cfg.ModelName,
		Messages:    chatMessages,
		Stream:      useStream,
		Temperature: effectiveTemperature(cfg),
		MaxTokens:   cfg.EffectiveModelCapabilities().ReservedOutputTokens,
	}
	if cfg.IsDeepSeek() {
		thinking := cfg.EffectiveThinkingMode() == "enabled"
		thinkingType := "disabled"
		if thinking {
			thinkingType = "enabled"
		}
		reqBody.Thinking = &ThinkingConfig{Type: thinkingType}
		reqBody.ReasoningEffort = cfg.EffectiveReasoningEffort()
		reqBody.UserID = strings.TrimSpace(cfg.UserID)
		reqBody.deepSeekThinking = thinking
	}
	if isKnownUnsupported(cfg, "max_tokens") {
		reqBody.MaxTokens = 0
	}
	if withTools {
		prepared, err := prepareNativeTools(ctx, cfg, messages)
		if err != nil {
			return LLMResult{}, err
		}
		reqBody.preparedTools = prepared.encoded
		// auto lets the model select a strongly typed built-in function, while
		// agent_action remains available for shell/file/task/finish actions.
		reqBody.ToolChoice = "auto"
	}

	if useStream {
		if !isKnownUnsupported(cfg, "stream_options") {
			reqBody.StreamOptions = &StreamOptions{IncludeUsage: true}
		}
		result, err := callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		if err != nil && isStreamOptionsUnsupported(err) {
			if explicitlyUnsupported(err, "stream_options") || explicitlyUnsupported(err, "include_usage") {
				rememberUnsupported(cfg, "stream_options")
			}
			reqBody.StreamOptions = nil
			result, err = callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		}
		if err != nil && reqBody.MaxTokens > 0 && isMaxTokensUnsupported(err) {
			if explicitlyUnsupported(err, "max_tokens") {
				rememberUnsupported(cfg, "max_tokens")
			}
			reqBody.MaxTokens = 0
			return callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		}
		return result, err
	}

	body, status, headers, err := doHTTPPostReply(ctx, url, cfg, reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if status != 200 && reqBody.MaxTokens > 0 {
		apiErr := apiResponseError(status, headers, body)
		if isMaxTokensUnsupported(apiErr) {
			if explicitlyUnsupported(apiErr, "max_tokens") {
				rememberUnsupported(cfg, "max_tokens")
			}
			reqBody.MaxTokens = 0
			body, status, headers, err = doHTTPPostReply(ctx, url, cfg, reqBody)
			if err != nil {
				return LLMResult{}, err
			}
		}
	}
	if status != 200 {
		return LLMResult{}, apiResponseError(status, headers, body)
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return LLMResult{}, fmt.Errorf("parse error: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return LLMResult{Usage: chatResp.Usage}, errors.New("empty response")
	}
	msg := chatResp.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		calls := make([]LLMToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			calls = append(calls, LLMToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		result := LLMResult{Content: msg.Content, ReasoningContent: msg.ReasoningContent, ToolCallID: calls[0].ID, ToolCallName: calls[0].Name, ToolCallArgs: calls[0].Arguments, ToolCalls: calls, Usage: chatResp.Usage}
		return result, finishReasonError(chatResp.Choices[0].FinishReason, result)
	}
	result := LLMResult{Content: msg.Content, ReasoningContent: msg.ReasoningContent, Usage: chatResp.Usage}
	return result, finishReasonError(chatResp.Choices[0].FinishReason, result)
}

const runtimeV3DeferredToolLimit = 16

const maxDeepSeekRequestBodyBytes = 48 << 20

func nativeToolDefinitionsForRequest(cfg config.Config, messages []Message) []ToolDefinition {
	limit, contextText, pinned := nativeToolSelection(cfg, messages)
	return canonicalNativeTools(AgentToolDefinitionsForContextWithPinned(limit, contextText, pinned))
}

func pinnedNativeToolNames(messages []Message) []string {
	selected := make(map[string]bool)
	for _, message := range messages {
		if message.Role == "tool" && isAvailableNativeTool(message.Name) {
			selected[message.Name] = true
		}
		if message.Role != "system" {
			continue
		}
		const marker = "【本任务已验证工具】"
		for _, line := range strings.Split(message.Content, "\n") {
			index := strings.Index(line, marker)
			if index < 0 {
				continue
			}
			list := line[index+len(marker):]
			if colon := strings.IndexAny(list, ":："); colon >= 0 {
				list = list[colon+1:]
			}
			for _, name := range strings.Split(list, ",") {
				name = strings.TrimSpace(name)
				if isAvailableNativeTool(name) {
					selected[name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(selected))
	for name := range selected {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func isAvailableNativeTool(name string) bool {
	if name == "" || alwaysVisibleNativeTool(name) {
		return false
	}
	if _, ok := deepsentrytools.Get(name); ok {
		return true
	}
	_, _, ok := mcp.Global().Get(name)
	return ok
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage TokenUsage `json:"usage"`
}

func callOpenAICompatibleStream(ctx context.Context, url string, cfg config.Config, reqBody ChatRequest, onStream func(string)) (LLMResult, error) {
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if cfg.IsDeepSeek() && len(jsonData) > maxDeepSeekRequestBodyBytes {
		return LLMResult{}, fmt.Errorf("DeepSeek 请求体超过 48 MiB（当前 %.1f MiB），请减少图片或历史文本", float64(len(jsonData))/(1<<20))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return LLMResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if cfg.ApiKey != "" && cfg.ApiKey != "none" {
		req.Header.Set("Authorization", "Bearer "+cfg.ApiKey)
	}
	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return LLMResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
		return LLMResult{}, apiResponseError(resp.StatusCode, resp.Header, body)
	}

	var content strings.Builder
	var reasoningContent strings.Builder
	type toolCallBuilder struct {
		id   strings.Builder
		name strings.Builder
		args strings.Builder
	}
	toolBuilders := make(map[int]*toolCallBuilder)
	var usage TokenUsage
	var partialErr error
	var finishReason string
	maxResponseBytes := maxLLMResponseBytes(cfg)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			partialErr = fmt.Errorf("invalid SSE JSON: %w", err)
			continue
		}
		if chunk.Usage.HasAny() {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		if choice.Delta.ReasoningContent != "" {
			reasoningContent.WriteString(choice.Delta.ReasoningContent)
		}
		for _, call := range choice.Delta.ToolCalls {
			builder := toolBuilders[call.Index]
			if builder == nil {
				builder = &toolCallBuilder{}
				toolBuilders[call.Index] = builder
			}
			builder.id.WriteString(call.ID)
			builder.name.WriteString(call.Function.Name)
			builder.args.WriteString(call.Function.Arguments)
		}
		delta := choice.Delta.Content
		toolBytes := 0
		for _, builder := range toolBuilders {
			toolBytes += builder.id.Len() + builder.name.Len() + builder.args.Len()
		}
		if int64(content.Len()+reasoningContent.Len()+len(delta)+toolBytes) > maxResponseBytes {
			return LLMResult{Usage: usage}, fmt.Errorf("LLM 流式响应超过上限 %d 字节", maxResponseBytes)
		}
		if delta != "" {
			content.WriteString(delta)
			onStream(delta)
		}
	}
	if err := scanner.Err(); err != nil {
		partialErr = fmt.Errorf("stream read error: %w", err)
	}
	if strings.TrimSpace(content.String()) == "" && len(toolBuilders) == 0 {
		return LLMResult{ReasoningContent: reasoningContent.String(), Usage: usage}, errors.New("empty response: no content or tool call")
	}
	indexes := make([]int, 0, len(toolBuilders))
	for index := range toolBuilders {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]LLMToolCall, 0, len(indexes))
	for _, index := range indexes {
		builder := toolBuilders[index]
		call := LLMToolCall{ID: builder.id.String(), Name: builder.name.String(), Arguments: builder.args.String()}
		if strings.TrimSpace(call.Arguments) != "" && !json.Valid([]byte(call.Arguments)) {
			partialErr = fmt.Errorf("invalid JSON tool arguments for %s", call.Name)
		}
		calls = append(calls, call)
	}
	result := LLMResult{Content: content.String(), ReasoningContent: reasoningContent.String(), ToolCalls: calls, Usage: usage}
	if len(calls) > 0 {
		result.ToolCallID = calls[0].ID
		result.ToolCallName = calls[0].Name
		result.ToolCallArgs = calls[0].Arguments
	}
	if partialErr == nil {
		partialErr = finishReasonError(finishReason, result)
	}
	if partialErr != nil {
		return result, &PartialStreamError{Result: result, Err: partialErr}
	}
	return result, nil
}

func finishReasonError(reason string, result LLMResult) error {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length":
		return &PartialStreamError{Result: result, Err: errors.New("模型输出达到长度上限（finish_reason=length）")}
	case "content_filter":
		return &PartialStreamError{Result: result, Err: errors.New("模型输出被内容过滤（finish_reason=content_filter）")}
	case "insufficient_system_resource":
		return &PartialStreamError{Result: result, Err: errors.New("DeepSeek 后端推理资源不足（finish_reason=insufficient_system_resource）")}
	case "aborted":
		return &PartialStreamError{Result: result, Err: errors.New("模型生成被中断（finish_reason=aborted）")}
	default:
		return nil
	}
}

type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Metadata     *anthropicMetadata     `json:"metadata,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	Thinking  string                `json:"thinking,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   interface{}           `json:"content,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicResponse struct {
	Content []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking,omitempty"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason,omitempty"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func callAnthropic(ctx context.Context, cfg config.Config, messages []Message, useNativeTools bool) (LLMResult, error) {
	url := config.NormalizeAPIURL(cfg.ApiURL, config.ProtocolAnthropicMessages)
	var system strings.Builder
	var msgs []anthropicMessage
	for _, m := range messages {
		switch m.Role {
		case "system":
			system.WriteString(m.Content)
			system.WriteString("\n")
		case "user", "assistant":
			blocks := make([]anthropicContentBlock, 0, 1+len(m.Attachments)+len(m.ToolCalls))
			if m.Role == "assistant" && strings.TrimSpace(m.ReasoningContent) != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "thinking", Thinking: m.ReasoningContent})
			}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			if len(m.Attachments) > 0 {
				if m.Role != "user" {
					return LLMResult{}, errors.New("Anthropic 图片附件只能出现在 user 消息中")
				}
				images, err := materializeImages(m.Attachments)
				if err != nil {
					return LLMResult{}, err
				}
				if len(blocks) == 0 {
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: "请分析所附图片，并结合当前任务继续。"})
				}
				for _, image := range images {
					blocks = append(blocks, anthropicContentBlock{Type: "image", Source: &anthropicImageSource{
						Type: "base64", MediaType: image.Attachment.MediaType,
						Data: base64.StdEncoding.EncodeToString(image.Data),
					}})
				}
			}
			for _, call := range m.ToolCalls {
				input := json.RawMessage(call.Function.Arguments)
				if !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input,
				})
			}
			if len(blocks) > 0 {
				msgs = append(msgs, anthropicMessage{Role: m.Role, Content: blocks})
			}
		case "tool":
			msgs = append(msgs, anthropicMessage{Role: "user", Content: []anthropicContentBlock{{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content,
			}}})
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, anthropicMessage{Role: "user", Content: "continue"})
	}

	reqBody := anthropicRequest{
		Model:     cfg.ModelName,
		MaxTokens: anthropicMaxTokens(cfg),
		System:    strings.TrimSpace(system.String()),
		Messages:  msgs,
	}
	if cfg.IsDeepSeek() {
		reqBody.MaxTokens = cfg.EffectiveModelCapabilities().ReservedOutputTokens
		if userID := strings.TrimSpace(cfg.UserID); userID != "" {
			reqBody.Metadata = &anthropicMetadata{UserID: userID}
		}
		if cfg.EffectiveThinkingMode() == "enabled" {
			reqBody.Thinking = &anthropicThinking{Type: "enabled"}
			if effort := cfg.EffectiveReasoningEffort(); effort != "" {
				reqBody.OutputConfig = &anthropicOutputConfig{Effort: effort}
			}
		} else {
			reqBody.Thinking = &anthropicThinking{Type: "disabled"}
			reqBody.OutputConfig = &anthropicOutputConfig{Effort: "none"}
			temp := effectiveTemperature(cfg)
			reqBody.Temperature = &temp
		}
	} else if effort := anthropicEffort(cfg.ModelName); effort != "" {
		reqBody.OutputConfig = &anthropicOutputConfig{Effort: effort}
	}
	if useNativeTools {
		definitions := nativeToolDefinitionsForRequest(cfg, messages)
		reqBody.Tools = make([]anthropicTool, 0, len(definitions))
		for _, definition := range definitions {
			reqBody.Tools = append(reqBody.Tools, anthropicTool{
				Name: definition.Function.Name, Description: definition.Function.Description,
				InputSchema: definition.Function.Parameters,
			})
		}
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return LLMResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return LLMResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.ApiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return LLMResult{}, err
	}
	defer resp.Body.Close()
	body, err := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
	if err != nil {
		return LLMResult{}, err
	}
	if resp.StatusCode != 200 {
		return LLMResult{}, apiResponseError(resp.StatusCode, resp.Header, body)
	}

	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return LLMResult{}, err
	}
	usage := TokenUsage{
		PromptTokens:     ar.Usage.InputTokens + ar.Usage.CacheReadInputTokens + ar.Usage.CacheCreationInputTokens,
		CompletionTokens: ar.Usage.OutputTokens,
		TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens + ar.Usage.CacheReadInputTokens + ar.Usage.CacheCreationInputTokens,
		CacheReadTokens:  ar.Usage.CacheReadInputTokens,
		CacheWriteTokens: ar.Usage.CacheCreationInputTokens,
	}
	var rawUsage struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(body, &rawUsage)
	for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens"} {
		if raw, ok := rawUsage.Usage[key]; ok && string(raw) != "null" {
			usage.CacheUsageReported = true
		}
	}
	if ar.Error != nil {
		return LLMResult{Usage: usage}, errors.New(ar.Error.Message)
	}
	var text, thinking strings.Builder
	var calls []LLMToolCall
	for _, c := range ar.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "thinking":
			thinking.WriteString(c.Thinking)
		case "tool_use":
			args := strings.TrimSpace(string(c.Input))
			if args == "" || args == "null" {
				args = "{}"
			}
			calls = append(calls, LLMToolCall{ID: c.ID, Name: c.Name, Arguments: args})
		}
	}
	if strings.EqualFold(ar.StopReason, "refusal") {
		msg := strings.TrimSpace(text.String())
		if msg == "" {
			msg = "模型拒绝回答"
		}
		return LLMResult{Usage: usage}, fmt.Errorf("Anthropic 拒绝回答: %s", msg)
	}
	result := LLMResult{Content: text.String(), ReasoningContent: thinking.String(), ToolCalls: calls, Usage: usage}
	if len(calls) > 0 {
		result.ToolCallID = calls[0].ID
		result.ToolCallName = calls[0].Name
		result.ToolCallArgs = calls[0].Arguments
	}
	if strings.EqualFold(ar.StopReason, "max_tokens") {
		return result, &PartialStreamError{Result: result, Err: errors.New("Anthropic 响应达到 max_tokens 上限")}
	}
	if text.Len() == 0 && len(calls) == 0 {
		return result, errors.New("Anthropic 返回空内容")
	}
	return result, nil
}

// anthropicMaxTokens reserves room for Claude 5 adaptive thinking, which shares
// the max_tokens budget. Operators can still pin ReservedOutputTokens.
func anthropicMaxTokens(cfg config.Config) int {
	if cfg.ReservedOutputTokens > 0 {
		return cfg.ReservedOutputTokens
	}
	if anthropicUsesAdaptiveThinking(cfg.ModelName) {
		return 65536
	}
	return cfg.EffectiveModelCapabilities().ReservedOutputTokens
}

func anthropicEffort(model string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-opus-5-5") {
		return "medium"
	}
	if anthropicUsesAdaptiveThinking(model) {
		return "high"
	}
	return ""
}

func anthropicUsesAdaptiveThinking(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || strings.Contains(m, "haiku") {
		return false
	}
	return strings.Contains(m, "claude-opus-5") ||
		strings.Contains(m, "claude-sonnet-5") ||
		strings.Contains(m, "claude-fable-5") ||
		strings.Contains(m, "claude-opus-4-8") ||
		strings.Contains(m, "claude-opus-4-7")
}

func recentToolSelectionContext(messages []Message, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 12000
	}
	// Retrieval follows user requirements, not the vocabulary of a large log
	// or webpage. Successful/discovered functions are pinned separately. This
	// keeps irrelevant output from churning schemas or hiding the actual goal.
	var userParts []string
	userBytes := 0
	for i := len(messages) - 1; i >= 0 && userBytes < maxBytes; i-- {
		if messages[i].Role != "user" {
			continue
		}
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		content = truncateStr(content, maxBytes-userBytes)
		userParts = append(userParts, content)
		userBytes += len(content)
	}
	if len(userParts) > 0 {
		for i, j := 0, len(userParts)-1; i < j; i, j = i+1, j-1 {
			userParts[i], userParts[j] = userParts[j], userParts[i]
		}
		return strings.Join(userParts, "\n")
	}
	// Compatibility callers may supply only a summary or tool result.
	var parts []string
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "system" {
			continue
		}
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		if len(content)+used > maxBytes {
			content = truncateStr(content, maxBytes-used)
		}
		parts = append(parts, content)
		used += len(content)
		if used >= maxBytes {
			break
		}
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n")
}

func doHTTPPostReply(ctx context.Context, url string, cfg config.Config, payload interface{}) ([]byte, int, http.Header, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, nil, err
	}
	if cfg.IsDeepSeek() && len(jsonData) > maxDeepSeekRequestBodyBytes {
		return nil, 0, nil, fmt.Errorf("DeepSeek 请求体超过 48 MiB（当前 %.1f MiB），请减少图片或历史文本", float64(len(jsonData))/(1<<20))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.ApiKey != "" && cfg.ApiKey != "none" {
		req.Header.Set("Authorization", "Bearer "+cfg.ApiKey)
	}
	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	body, err := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return body, resp.StatusCode, resp.Header, nil
}

func maxLLMResponseBytes(cfg config.Config) int64 {
	limit := int64(cfg.EffectiveModelCapabilities().ReservedOutputTokens) * 8
	if limit < 1<<20 {
		limit = 1 << 20
	}
	if limit > 32<<20 {
		limit = 32 << 20
	}
	return limit
}

func readLimitedResponseBody(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("LLM HTTP 响应超过上限 %d 字节", limit)
	}
	return body, nil
}

func effectiveTemperature(cfg config.Config) float64 {
	// Zero is a valid and intentional value for operational/benchmark
	// determinism. The old fallback silently turned temperature: 0.0 into 0.1,
	// causing identical Runtime A/B prompts to select different tools.
	return cfg.Temperature
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end] + "..."
}

// TruncateHistoryFallback 摘要失败时的机械截断
func TruncateHistoryFallback(history *[]Message, keepRecent int) {
	TruncateHistoryFallbackWithHints(history, keepRecent, "")
}

// TruncateHistoryFallbackWithHints 在摘要服务不可用时仍保留原始目标、已有摘要与核心线索。
func TruncateHistoryFallbackWithHints(history *[]Message, keepRecent int, pinnedContext string) {
	truncateHistoryFallbackToBudget(history, keepRecent, pinnedContext, 0)
}

func truncateHistoryFallbackToBudget(history *[]Message, keepRecent int, pinnedContext string, tokenBudget int) {
	if history == nil || keepRecent <= 0 {
		return
	}
	h := *history
	if len(h) <= keepRecent+2 && (tokenBudget <= 0 || EstimateMessagesTokens(h) <= tokenBudget) {
		return
	}
	if keepRecent > len(h) {
		keepRecent = len(h)
	}
	trimmed := append([]Message(nil), h[len(h)-keepRecent:]...)
	goal := firstHistoryUserGoal(h)
	latestDirective := latestHistoryUserDirective(h)
	priorSummary := latestHistorySummary(h)
	var context strings.Builder
	context.WriteString(fmt.Sprintf("【前情提要】(摘要服务不可用，机械保留最近 %d 条)\n", keepRecent))
	if goal != "" {
		context.WriteString("【原始用户目标】\n")
		context.WriteString(trimContextText(goal, 3000))
		context.WriteString("\n")
	}
	if latestDirective != "" && latestDirective != goal {
		context.WriteString("【最新用户补充/修正】\n")
		context.WriteString(trimContextText(latestDirective, 3000))
		context.WriteString("\n")
	}
	if strings.TrimSpace(pinnedContext) != "" {
		context.WriteString(trimContextText(pinnedContext, 8000))
		context.WriteString("\n")
	}
	if priorSummary != "" {
		context.WriteString("【上一次有效摘要】\n")
		context.WriteString(trimContextText(priorSummary, 8000))
		context.WriteString("\n")
	}
	context.WriteString("较早原始对话已省略；以上固定信息与最近步骤继续有效。")
	contextMessage := Message{
		Role:    "system",
		Content: context.String(),
	}
	if tokenBudget > 0 {
		remaining := tokenBudget - EstimateMessagesTokens([]Message{contextMessage})
		if remaining < 256 {
			remaining = 256
		}
		selected := make([]Message, 0, len(trimmed))
		for i := len(trimmed) - 1; i >= 0 && remaining > 0; i-- {
			message := trimmed[i]
			cost := 4 + EstimateTextTokens(message.Role) + EstimateTextTokens(message.Content)
			if cost > remaining {
				message.Content = trimContextTextToTokens(message.Content, maxAnalyzerInt(64, remaining-8))
				cost = 4 + EstimateTextTokens(message.Role) + EstimateTextTokens(message.Content)
			}
			selected = append(selected, message)
			remaining -= cost
		}
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
		trimmed = selected
	}
	*history = append([]Message{contextMessage}, trimmed...)
}

func isGPT6ReasoningModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, id := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if m == id || strings.HasPrefix(m, id+"-20") {
			return true
		}
	}
	return false
}

// GPT-6 models default to reasoning; Chat Completions rejects sampling
// parameters at non-none effort. Keep other models' temperature behavior.
func (r ChatRequest) MarshalJSON() ([]byte, error) {
	type plain ChatRequest
	if r.deepSeekThinking {
		// DeepSeek ignores temperature in thinking mode. Omitting it keeps the
		// request honest and avoids teaching the model that the value matters.
		if r.preparedTools != nil {
			return json.Marshal(struct {
				plain
				Temperature *float64        `json:"temperature,omitempty"`
				Tools       json.RawMessage `json:"tools,omitempty"`
			}{plain: plain(r), Tools: r.preparedTools})
		}
		return json.Marshal(struct {
			plain
			Temperature *float64 `json:"temperature,omitempty"`
		}{plain: plain(r)})
	}
	if r.preparedTools != nil && !isGPT6ReasoningModel(r.Model) {
		return json.Marshal(struct {
			plain
			Tools json.RawMessage `json:"tools,omitempty"`
		}{plain: plain(r), Tools: r.preparedTools})
	}
	if !isGPT6ReasoningModel(r.Model) {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Temperature         *float64 `json:"temperature,omitempty"`
		MaxTokens           *int     `json:"max_tokens,omitempty"`
		MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
	}{plain: plain(r), MaxCompletionTokens: r.MaxTokens})
}
