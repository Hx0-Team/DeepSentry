package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HawkEyeToolProfile is DeepSentry's local contract for the public HawkEye
// extension 1.0.7 / MCP Server 1.0.14 tool surface. Server annotations remain useful evidence, but this
// allow-list is what permits action-aware routing and approvals; an arbitrary
// third-party MCP server cannot become trusted merely by setting readOnlyHint.
type HawkEyeToolProfile struct {
	Category string
	Priority int
}

const (
	MCPRiskLow    = "low"
	MCPRiskMedium = "medium"
	MCPRiskHigh   = "high"
)

var hawkEyeToolProfiles = map[string]HawkEyeToolProfile{
	"browser_navigate": {"browser", 920}, "browser_search": {"research", 870},
	"browser_fetch": {"research", 860}, "browser_research": {"research", 850},
	"browser_go_back": {"browser", 600}, "browser_go_forward": {"browser", 600},
	"browser_snapshot": {"browser", 990}, "browser_find": {"browser", 900},
	"browser_security": {"evidence", 720}, "browser_click": {"interaction", 890},
	"browser_hover": {"interaction", 610}, "browser_type": {"interaction", 880},
	"browser_select_option": {"interaction", 760}, "browser_press_key": {"interaction", 870},
	"browser_wait": {"browser", 580}, "browser_get_console_logs": {"evidence", 650},
	"browser_screenshot": {"evidence", 820}, "browser_captcha_assist": {"interaction", 640},
	"browser_resize": {"interaction", 520}, "browser_tabs": {"browser", 1000},
	"browser_reload": {"browser", 610}, "browser_wait_for": {"browser", 780},
	"browser_fill_form": {"interaction", 750}, "browser_exam_questions": {"browser", 500},
	"browser_answer_questions": {"interaction", 500}, "browser_handle_dialog": {"interaction", 620},
	"browser_file_upload": {"interaction", 560}, "browser_download_file": {"evidence", 570},
	"browser_drag": {"interaction", 540}, "browser_scroll": {"interaction", 630},
	"browser_read_text": {"browser", 910},
	"browser_bookmarks": {"browser", 730},

	"hawkeye_capture_start": {"capture", 970}, "hawkeye_capture_stop": {"capture", 740},
	"hawkeye_capture_state": {"capture", 980}, "hawkeye_capture_history": {"capture", 960},
	"hawkeye_capture_inspect": {"capture", 940}, "hawkeye_request_get": {"capture", 950},
	"hawkeye_request_replay": {"active", 830}, "hawkeye_request_mutate": {"active", 840},
	"hawkeye_fuzz_run": {"active", 760}, "hawkeye_response_compare": {"audit", 810},
	"hawkeye_scope": {"scope", 930}, "hawkeye_findings": {"audit", 680},
	"hawkeye_codec": {"utility", 510}, "hawkeye_script_list": {"script", 470},
	"hawkeye_script_run": {"script", 450}, "hawkeye_evaluate": {"script", 300},
	"hawkeye_script_upsert": {"script", 430}, "hawkeye_sensitive_scan": {"audit", 800},
	"hawkeye_script_manage": {"script", 480},
	"hawkeye_darklink_scan": {"audit", 790}, "hawkeye_intercept": {"intercept", 920},
}

func hawkEyeOriginalName(tool *ExternalTool) string {
	if tool == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(firstNonEmptyMCP(tool.OriginalName, tool.Name)))
}

// HawkEyeKnownToolNames returns a stable copy for contract tests and operator
// diagnostics. It intentionally lists every public 1.0.14 tool, not just the
// small subset most often used in prompts.
func HawkEyeKnownToolNames() []string {
	names := make([]string, 0, len(hawkEyeToolProfiles))
	for name := range hawkEyeToolProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func HawkEyeToolProfileForName(name string) (HawkEyeToolProfile, bool) {
	profile, ok := hawkEyeToolProfiles[strings.ToLower(strings.TrimSpace(name))]
	return profile, ok
}

func explicitHawkEyeIdentity(tool *ExternalTool) bool {
	if tool == nil {
		return false
	}
	server := strings.ToLower(tool.Server)
	name := hawkEyeOriginalName(tool)
	return strings.Contains(server, "hawkeye") || strings.Contains(server, "hx0") ||
		strings.HasPrefix(name, "hawkeye_")
}

func hawkEyeServerSignatures(tools []*ExternalTool) map[string]bool {
	signatures := make(map[string]map[string]bool)
	trusted := make(map[string]bool)
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		server := strings.ToLower(strings.TrimSpace(tool.Server))
		if server == "" {
			continue
		}
		if strings.Contains(server, "hawkeye") || strings.Contains(server, "hx0") {
			trusted[server] = true
		}
		name := hawkEyeOriginalName(tool)
		if strings.HasPrefix(name, "hawkeye_") {
			if _, known := HawkEyeToolProfileForName(name); !known {
				continue
			}
			if signatures[server] == nil {
				signatures[server] = make(map[string]bool)
			}
			signatures[server][name] = true
		}
	}
	// Three proprietary HawkEye tools on the same MCP server are a stronger
	// signature than a generic browser_* name, while still allowing users to
	// call the server "browser" or another local alias.
	for server, names := range signatures {
		if len(names) >= 3 {
			trusted[server] = true
		}
	}
	return trusted
}

