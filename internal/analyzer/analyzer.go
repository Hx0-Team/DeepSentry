package analyzer

import (
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/security"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type AgentResponse struct {
	Thought     string `json:"thought"`
	Command     string `json:"command"`
	RiskLevel   string `json:"risk_level"`
	Reason      string `json:"reason"`
	IsFinished  bool   `json:"is_finished"`
	FinalReport string `json:"final_report"`
}

// 兼容性结构体：用于解析 AI 可能返回的多种格式
type CompatibilityResponse struct {
	Thought     string      `json:"thought"`
	Command     string      `json:"command"`
	RiskLevel   string      `json:"risk_level"`
	IsFinished  bool        `json:"is_finished"`
	FinalReport interface{} `json:"final_report"`
	CmdArray    []string    `json:"cmd"`
	Explanation string      `json:"explanation"`
}

// RunAgentStep 执行 Agent 的单步思考
func RunAgentStep(sysCtx collector.SystemContext, history *[]Message) (AgentResponse, error) {
	apiKey := config.GlobalConfig.ApiKey

	// 1. 获取基础 System Prompt (来自 collector)
	basePrompt := sysCtx.GenerateSystemPrompt()

	// 增强 Windows 路径操作指南 & JSON 约束
	selfProtectionPrompt := `
【⛔ 核心自我保护守则】
1. 绝对禁止删除/移动 config.yaml, deepsentry.exe, reports/ 目录。

【🪟 Windows 文件操作专家模式】
1. **中文路径与乱码**：如果 'dir' 显示乱码，请使用通配符 (*.pdf) 操作，不要直接复制乱码文件名。
2. **路径变量**：使用 PowerShell 时可直接用 $HOME。

【⚠️ JSON 严格语法】
1. 在 JSON 字符串值中，**双引号 (") 必须转义为 (\")**。
2. **反斜杠 (\) 必须转义为 (\\)**。
   - 错误示例: {"command": "grep "eval" file"}
   - 正确示例: {"command": "grep \"eval\" file"}
`
	systemPrompt := basePrompt + selfProtectionPrompt

	// Context 滑动窗口：防止 Token 超限
	if len(*history) > 15 {
		compressHistory(apiKey, history)
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
	}
	messages = append(messages, *history...)

	// 调用 LLM
	rawResp, err := callLLM(apiKey, messages)
	if err != nil {
		return AgentResponse{}, err
	}

	// 2. 清洗 JSON
	cleanResp := cleanJSON(rawResp)
	var compat CompatibilityResponse

	// 3. 尝试标准解析
	err = json.Unmarshal([]byte(cleanResp), &compat)

	// 🟢 [核心修复] JSON 解析失败时的智能兜底 (字符级扫描)
	if err != nil {
		// 尝试手动补全括号（针对截断情况）
		fixTry := cleanResp
		if !strings.HasSuffix(strings.TrimSpace(fixTry), "}") {
			fixTry += "}"
		}

		// 再次尝试标准解析
		if err2 := json.Unmarshal([]byte(fixTry), &compat); err2 != nil {
			// 🔴 启用【字符级扫描提取器】
			// 这是最后的防线：不依赖 JSON 库，直接从字符串中从左到右扫描提取 command 的值
			// 能完美处理转义引号 (\") 和转义反斜杠 (\\) 造成的解析错误
			extractedCmd, found := extractCommandString(cleanResp)

			if found && extractedCmd != "" {
				compat.Command = extractedCmd
				compat.Thought = "JSON 格式异常(转义错误)，已启用【字符级扫描】精确提取命令。"
				compat.RiskLevel = "high" // 强制设为高危，让用户确认

				// 视为成功，清除错误
				err = nil
			} else {
				// 彻底失败
				return AgentResponse{
					Thought:     "AI 响应格式完全不可读",
					FinalReport: fmt.Sprintf("❌ 解析失败: %v\n原始响应:\n%s", err, rawResp),
					IsFinished:  true,
					RiskLevel:   "low",
				}, nil
			}
		} else {
			// 补全括号后解析成功
			err = nil
		}
	}

	resp := AgentResponse{
		RiskLevel:  compat.RiskLevel,
		IsFinished: compat.IsFinished,
	}

	// 适配 Command (兼容 string 或 []string)
	if compat.Command != "" {
		resp.Command = compat.Command
	} else if len(compat.CmdArray) > 0 {
		resp.Command = compat.CmdArray[len(compat.CmdArray)-1]
	}

	// 适配 Thought
	if compat.Thought != "" {
		resp.Thought = compat.Thought
	} else if compat.Explanation != "" {
		resp.Thought = compat.Explanation
	} else {
		resp.Thought = inferThoughtFromCommand(resp.Command)
	}

	// 适配 Report
	switch v := compat.FinalReport.(type) {
	case string:
		resp.FinalReport = v
	case map[string]interface{}, []interface{}:
		prettyBytes, _ := json.MarshalIndent(v, "", "  ")
		resp.FinalReport = string(prettyBytes)
	default:
		if v != nil {
			resp.FinalReport = fmt.Sprintf("%v", v)
		}
	}

	// -------------------------------------------------------------------------
	// 🟢 [核心修复点] 强制使用 security 包进行风险检查
	// -------------------------------------------------------------------------
	if resp.Command != "" {
		// 调用 security 包 (也就是你写了 CheckRisk 的那个文件)
		realRisk, realReason := security.CheckRisk(resp.Command)

		// 霸道覆盖：无论 AI 说是 high 还是 low，都以本地代码逻辑为准
		resp.RiskLevel = realRisk
		resp.Reason = realReason
	}
	// -------------------------------------------------------------------------

	// --- 报告内容兜底逻辑 ---
	// 只有在 IsFinished 为 true 时才生成最终报告
	if resp.IsFinished {
		if strings.TrimSpace(resp.FinalReport) == "" || resp.FinalReport == "任务完成" {
			if resp.Thought != "" {
				resp.FinalReport = fmt.Sprintf("📋 任务总结: %s", resp.Thought)
			} else {
				resp.FinalReport = "任务已结束 (详细结果请向上翻阅执行日志)"
			}
		}
	}

	return resp, nil
}

