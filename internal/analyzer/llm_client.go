package analyzer

import (
	"ai-edr/internal/config"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// LLMResult LLM 调用结果
type LLMResult struct {
	Content      string
	ToolCallName string
	ToolCallArgs string
	Usage        TokenUsage
}

type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

func (u TokenUsage) HasAny() bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// CallLLMWithRetry 带重试与降级的统一 LLM 调用；onStream 非 nil 时在 OpenAI 兼容 JSON 模式下启用 SSE 流式
func CallLLMWithRetry(messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	return CallLLMWithRetryContext(context.Background(), messages, useNativeTools, onStream)
}

func CallLLMWithRetryContext(ctx context.Context, messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := config.GlobalConfig
	retries := cfg.EffectiveLLMRetries()
	var lastErr error

	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(llmRetryDelay(attempt))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return LLMResult{}, ctx.Err()
			}
		}

		native := useNativeTools && cfg.IsOpenAICompatible()
		var result LLMResult
		var err error

		// TUI 等场景：有 onStream 时优先 JSON + SSE 流式
		if onStream != nil && cfg.IsOpenAICompatible() {
			result, err = callOpenAICompatible(ctx, cfg, messages, false, onStream)
			if err != nil && isStreamUnsupported(err) {
				result, err = callOpenAICompatible(ctx, cfg, messages, false, nil)
			}
		} else if cfg.IsAnthropic() {
			result, err = callAnthropic(ctx, cfg, messages)
		} else if cfg.IsOpenAIResponses() {
			result, err = callOpenAIResponses(ctx, cfg, messages)
		} else if native {
			result, err = callOpenAICompatible(ctx, cfg, messages, true, nil)
			if err != nil && isToolsUnsupported(err) {
				result, err = callOpenAICompatible(ctx, cfg, messages, false, onStream)
			}
		} else {
			result, err = callOpenAICompatible(ctx, cfg, messages, false, nil)
		}

		if err == nil {
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
	return LLMResult{}, fmt.Errorf("LLM 调用失败(已重试 %d 次): %w", retries, lastErr)
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
	Model           string  `json:"model"`
	Input           string  `json:"input"`
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"max_output_tokens,omitempty"`
}

type responsesResponse struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func callOpenAIResponses(ctx context.Context, cfg config.Config, messages []Message) (LLMResult, error) {
	url := strings.TrimRight(cfg.ApiURL, "/")
	if !strings.HasSuffix(url, "/responses") {
		if strings.HasSuffix(url, "/v1") {
			url += "/responses"
		} else {
			url += "/v1/responses"
		}
	}
	reqBody := responsesRequest{
		Model:           cfg.ModelName,
		Input:           messagesToTranscript(messages),
		Temperature:     effectiveTemperature(cfg),
		MaxOutputTokens: cfg.EffectiveModelCapabilities().ReservedOutputTokens,
	}
	body, status, err := doHTTPPost(ctx, url, cfg, reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if status != 200 {
		return LLMResult{}, fmt.Errorf("调用 Responses API 失败 %d: %s", status, truncateStr(string(body), 500))
	}
	var rr responsesResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return LLMResult{}, err
	}
	if rr.Error != nil {
		return LLMResult{}, errors.New(rr.Error.Message)
	}
	usage := TokenUsage{
		PromptTokens:     rr.Usage.InputTokens,
		CompletionTokens: rr.Usage.OutputTokens,
		TotalTokens:      rr.Usage.TotalTokens,
	}
	if strings.TrimSpace(rr.OutputText) != "" {
		return LLMResult{Content: rr.OutputText, Usage: usage}, nil
	}
	var b strings.Builder
	for _, out := range rr.Output {
		for _, c := range out.Content {
			if c.Text != "" {
				b.WriteString(c.Text)
			}
		}
	}
	if b.Len() == 0 {
		return LLMResult{}, errors.New("empty responses output")
	}
	return LLMResult{Content: b.String(), Usage: usage}, nil
}

func messagesToTranscript(messages []Message) string {
	var b strings.Builder
	for _, m := range messages {
		role := strings.ToUpper(strings.TrimSpace(m.Role))
		if role == "" {
			role = "USER"
		}
		b.WriteString(role)
		b.WriteString(":\n")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}

func isRetryable(err error) bool {
	if err == nil {
		return false
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
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "tool") || strings.Contains(s, "400") || strings.Contains(s, "404")
}

func isStreamUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream") || strings.Contains(s, "400") ||
		strings.Contains(s, "404") || strings.Contains(s, "not support")
}

func isStreamOptionsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream_options") ||
		strings.Contains(s, "include_usage") ||
		strings.Contains(s, "unknown parameter") ||
		strings.Contains(s, "unrecognized") ||
		strings.Contains(s, "unsupported parameter")
}

func isMaxTokensUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "max_tokens") && !strings.Contains(s, "max tokens") {
		return false
	}
	return strings.Contains(s, "unknown") || strings.Contains(s, "unrecognized") ||
		strings.Contains(s, "unsupported") || strings.Contains(s, "not support") ||
		strings.Contains(s, "extra inputs") || strings.Contains(s, "400")
}

func callOpenAICompatible(ctx context.Context, cfg config.Config, messages []Message, withTools bool, onStream func(string)) (LLMResult, error) {
	url := config.NormalizeChatURL(cfg.ApiURL)
	useStream := onStream != nil && !withTools
	reqBody := ChatRequest{
		Model:       cfg.ModelName,
		Messages:    messages,
		Stream:      useStream,
		Temperature: effectiveTemperature(cfg),
		MaxTokens:   cfg.EffectiveModelCapabilities().ReservedOutputTokens,
	}
	if withTools {
		capabilities := cfg.EffectiveModelCapabilities()
		reqBody.Tools = AgentToolDefinitionsForContext(capabilities.NativeToolLimit, recentToolSelectionContext(messages, 12000))
		// auto lets the model select a strongly typed built-in function, while
		// agent_action remains available for shell/file/task/finish actions.
		reqBody.ToolChoice = "auto"
	}

	if useStream {
		reqBody.StreamOptions = &StreamOptions{IncludeUsage: true}
		result, err := callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		if err != nil && isStreamOptionsUnsupported(err) {
			reqBody.StreamOptions = nil
			result, err = callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		}
		if err != nil && reqBody.MaxTokens > 0 && isMaxTokensUnsupported(err) {
			reqBody.MaxTokens = 0
			return callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		}
		return result, err
	}

	body, status, err := doHTTPPost(ctx, url, cfg, reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if status != 200 && reqBody.MaxTokens > 0 {
		apiErr := fmt.Errorf("API Error %d: %s", status, truncateStr(string(body), 500))
		if isMaxTokensUnsupported(apiErr) {
			reqBody.MaxTokens = 0
			body, status, err = doHTTPPost(ctx, url, cfg, reqBody)
			if err != nil {
				return LLMResult{}, err
			}
		}
	}
	if status != 200 {
		return LLMResult{}, fmt.Errorf("API Error %d: %s", status, truncateStr(string(body), 500))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return LLMResult{}, fmt.Errorf("parse error: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return LLMResult{}, errors.New("empty response")
	}
	msg := chatResp.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		return LLMResult{Content: msg.Content, ToolCallName: msg.ToolCalls[0].Function.Name, ToolCallArgs: msg.ToolCalls[0].Function.Arguments, Usage: chatResp.Usage}, nil
	}
	return LLMResult{Content: msg.Content, Usage: chatResp.Usage}, nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int `json:"index"`
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
		return LLMResult{}, fmt.Errorf("API Error %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var content strings.Builder
	var usage TokenUsage
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
			continue
		}
		if chunk.Usage.HasAny() {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		if int64(content.Len()+len(delta)) > maxResponseBytes {
			return LLMResult{}, fmt.Errorf("LLM 流式响应超过上限 %d 字节", maxResponseBytes)
		}
		content.WriteString(delta)
		onStream(delta)
	}
	if err := scanner.Err(); err != nil {
		return LLMResult{}, fmt.Errorf("stream read error: %w", err)
	}
	if content.Len() == 0 {
		return LLMResult{}, errors.New("empty stream response")
	}
	return LLMResult{Content: content.String(), Usage: usage}, nil
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func callAnthropic(ctx context.Context, cfg config.Config, messages []Message) (LLMResult, error) {
	url := config.NormalizeChatURL(cfg.ApiURL)
	var system strings.Builder
	var msgs []anthropicMessage
	for _, m := range messages {
		switch m.Role {
		case "system":
			system.WriteString(m.Content)
			system.WriteString("\n")
		case "user", "assistant":
			msgs = append(msgs, anthropicMessage(m))
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, anthropicMessage{Role: "user", Content: "continue"})
	}

	reqBody := anthropicRequest{
		Model:     cfg.ModelName,
		MaxTokens: cfg.EffectiveModelCapabilities().ReservedOutputTokens,
		System:    strings.TrimSpace(system.String()),
		Messages:  msgs,
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
		return LLMResult{}, fmt.Errorf("调用 Anthropic API 失败 %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return LLMResult{}, err
	}
	if ar.Error != nil {
		return LLMResult{}, errors.New(ar.Error.Message)
	}
	var text strings.Builder
	for _, c := range ar.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	usage := TokenUsage{
		PromptTokens:     ar.Usage.InputTokens,
		CompletionTokens: ar.Usage.OutputTokens,
		TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
	}
	return LLMResult{Content: text.String(), Usage: usage}, nil
}

func recentToolSelectionContext(messages []Message, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 12000
	}
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

func doHTTPPost(ctx context.Context, url string, cfg config.Config, payload interface{}) ([]byte, int, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.ApiKey != "" && cfg.ApiKey != "none" {
		req.Header.Set("Authorization", "Bearer "+cfg.ApiKey)
	}
	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
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
	if cfg.Temperature > 0 {
		return cfg.Temperature
	}
	return 0.1
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