// IsHawkEyeTool recognizes explicitly named HawkEye tools and browser_* tools
// belonging to a server with a HawkEye capability signature.
func IsHawkEyeTool(tool *ExternalTool, all []ExternalTool) bool {
	if tool == nil {
		return false
	}
	if _, known := HawkEyeToolProfileForName(hawkEyeOriginalName(tool)); !known {
		return false
	}
	if explicitHawkEyeIdentity(tool) {
		return true
	}
	pointers := make([]*ExternalTool, 0, len(all))
	for i := range all {
		pointers = append(pointers, &all[i])
	}
	return hawkEyeServerSignatures(pointers)[strings.ToLower(strings.TrimSpace(tool.Server))]
}

func isHawkEyeExternalToolInSet(tool *ExternalTool, all map[string]*ExternalTool) bool {
	if explicitHawkEyeIdentity(tool) {
		_, known := HawkEyeToolProfileForName(hawkEyeOriginalName(tool))
		return known
	}
	items := make([]*ExternalTool, 0, len(all))
	for _, item := range all {
		items = append(items, item)
	}
	return IsHawkEyeTool(tool, dereferenceExternalTools(items))
}

func dereferenceExternalTools(items []*ExternalTool) []ExternalTool {
	out := make([]ExternalTool, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, *item)
		}
	}
	return out
}

func hawkEyeDropsStructuredDump(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "browser_snapshot", "browser_click", "browser_hover", "browser_type",
		"browser_press_key", "browser_select_option", "browser_navigate",
		"browser_reload", "browser_go_back", "browser_go_forward", "browser_scroll",
		"browser_wait_for", "browser_fill_form", "browser_find", "browser_read_text":
		return true
	default:
		return false
	}
}