// compressHistory 压缩历史记录
func compressHistory(apiKey string, history *[]Message) error {
	cutIndex := 10
	if len(*history) < cutIndex {
		return nil
	}
	toSummarize := (*history)[:cutIndex]
	remaining := (*history)[cutIndex:]
	summaryPrompt := []Message{
		{Role: "system", Content: "你是一个专业的会议记录员。请阅读以下对话，将其压缩成一段简练的【前情提要】。保留关键的系统信息、已执行的命令和发现的线索。"},
	}
	summaryPrompt = append(summaryPrompt, toSummarize...)
	summaryPrompt = append(summaryPrompt, Message{Role: "user", Content: "请生成摘要。"})

	summaryText, err := callLLM(apiKey, summaryPrompt)
	if err != nil {
		return err
	}
	newHistory := []Message{
		{Role: "system", Content: fmt.Sprintf("【前情提要】:\n%s", summaryText)},
	}
	newHistory = append(newHistory, remaining...)
	*history = newHistory
	return nil
}

func inferThoughtFromCommand(cmd string) string {
	if strings.HasPrefix(cmd, "upload") {
		return "正在上传文件到目标主机..."
	}
	if strings.HasPrefix(cmd, "download") {
		return "正在下载文件到本地分析..."
	}
	if cmd == "" {
		return "分析中..."
	}
	return fmt.Sprintf("执行: %s", cmd)
}

// cleanJSON 负责清洗和修复 JSON 字符串
func cleanJSON(s string) string {
	// 1. 移除 Markdown 代码块标记
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// 2. 🟢 [核心修复] 预处理 JSON 中常见的非法 Shell 转义符
	// Shell 命令中的管道符 `|` 在 JSON 字符串中如果不转义，有时会导致解析问题（取决于上下文）
	// 但更重要的是防止 AI 写出 "grep 'a|b'" 这种导致 JSON 结构破坏的写法
	// 这里我们做一个防御性替换：将 `\|` 替换为 `\\|` (转义反斜杠)
	if strings.Contains(s, `\|`) {
		s = strings.ReplaceAll(s, `\|`, `\\|`)
	}

	return s
}

// 🟢 [核心新增] extractCommandString 手动扫描字符串，提取 "command": "..." 中的值
// 能够完美处理转义引号 (\") 和转义反斜杠 (\\)，不依赖正则
func extractCommandString(jsonStr string) (string, bool) {
	// 1. 定位 key
	key := `"command"`
	idx := strings.Index(jsonStr, key)
	if idx == -1 {
		return "", false
	}

	// 2. 从 key 后面开始找第一个冒号
	cursor := idx + len(key)
	// 跳过冒号前的空白
	for cursor < len(jsonStr) && (jsonStr[cursor] == ' ' || jsonStr[cursor] == ':' || jsonStr[cursor] == '\n' || jsonStr[cursor] == '\r') {
		cursor++
	}

	// 3. 找值的起始引号
	startQuote := -1
	for i := cursor; i < len(jsonStr); i++ {
		if jsonStr[i] == '"' {
			startQuote = i
			break
		}
	}
	if startQuote == -1 {
		return "", false
	}

	// 4. 逐字符扫描，寻找结束引号（注意跳过转义字符）
	var resultBuilder strings.Builder
	inEscape := false // 是否处于转义状态

	for i := startQuote + 1; i < len(jsonStr); i++ {
		char := jsonStr[i]

		if inEscape {
			// 上一个字符是反斜杠，当前字符是转义后的字符
			// JSON 规范中，\" 代表 "，\\ 代表 \

			// 我们需要还原出“原始的Shell命令字符串”
			// 如果 JSON 里写的是 \" (即Shell里的 ")，我们需要写入 "
			// 如果 JSON 里写的是 \\ (即Shell里的 \)，我们需要写入 \

			switch char {
			case '"', '\\', '/':
				resultBuilder.WriteByte(char)
			case 'n':
				resultBuilder.WriteByte('\n')
			case 'r':
				resultBuilder.WriteByte('\r')
			case 't':
				resultBuilder.WriteByte('\t')
			default:
				// 其他情况，保留反斜杠和字符 (比如正则里的 \d，AI可能写成了 \\d)
				// 既然是手动提取，我们尽量保留原意
				resultBuilder.WriteByte('\\')
				resultBuilder.WriteByte(char)
			}
			inEscape = false
		} else {
			if char == '\\' {
				inEscape = true
			} else if char == '"' {
				// 找到了未转义的结束引号，提取结束！
				return resultBuilder.String(), true
			} else {
				resultBuilder.WriteByte(char)
			}
		}
	}

	return "", false
}

// callLLM 统一调用大模型接口
func callLLM(apiKey string, messages []Message) (string, error) {
	reqBody := ChatRequest{
		Model:       config.GlobalConfig.ModelName,
		Messages:    messages,
		Stream:      false,
		Temperature: 0.1, // Temperature 设低一点，让 AI 输出更稳定
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", config.GlobalConfig.ApiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API Error %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("Parse Error: %v", err)
	}
	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}
	return "", errors.New("empty response")
}