func HawkEyeToolDescriptionOverlay(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "browser_click":
		return "DeepSentry: 点最小可交互目标（内层 a/button，不要点包裹卡片）。优先传 ref/stableId；需要名称约束时加 text=无障碍名称；element 只是说明。需要精确匹配时 exact=true。也可用 snapshot 的 stableId。B站倍速不要点菜单，用 hawkeye_evaluate 设 video.playbackRate。全屏优先 press_key key=f inputMode=trusted。清晰度下拉才用 browser_select_option。"
	case "browser_press_key":
		return "DeepSentry: 全屏按 key=f 且 inputMode=trusted（未传时会自动补 trusted）。先聚焦播放器。Chrome 使用 CDP，Firefox 使用本地原生输入；支持 inputMode=native。inputDispatched 不代表操作成功，先用 fullscreenElement 验证，验证失败不要直接重复发送。不要用 JS dispatchEvent 伪装 userActivation。"
	case "browser_select_option":
		return "DeepSentry: 清晰度等下拉用这个工具，value 传可见项。B站倍速不要用它，主路径是 hawkeye_evaluate 设置 video.playbackRate=2。不要点装饰箭头后盲点。"
	case "browser_navigate":
		return "DeepSentry: 已知 URL 用 navigate，不要反复 tabs action=new。内网/实验环境自签证书会自动绕过浏览器拦截页；公网证书告警不会自动继续，用 browser_security 诊断。搜索页拿到 BV 链接后 navigate 到 https://www.bilibili.com/video/BV.../ ；合集第 N 集加 ?p=N。"
	case "browser_tabs":
		return "DeepSentry: list 不绑定。整次任务最多 new 一次；已打开的页用 select。禁止用 tabs/new 代替 navigate。"
	case "browser_snapshot":
		return "DeepSentry: 首次 snapshot，后续优先 find 或 diff=true；boxes 仅需坐标时开。默认视口 compact 树。翻页只传 page_token=context.next_page_token（2分钟有效），不带其他参数；不要 read_file artifact。"
	case "browser_find":
		return "DeepSentry: 大页面定位优先 find，不要把 snapshot 落盘后再 grep。翻页同样只传 page_token。"
	case "browser_search":
		return "DeepSentry: 新查询必须 query 或 queries；使用精确短语/site:/filetype:/排除词。maxFetches=0..7，concurrency=auto或1..7，按证据需求选择。证据不足才改写；blockedByChallenge/degraded/stale 不代表核实。翻页只传 page_token。"
	case "browser_research":
		return "DeepSentry: 新调用必需 prompt。queries 用搜索运算符；maxPages=1..15，concurrency=auto或1..7，简单问题少读来源。核验实际正文与缺口，不把 evidenceScore 当真实性。"
	case "browser_fetch":
		return "DeepSentry: 隔离临时标签拉取公开页正文。默认会写入 HawkEye 抓包历史；只要正文可设 capture=false。私网地址会被拒绝。"
	case "browser_security":
		return "DeepSentry: 读当前页 TLS/证书结构化证据。不要用截图或正文推断证书是否可信。证据缺失时 refresh=true 做一次诊断刷新。"
	case "browser_captcha_assist":
		return "DeepSentry: 自有/登录图验证码必须走这个工具。先 action=analyze 看放大裁图，只认大号深色前景字，忽略彩线和浅色底纹。禁止 hawkeye_evaluate 像素、禁止下载 img src、禁止点击图片（会刷新）。再 action=solve authorized=true answer=识别结果。滑块用 suggestedOffsetRatio。reCAPTCHA/hCaptcha/Turnstile 等第三方挑战只报 manual_required，让用户手过。"
	case "browser_fill_form":
		return "DeepSentry: 批量填表优先 fill_form，不要用 browser_type 逐项填。submit=false 先填写并核验；submit=true 会提交，涉及生产/不可逆表单时先获得用户明确授权。"
	case "browser_exam_questions":
		return "DeepSentry: 先提取题目、选项和 frame-scoped refs，再用 browser_answer_questions 填答案，默认不会提交。cursor 用于分页，不要用 snapshot 硬抠题目。"
	case "browser_answer_questions":
		return "DeepSentry: 只填写答案，不提交。按 index/text 匹配题目，填完先核验；需要提交时另用 browser_fill_form submit=true 或明确的点击动作。"
	case "browser_handle_dialog":
		return "DeepSentry: 先读取最近 alert/confirm/prompt 证据；accept 可能确认删除、覆盖或提交，涉及副作用先问用户；dismiss 只取消。clear 会清理本地对话框记录。"
	case "browser_file_upload":
		return "DeepSentry: 只上传用户明确授权的本地文件；路径由控制端读取并传输，不要把凭据、私钥或未授权文件传给页面。上传后核验 choice/file 状态。"
	case "browser_download_file":
		return "DeepSentry: discover 只发现可下载资源；download 会启动浏览器原始下载，需要用户明确要求。成功必须以 nativeDownloadStarted=true 且返回 downloadId 为准，不要只看点击结果。"
	case "browser_read_text":
		return "DeepSentry: 长文按返回的 offset/分页继续读取，不要用 snapshot 抓全文或反复滚动。保留来源 URL 和可见文本边界。"
	case "hawkeye_evaluate":
		return "DeepSentry: 默认 MAIN 世界，能看见页面变量。B站播放：读 paused/currentTime/playbackRate；倍速设 video.playbackRate；全屏后读 fullscreenElement；黑屏时 canvas.drawImage(video)。全屏手势仍须先 press_key f。验证码、cookie、联网请求不要走 evaluate。"
	case "browser_bookmarks":
		return "DeepSentry: list/search/get/backup 为读取，按 offset/limit 分页；修改用精确 id，update/move/remove 带 expectedUrl/expectedTitle 防误改。删除/去重必须用户明确要求，核验回执；无需新建网页。"
	case "hawkeye_script_manage":
		return "DeepSentry: get(scriptId) 先读 code/updatedAt；编辑通过 script_upsert(scriptId,code,expectedUpdatedAt)；再 get 验证，明确要求执行才 script_run。enable/disable/global_enable/global_disable/remove 是持久修改。"
	case "hawkeye_script_upsert":
		return "DeepSentry: 创建必需 code；更新先 script_manage get，传 scriptId 与 expectedUpdatedAt 防覆盖。省略元数据保留原值。保存不代表执行，执行使用 script_run。"
	case "hawkeye_codec":
		return "DeepSentry: 先 catalog(query/category,summary=true)，操作数量动态，不猜字段。run 最多16步；smartdecode 用 maxDepth/maxAttempts/minScore，ai=true 会向配置模型传内容。二维码/条码多结果用 maxResults/region，网页用 page_scan(maxImages,offset)/nextOffset。检查 text/binary truncated；二进制用 workbench save_file 保留原字节，export 只导出UTF-8。翻页只传 page_token。"
	case "hawkeye_request_replay":
		return "DeepSentry: 主动请求只允许在 hawkEye scope 明确授权的 host 上执行；优先用 url/query/json 的普通 API 形式，rawRequest/recordId 仅用于精确重放。高风险，执行前确认授权范围。"
	case "hawkeye_request_mutate":
		return "DeepSentry: 只生成请求变体，不发送。先 mutate 检查参数，再决定是否用 replay/fuzz；不要把变体预览当成已执行请求。"
	case "hawkeye_fuzz_run":
		return "DeepSentry: 仅对 scope 中明确授权且开启 fuzzing 的 host 运行；最多 3 个 §...§ 点和 12 个请求。先 scope 核验，再执行，结束汇总响应差异与证据。"
	case "hawkeye_scope":
		return "DeepSentry: get 只读；set/add/remove/clear 会改变主动测试授权边界，必须先获得用户明确授权。不要把普通浏览或 replay 当成自动扩大 scope。"
	case "hawkeye_findings":
		return "DeepSentry: list/get 只读；upsert/create/update/remove 会修改本地审计记录，clear 会清空。保留 finding id 和证据来源。"
	case "hawkeye_intercept":
		return "DeepSentry: status/queue 只读；enable/disable 是本地拦截状态；release/release_all/drop 会影响真实流量，属于高风险。启用后必须 disable 收尾，避免页面挂起。"
	case "hawkeye_capture_history":
		return "DeepSentry: 默认限定 MCP 绑定 tab 和当前 host；跨 tab 查询用 targetHost。继续读取用 page_token/next_page_token，不要重复抓全量历史。"
	case "hawkeye_sensitive_scan":
		return "DeepSentry: 只读敏感信息检测；可扫内联 html/text/responseBody、单条 recordId 或 targetHost 历史。结果需脱敏后再写入报告。"
	case "hawkeye_darklink_scan":
		return "DeepSentry: 只读暗链/隐藏链接检测；无内容时扫描当前绑定 tab DOM。不要把命中的链接自动打开，先作为证据复核。"
	default:
		return ""
	}
}

func hawkEyeInteractionHint(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "browser_snapshot", "browser_click", "browser_navigate", "browser_select_option", "browser_press_key", "browser_find", "hawkeye_evaluate":
		return "[HawkEye] 精确点击: text=无障碍名 + ref，点最小可交互节点。倍速: hawkeye_evaluate 设 video.playbackRate（不要点倍速菜单；清晰度才用 browser_select_option）。全屏: press_key key=f inputMode=trusted。黑屏截图用 canvas.drawImage。翻页只传 page_token。不要 read_file snapshot artifact，不要并行连点同一页。inputDispatched=true 且 outcomeVerified!=true 时先检查页面，不能盲目重试；导航只返回预览，需要节点时另取 snapshot。"
	default:
		return ""
	}
}

func hawkEyeClickMissingTextHint(name string, args map[string]string) string {
	if !strings.EqualFold(strings.TrimSpace(name), "browser_click") {
		return ""
	}
	if strings.TrimSpace(hawkEyeArg(args, "text")) == "" && strings.TrimSpace(firstNonEmptyMCP(hawkEyeRawArg(args, "ref"), hawkEyeRawArg(args, "stableId"))) != "" {
		return `[HawkEye] 本次 click 有 ref/stableId 但没有 text=无障碍名称。ref/stableId 可单独定位；只有需要精确名称约束时才加 text 并设 exact=true。先核验本次结果，不要仅因缺少 text 就重试。`
	}
	return ""
}

func hawkEyeRawArg(args map[string]string, key string) string {
	for candidate, value := range args {
		if strings.EqualFold(strings.ReplaceAll(strings.TrimSpace(candidate), "_", ""), strings.ReplaceAll(key, "_", "")) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func hawkEyeOpaqueToken(raw string) string {
	token := strings.TrimSpace(raw)
	if token == "" || token == "0" || token == "<nil>" {
		return ""
	}
	if _, err := strconv.Atoi(token); err == nil {
		return ""
	}
	return token
}

func hawkEyeContinuationToken(args map[string]string) string {
	if token := hawkEyeOpaqueToken(hawkEyeRawArg(args, "page_token")); token != "" {
		return token
	}
	if token := hawkEyeOpaqueToken(hawkEyeRawArg(args, "next_page_token")); token != "" {
		return token
	}
	if token := hawkEyeOpaqueToken(hawkEyeRawArg(args, "next_cursor")); token != "" {
		return token
	}
	return hawkEyeOpaqueToken(hawkEyeRawArg(args, "cursor"))
}

func hawkEyeSafeEvaluate(code string) bool {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" || len(trimmed) > 2500 {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, blocked := range []string{
		"fetch(", "xmlhttprequest", "websocket", "eval(", "new function",
		"document.cookie", "localstorage", "sessionstorage", "innerhtml",
		"document.write", "postmessage", "import(", "webkitmessagehandlers",
	} {
		if strings.Contains(lower, blocked) {
			return false
		}
	}
	if strings.Contains(lower, "requestfullscreen") {
		return false
	}
	allowed := 0
	for _, needle := range []string{
		"playbackrate", "currenttime", "paused", "ended", "muted",
		"fullscreenelement", "canvas", "drawimage", "todataurl", "toblob",
		"queryselector", "video", "bpx-player",
	} {
		if strings.Contains(lower, needle) {
			allowed++
		}
	}
	return allowed >= 2
}

func hawkEyeStructuredSummary(name string, structured any) string {
	if structured == nil {
		return ""
	}
	raw, err := json.Marshal(structured)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	context, _ := payload["context"].(map[string]any)
	if context == nil && !hawkEyeDropsStructuredDump(name) {
		return ""
	}
	complete := true
	nextPageToken := ""
	if context != nil {
		if value, ok := context["complete"].(bool); ok {
			complete = value
		}
		nextPageToken = firstNonEmptyMCP(
			hawkEyeMapString(context, "next_page_token"),
			hawkEyeOpaqueToken(hawkEyeMapString(context, "next_cursor")),
		)
	}
	url := firstNonEmptyMCP(hawkEyeMapString(payload, "url"), nestedHawkEyeString(payload, "tab", "url"))
	title := firstNonEmptyMCP(hawkEyeMapString(payload, "title"), nestedHawkEyeString(payload, "tab", "title"))
	var b strings.Builder
	b.WriteString("[HawkEye 摘要]")
	if url != "" {
		fmt.Fprintf(&b, " url=%s", url)
	}
	if title != "" {
		fmt.Fprintf(&b, " title=%s", title)
	}
	if ok, exists := payload["ok"]; exists {
		fmt.Fprintf(&b, " ok=%v", ok)
	}
	for _, key := range []string{"inputDispatched", "outcomeVerified", "category", "error", "hint"} {
		if value, exists := payload[key]; exists {
			fmt.Fprintf(&b, " %s=%v", key, value)
		}
	}
	if payload["inputDispatched"] == true && payload["outcomeVerified"] != true {
		b.WriteString(" （输入已发送，先检查页面状态；不要直接重复点击或按键）")
	}
	fmt.Fprintf(&b, " complete=%v", complete)
	if !complete && nextPageToken != "" && nextPageToken != "<nil>" {
		fmt.Fprintf(&b, " next_page_token=%s （翻页只传 page_token，不要带 cursor 或其他参数）", nextPageToken)
	}
	if count, ok := payload["element_count"]; ok {
		fmt.Fprintf(&b, " element_count=%v", count)
	}
	return b.String()
}

func hawkEyeMapString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func nestedHawkEyeString(payload map[string]any, keys ...string) string {
	current := any(payload)
	for _, key := range keys {
		obj, _ := current.(map[string]any)
		if obj == nil {
			return ""
		}
		current = obj[key]
	}
	if current == nil {
		return ""
	}
	return fmt.Sprint(current)
}

func hawkEyeToolPromptPriorityInSet(tool *ExternalTool, all map[string]*ExternalTool) int {
	if all != nil {
		if !isHawkEyeExternalToolInSet(tool, all) {
			return 0
		}
	} else if !isHawkEyeExternalTool(tool) {
		return 0
	}
	if profile, ok := HawkEyeToolProfileForName(hawkEyeOriginalName(tool)); ok {
		return profile.Priority
	}
	return 100
}

func hawkEyeArg(args map[string]string, key string) string {
	for candidate, value := range args {
		if strings.EqualFold(strings.ReplaceAll(strings.TrimSpace(candidate), "_", ""), strings.ReplaceAll(key, "_", "")) {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	return ""
}

func hawkEyeTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// HawkEyeToolRisk resolves the actual action rather than assigning the entire
// MCP server one generic risk. The caller must first establish IsHawkEyeTool;
// this function deliberately fails closed on unknown action variants.
func HawkEyeToolRisk(tool *ExternalTool, args map[string]string) (risk, reason string, ok bool) {
	if tool == nil {
		return "", "", false
	}
	name := hawkEyeOriginalName(tool)
	if _, known := HawkEyeToolProfileForName(name); !known {
		return "", "", false
	}
	if hawkEyeContinuationToken(args) != "" {
		return MCPRiskLow, "HawkEye page_token 仅读取已缓存的不可变结果，不重放原动作", true
	}
	readOnly := map[string]bool{
		"browser_search": true, "browser_fetch": true, "browser_research": true,
		"browser_snapshot": true, "browser_find": true, "browser_security": true,
		"browser_get_console_logs": true, "browser_wait": true, "browser_wait_for": true,
		"browser_exam_questions": true, "browser_read_text": true,
		"hawkeye_capture_state": true, "hawkeye_capture_history": true,
		"hawkeye_capture_inspect": true, "hawkeye_request_get": true,
		"hawkeye_request_mutate": true, "hawkeye_response_compare": true,
		"hawkeye_script_list":    true,
		"hawkeye_sensitive_scan": true, "hawkeye_darklink_scan": true,
	}
	if readOnly[name] {
		return MCPRiskLow, fmt.Sprintf("HawkEye %s 为已知只读查询/离线分析", name), true
	}
	if name == "browser_screenshot" {
		if hawkEyeTruthy(hawkEyeArg(args, "save_to_file")) {
			return MCPRiskMedium, "HawkEye screenshot 会在控制端写入本地证据文件", true
		}
		return MCPRiskLow, "HawkEye screenshot 仅返回图像证据", true
	}

	switch name {
	case "browser_bookmarks":
		switch hawkEyeArg(args, "action") {
		case "list", "search", "get", "backup":
			return MCPRiskLow, "HawkEye bookmarks 仅读取书签与备份内容", true
		case "create", "update", "move":
			return MCPRiskMedium, "HawkEye bookmarks 会修改用户书签", true
		default:
			return MCPRiskHigh, "HawkEye bookmarks 删除或未知动作，必须核验明确授权", true
		}
	case "hawkeye_script_manage":
		switch hawkEyeArg(args, "action") {
		case "list", "get":
			return MCPRiskLow, "HawkEye script_manage 仅读取脚本元数据/源码", true
		default:
			return MCPRiskHigh, "HawkEye script_manage 会改变持久脚本或自动执行开关", true
		}
	case "hawkeye_codec":
		var options map[string]any
		_ = json.Unmarshal([]byte(hawkEyeRawArg(args, "options")), &options)
		if options["ai"] == true || hawkEyeTruthy(hawkEyeArg(args, "ai")) {
			return MCPRiskHigh, "HawkEye Codec AI 解码会向配置的模型发送输入/候选内容", true
		}
		if hawkEyeArg(args, "action") == "workbench" && hawkEyeArg(args, "command") != "read" {
			return MCPRiskMedium, "HawkEye Codec workbench 会修改工作台或导出/保存本地文件", true
		}
		return MCPRiskLow, "HawkEye Codec 为本地目录查询/离线计算", true
	case "hawkeye_scope":
		switch hawkEyeArg(args, "action") {
		case "", "get":
			return MCPRiskLow, "HawkEye scope get 仅读取当前授权边界", true
		case "set", "add", "remove", "clear":
			return MCPRiskHigh, "HawkEye scope 会改变主动安全测试授权边界", true
		default:
			return MCPRiskHigh, "HawkEye scope action 未知，无法确认授权边界", true
		}
	case "hawkeye_findings":
		switch hawkEyeArg(args, "action") {
		case "", "list", "get":
			return MCPRiskLow, "HawkEye findings 仅读取审计结果", true
		case "upsert", "create", "update", "remove":
			return MCPRiskMedium, "HawkEye findings 会修改本地审计记录", true
		case "clear":
			return MCPRiskHigh, "HawkEye findings clear 会清空审计记录", true
		default:
			return MCPRiskHigh, "HawkEye findings action 未知", true
		}
	case "hawkeye_intercept":
		switch hawkEyeArg(args, "action") {
		case "status", "queue":
			return MCPRiskLow, "HawkEye intercept 仅读取拦截状态/队列", true
		case "enable", "disable":
			return MCPRiskLow, "HawkEye intercept 启停是本地浏览器工作流状态，不单独弹确认", true
		case "release", "release_all":
			return MCPRiskHigh, "HawkEye intercept 会将挂起请求发送给目标", true
		case "drop":
			return MCPRiskHigh, "HawkEye intercept drop 会丢弃目标请求", true
		default:
			return MCPRiskHigh, "HawkEye intercept action 未知", true
		}
	case "browser_captcha_assist":
		if hawkEyeArg(args, "action") == "analyze" || hawkEyeArg(args, "action") == "" {
			return MCPRiskLow, "HawkEye captcha analyze 仅分析当前验证码", true
		}
		return MCPRiskMedium, "HawkEye captcha solve 会与页面交互/提交", true
	case "browser_fill_form":
		if hawkEyeTruthy(hawkEyeArg(args, "submit")) {
			return MCPRiskMedium, "HawkEye fill_form 会填写并提交表单", true
		}
		return MCPRiskLow, "HawkEye fill_form 仅填写表单字段，不提交", true
	case "browser_handle_dialog":
		switch hawkEyeArg(args, "action") {
		case "accept":
			return MCPRiskMedium, "HawkEye handle_dialog accept 会确认下一个确认/输入对话框", true
		case "", "dismiss":
			return MCPRiskLow, "HawkEye handle_dialog 仅读取对话框证据或取消", true
		default:
			return MCPRiskHigh, "HawkEye handle_dialog action 未知", true
		}
	case "browser_file_upload":
		return MCPRiskHigh, "HawkEye file_upload 会向目标页面提供本地文件", true
	case "browser_download_file":
		if action := hawkEyeArg(args, "action"); action == "" || action == "discover" {
			return MCPRiskLow, "HawkEye download discover 仅发现页面可下载资源", true
		}
		if strings.EqualFold(hawkEyeArg(args, "conflictAction"), "overwrite") {
			return MCPRiskHigh, "HawkEye download 会覆盖同名本地文件", true
		}
		return MCPRiskMedium, "HawkEye download 会启动浏览器本地下载", true
	case "browser_tabs":
		if hawkEyeArg(args, "action") == "close" {
			return MCPRiskMedium, "HawkEye tabs close 会关闭用户浏览器标签页", true
		}
		return MCPRiskLow, "HawkEye tabs list/new/select 是普通浏览器导航，不单独弹确认", true
	case "hawkeye_request_replay":
		return MCPRiskHigh, "HawkEye request_replay 会向授权目标发送主动请求", true
	case "hawkeye_fuzz_run":
		return MCPRiskHigh, "HawkEye fuzz_run 会对授权目标批量发送变体", true
	case "hawkeye_evaluate":
		if hawkEyeSafeEvaluate(hawkEyeRawArg(args, "code")) {
			return MCPRiskLow, "HawkEye evaluate 仅做播放状态/倍速/抓帧校验，不单独弹确认", true
		}
		return MCPRiskHigh, "HawkEye evaluate 会在页面主世界执行任意 JavaScript", true
	case "hawkeye_script_run", "hawkeye_script_upsert":
		return MCPRiskHigh, "HawkEye 脚本工具会保存或执行页面注入脚本", true
	case "hawkeye_capture_start", "hawkeye_capture_stop":
		return MCPRiskLow, "HawkEye capture 启停是本地取证状态，不单独弹确认", true
	case "browser_navigate", "browser_go_back", "browser_go_forward", "browser_click", "browser_hover",
		"browser_type", "browser_select_option", "browser_press_key", "browser_resize",
		"browser_reload", "browser_answer_questions",
		"browser_drag", "browser_scroll":
		return MCPRiskLow, fmt.Sprintf("HawkEye %s 属于用户已连接浏览器的常规自动化，不单独弹确认", name), true
	default:
		// Known future variants still fail closed. A genuine read-only annotation
		// is included in the reason for diagnosis but never silently trusted.
		return MCPRiskHigh, fmt.Sprintf("HawkEye %s 未配置本地动作策略（readOnlyHint=%t）", name, tool.Annotations.ReadOnlyHint), true
	}
}

// HawkEyeToolTimeout prevents DeepSentry's default 60-second client deadline
// from cancelling operations before HawkEye's own documented timeout. An
// explicit user-configured timeout always wins.
func HawkEyeToolTimeout(name string, configured time.Duration, explicitlyConfigured bool) time.Duration {
	if explicitlyConfigured {
		return configured
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "browser_research":
		return 205 * time.Second
	case "browser_search", "browser_fetch", "browser_captcha_assist", "hawkeye_fuzz_run",
		"browser_download_file", "browser_file_upload", "hawkeye_codec":
		return 130 * time.Second
	case "browser_snapshot", "browser_navigate", "browser_reload", "browser_read_text", "browser_find",
		"browser_screenshot", "browser_go_back", "browser_go_forward", "browser_wait_for", "browser_scroll",
		"browser_fill_form", "browser_answer_questions", "browser_exam_questions",
		"hawkeye_capture_history", "hawkeye_request_replay", "hawkeye_response_compare",
		"hawkeye_script_run", "hawkeye_sensitive_scan", "hawkeye_darklink_scan":
		return 100 * time.Second
	default:
		return configured
	}
}

func preferExistingHawkEyeHTTP(cfg ServerConfig) (ServerConfig, bool) {
	if !isHawkEyeStdioConfig(cfg) {
		return cfg, false
	}
	port := hawkEyePortFromConfig(cfg)
	if port <= 0 {
		return cfg, false
	}
	if !hawkEyeHTTPReady(hawkEyeMCPURL(cfg)) && !hawkEyePortOccupied(port) {
		return cfg, false
	}
	return hawkEyeStreamableHTTPConfig(cfg), true
}

func hawkEyeStreamableHTTPConfig(cfg ServerConfig) ServerConfig {
	cfg.Type = "streamable_http"
	cfg.URL = hawkEyeMCPURL(cfg)
	cfg.Command = ""
	cfg.Args = nil
	return cfg
}

func hawkEyeMCPURL(cfg ServerConfig) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", hawkEyePortFromConfig(cfg))
}

func isHawkEyeStdioConfig(cfg ServerConfig) bool {
	transport := strings.ToLower(strings.TrimSpace(cfg.Type))
	if transport != "" && transport != "stdio" {
		return false
	}
	blob := strings.ToLower(strings.Join([]string{cfg.Name, cfg.Command, strings.Join(cfg.Args, " ")}, " "))
	return strings.Contains(blob, "hawkeye") || strings.Contains(blob, "hx0")
}

func hawkEyePortFromConfig(cfg ServerConfig) int {
	if port := hawkEyePortFromArgs(cfg.Args); port > 0 {
		return port
	}
	if cfg.Env != nil {
		if port := hawkEyePortFromRaw(cfg.Env["HX0_MCP_WS_PORT"]); port > 0 {
			return port
		}
	}
	return 19016
}

func hawkEyePortFromArgs(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--port" || arg == "--ws-port":
			if i+1 < len(args) {
				return hawkEyePortFromRaw(args[i+1])
			}
		case strings.HasPrefix(arg, "--port="):
			return hawkEyePortFromRaw(strings.TrimPrefix(arg, "--port="))
		case strings.HasPrefix(arg, "--ws-port="):
			return hawkEyePortFromRaw(strings.TrimPrefix(arg, "--ws-port="))
		}
	}
	return 0
}

func hawkEyePortFromRaw(raw string) int {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 1024 || port > 65535 {
		return 0
	}
	return port
}

func hawkEyePortOccupied(port int) bool {
	if port < 1024 || port > 65535 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hawkEyeHTTPReady(rawURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return hawkEyePortOccupiedFromURL(rawURL)
	}
	defer resp.Body.Close()
	if hawkEyeHTTPIndicatesMCP(resp) {
		return true
	}
	return hawkEyePortOccupiedFromURL(rawURL)
}

func hawkEyeHTTPIndicatesMCP(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream") {
			return true
		}
		buf := make([]byte, 128)
		n, _ := resp.Body.Read(buf)
		return strings.Contains(string(buf[:n]), "HawkEye") || strings.Contains(strings.ToLower(string(buf[:n])), "mcp")
	case http.StatusNoContent, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotAcceptable:
		return true
	}
	return strings.Contains(strings.ToUpper(resp.Header.Get("Allow")), "POST")
}

func hawkEyePortOccupiedFromURL(rawURL string) bool {
	host, err := hawkEyeHostPort(rawURL)
	if err != nil {
		return false
	}
	conn, dialErr := net.DialTimeout("tcp", host, 300*time.Millisecond)
	if dialErr != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hawkEyeHostPort(rawURL string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		trimmed = trimmed[:i]
	}
	if trimmed == "" {
		return "", fmt.Errorf("empty host")
	}
	if !strings.Contains(trimmed, ":") {
		trimmed += ":80"
	}
	return trimmed, nil
}
