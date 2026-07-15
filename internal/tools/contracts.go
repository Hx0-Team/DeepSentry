package tools

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ArgSpec describes one canonical argument exposed to the LLM. Runtime
// implementations may keep accepting legacy aliases, but the model only sees
// the canonical spelling so calls remain predictable.
type ArgSpec struct {
	Name        string
	Type        string
	Description string
	Required    bool
	Enum        []string
	Example     string
}

// ActionSpec documents action-specific requirements for tools that multiplex
// several operations behind an action argument.
type ActionSpec struct {
	Name        string
	Description string
	Required    []string
	AnyOf       [][]string
	Example     map[string]string
}

// ToolContract is the single source of truth used by native tool schemas,
// catalog/help output and runtime validation.
type ToolContract struct {
	Args             []ArgSpec
	Actions          []ActionSpec
	Examples         []map[string]string
	AnyOf            [][]string
	AllowUnknownArgs bool
}

var explicitContracts = map[string]ToolContract{
	"tool_catalog": {
		Args: []ArgSpec{
			{Name: "category", Type: "string", Description: "工具分类；不确定时使用 all", Example: "all"},
			{Name: "query", Type: "string", Description: "工具名或若干搜索关键词，支持空格分词", Example: "ssh target"},
			{Name: "name", Type: "string", Description: "精确查看某个工具的完整参数、动作和示例", Example: "config_manage"},
		},
		Examples: []map[string]string{{"name": "config_manage"}, {"category": "批量运维", "query": "ssh"}},
	},
	"config_manage": {
		Args: []ArgSpec{
			{Name: "action", Type: "string", Description: "配置管理动作", Required: true, Enum: []string{"status", "get", "validate", "backup", "add_skill_source", "add_mcp_server", "import_claude_mcp", "disable_mcp_server", "enable_mcp_server", "remove_mcp_server", "disable_skill_source", "enable_skill_source", "remove_skill_source", "add_target", "enable_fleet", "set_ssh", "set", "replace_yaml"}},
			{Name: "config_path", Type: "string", Description: "可选配置路径；通常留空使用当前 DeepSentry 配置"},
			{Name: "key", Type: "string", Description: "get/set 使用的配置键"},
			{Name: "value", Type: "string", Description: "set 使用的值"},
			{Name: "source", Type: "string", Description: "Skill 来源目录或导入文件路径"},
			{Name: "name", Type: "string", Description: "目标、MCP Server 等对象名称"},
			{Name: "spec", Type: "string", Description: "MCP Server 简写配置"},
			{Name: "protocol", Type: "string", Description: "目标协议", Enum: []string{"ssh", "telnet", "ftp"}, Example: "ssh"},
			{Name: "host", Type: "string", Description: "主机名或 IP；可包含端口，也可另传 port", Example: "10.0.0.8"},
			{Name: "port", Type: "integer", Description: "目标端口；SSH 默认 22、Telnet 默认 23、FTP 默认 21", Example: "2222"},
			{Name: "user", Type: "string", Description: "登录用户名；请使用 user 作为规范字段", Example: "root"},
			{Name: "password", Type: "string", Description: "登录密码（敏感，不会在结果中回显）"},
			{Name: "key_path", Type: "string", Description: "SSH 私钥路径"},
			{Name: "prompt", Type: "string", Description: "Telnet prompt 提示符"},
			{Name: "tags", Type: "string", Description: "逗号分隔标签", Example: "prod,web"},
			{Name: "content", Type: "string", Description: "replace_yaml/import 使用的 YAML 或 JSON 内容"},
			{Name: "command", Type: "string", Description: "MCP stdio command"},
			{Name: "args", Type: "string", Description: "MCP command 的逗号分隔参数"},
			{Name: "cwd", Type: "string", Description: "MCP stdio 工作目录"},
			{Name: "env", Type: "string", Description: "MCP 环境变量，逗号分隔 KEY=VALUE"},
			{Name: "url", Type: "string", Description: "MCP HTTP/SSE URL"},
			{Name: "type", Type: "string", Description: "MCP Server 类型；当前仅支持 stdio", Enum: []string{"stdio"}},
			{Name: "clear_single", Type: "boolean", Description: "enable_fleet 后是否清理旧单目标字段"},
		},
		Actions: []ActionSpec{
			{Name: "status", Description: "查看脱敏配置摘要"},
			{Name: "get", Description: "读取一个脱敏配置项", Required: []string{"key"}, Example: map[string]string{"action": "get", "key": "targets"}},
			{Name: "validate", Description: "验证当前 YAML 是否可解析"},
			{Name: "backup", Description: "创建一份权限受控的配置备份"},
			{Name: "add_target", Description: "新增或更新 Fleet SSH/Telnet/FTP 目标", Required: []string{"protocol", "host", "user"}, Example: map[string]string{"action": "add_target", "protocol": "ssh", "host": "10.0.0.8", "port": "2222", "user": "root", "password": "<password>", "tags": "prod"}},
			{Name: "set_ssh", Description: "设置当前单目标 SSH", Required: []string{"host", "user"}, Example: map[string]string{"action": "set_ssh", "host": "10.0.0.8", "port": "2222", "user": "root", "password": "<password>"}},
			{Name: "enable_fleet", Description: "将单目标纳入 targets 并切换为 Fleet 控制端模式；也可同时传新目标"},
			{Name: "add_skill_source", Description: "添加 Skill 来源目录", Required: []string{"source"}},
			{Name: "disable_skill_source", Description: "禁用 Skill 来源", Required: []string{"source"}},
			{Name: "enable_skill_source", Description: "重新启用 Skill 来源", Required: []string{"source"}},
			{Name: "remove_skill_source", Description: "移除 Skill 来源", Required: []string{"source"}},
			{Name: "add_mcp_server", Description: "添加 MCP Server；使用 spec，或使用 name+command/url 结构化参数"},
			{Name: "import_claude_mcp", Description: "从 content 或 source 指向的 Claude Desktop JSON 导入 MCP"},
			{Name: "disable_mcp_server", Description: "禁用 MCP Server", Required: []string{"name"}},
			{Name: "enable_mcp_server", Description: "重新启用 MCP Server", Required: []string{"name"}},
			{Name: "remove_mcp_server", Description: "移除 MCP Server", Required: []string{"name"}},
			{Name: "set", Description: "设置允许修改的标量配置", Required: []string{"key", "value"}},
			{Name: "replace_yaml", Description: "验证、备份并替换完整 YAML", Required: []string{"content"}},
		},
		Examples: []map[string]string{
			{"action": "status"},
			{"action": "add_target", "protocol": "ssh", "host": "10.0.0.8", "port": "2222", "user": "root", "password": "<password>", "tags": "prod"},
		},
	},
	"fleet_inventory": {
		Args:     []ArgSpec{{Name: "selector", Type: "string", Description: "all、目标名称、host、protocol、tag 或逗号分隔组合", Example: "all"}},
		Examples: []map[string]string{{"selector": "all"}, {"selector": "prod,ssh"}},
	},
	"fleet_exec": {
		Args: []ArgSpec{
			{Name: "selector", Type: "string", Description: "目标名称、host、protocol、tag 或逗号分隔组合；留空表示 all", Example: "prod,ssh"},
			{Name: "command", Type: "string", Description: "在每个选中 SSH/Telnet 目标执行的命令", Required: true, Example: "uptime"},
			{Name: "concurrency", Type: "integer", Description: "并发数，默认 5，最大 20", Example: "5"},
		},
		Examples: []map[string]string{{"selector": "prod,ssh", "command": "uptime && df -h", "concurrency": "5"}},
	},
	"fleet_file": {
		Args: []ArgSpec{
			{Name: "selector", Type: "string", Description: "目标选择器；留空表示 all"},
			{Name: "action", Type: "string", Description: "文件动作", Required: true, Enum: []string{"ls", "read", "download", "upload"}},
			{Name: "remote_path", Type: "string", Description: "目标机路径", Required: true, Example: "/var/log/auth.log"},
			{Name: "local_path", Type: "string", Description: "download/upload 使用的控制端路径"},
		},
		Actions: []ActionSpec{
			{Name: "ls", Description: "列目录", Required: []string{"remote_path"}},
			{Name: "read", Description: "读取文件", Required: []string{"remote_path"}},
			{Name: "download", Description: "下载到控制端", Required: []string{"remote_path", "local_path"}},
			{Name: "upload", Description: "上传到目标机", Required: []string{"remote_path", "local_path"}},
		},
		Examples: []map[string]string{{"selector": "prod", "action": "read", "remote_path": "/var/log/auth.log"}},
	},
	"file_download": {
		Args: []ArgSpec{
			{Name: "remote_path", Type: "string", Description: "目标机源文件路径", Required: true, Example: "/var/log/auth.log"},
			{Name: "local_path", Type: "string", Description: "控制端目标文件路径", Required: true, Example: "~/.deepsentry/workspace/auth.log"},
			{Name: "chunk_size", Type: "integer", Description: "分块大小（字节），默认 4194304"},
		},
		Examples: []map[string]string{{"remote_path": "/var/log/auth.log", "local_path": "~/.deepsentry/workspace/auth.log"}},
	},
	"file_upload": {
		Args: []ArgSpec{
			{Name: "local_path", Type: "string", Description: "控制端源文件路径", Required: true, Example: "~/.deepsentry/workspace/check.sh"},
			{Name: "remote_path", Type: "string", Description: "目标机目标文件路径", Required: true, Example: "/tmp/check.sh"},
			{Name: "chunk_size", Type: "integer", Description: "分块大小（字节），默认 4194304"},
		},
		Examples: []map[string]string{{"local_path": "~/.deepsentry/workspace/check.sh", "remote_path": "/tmp/check.sh"}},
	},
	"archive_pack":    archiveContract("将 source 打包到 dest"),
	"archive_extract": archiveContract("将 source 安全解压到 dest"),
	"script_run": {
		Args: []ArgSpec{
			{Name: "language", Type: "string", Description: "脚本语言，默认 python", Enum: []string{"python", "shell"}},
			{Name: "content", Type: "string", Description: "脚本正文；与 path 至少提供一个"},
			{Name: "path", Type: "string", Description: "已有脚本路径；与 content 至少提供一个"},
			{Name: "args", Type: "string", Description: "传给脚本的命令行参数"},
			{Name: "timeout", Type: "integer", Description: "超时秒数，默认 30，最大 300"},
		},
		AnyOf:    [][]string{{"content", "path"}},
		Examples: []map[string]string{{"language": "python", "content": "print('ok')", "timeout": "30"}, {"language": "shell", "path": "/tmp/check.sh"}},
	},
	"tcp_forward": {
		Args: []ArgSpec{
			{Name: "action", Type: "string", Description: "转发动作", Required: true, Enum: []string{"start", "list", "stop"}},
			{Name: "listen_host", Type: "string", Description: "监听地址，默认 127.0.0.1"},
			{Name: "listen_port", Type: "integer", Description: "监听端口；start/stop 必填"},
			{Name: "target_host", Type: "string", Description: "目标主机；start 必填"},
			{Name: "target_port", Type: "integer", Description: "目标端口；start 必填"},
		},
		Actions: []ActionSpec{
			{Name: "start", Description: "启动临时 TCP 转发", Required: []string{"listen_port", "target_host", "target_port"}, Example: map[string]string{"action": "start", "listen_port": "18080", "target_host": "10.0.0.8", "target_port": "8080"}},
			{Name: "list", Description: "列出当前转发"},
			{Name: "stop", Description: "按监听端口停止转发", Required: []string{"listen_port"}},
		},
		Examples: []map[string]string{{"action": "list"}, {"action": "start", "listen_port": "18080", "target_host": "10.0.0.8", "target_port": "8080"}},
	},
	"socks5_proxy": {
		Args: []ArgSpec{
			{Name: "action", Type: "string", Description: "代理动作", Required: true, Enum: []string{"start", "list", "stop"}},
			{Name: "listen_host", Type: "string", Description: "监听地址，默认 127.0.0.1"},
			{Name: "listen_port", Type: "integer", Description: "监听端口，start 默认 1080；stop 必填"},
			{Name: "username", Type: "string", Description: "可选认证用户名；必须与 password 同时提供"},
			{Name: "password", Type: "string", Description: "可选认证密码；必须与 username 同时提供"},
			{Name: "allow_lan", Type: "boolean", Description: "是否明确允许监听非回环地址，默认 false"},
		},
		Actions: []ActionSpec{
			{Name: "start", Description: "启动临时 SOCKS5 代理"},
			{Name: "list", Description: "列出当前代理"},
			{Name: "stop", Description: "按监听端口停止代理", Required: []string{"listen_port"}},
		},
		Examples: []map[string]string{{"action": "list"}, {"action": "start", "listen_host": "127.0.0.1", "listen_port": "1080", "allow_lan": "false"}},
	},
	"schedule_task": {
		Args: []ArgSpec{
			{Name: "action", Type: "string", Description: "定时任务动作", Required: true, Enum: []string{"plan", "add", "list", "remove", "run", "run-due"}},
			{Name: "text", Type: "string", Description: "plan/add 的自然语言时间与任务描述"},
			{Name: "task", Type: "string", Description: "plan/add 的任务正文"},
			{Name: "run_at", Type: "string", Description: "明确执行时间"},
			{Name: "repeat", Type: "string", Description: "重复规则"},
			{Name: "notify", Type: "string", Description: "dingtalk、feishu、email，可逗号多选"},
			{Name: "selector", Type: "string", Description: "目标选择器"},
			{Name: "kind", Type: "string", Description: "任务类型", Enum: []string{"inspection", "agent"}},
			{Name: "id", Type: "string", Description: "remove/run 的任务 ID"},
			{Name: "timezone", Type: "string", Description: "IANA 时区"},
			{Name: "report", Type: "boolean", Description: "是否生成报告"},
			{Name: "allow_batch", Type: "boolean", Description: "是否允许无人值守 Agent 批处理"},
			{Name: "confirm_unattended", Type: "boolean", Description: "显式确认无人值守 Agent；kind=agent 的 add 必填 true"},
		},
		Actions: []ActionSpec{
			{Name: "plan", Description: "只解析计划，不落盘", AnyOf: [][]string{{"text", "task"}}},
			{Name: "add", Description: "创建定时任务", AnyOf: [][]string{{"text", "task"}}},
			{Name: "list", Description: "列出任务"},
			{Name: "remove", Description: "删除任务", Required: []string{"id"}},
			{Name: "run", Description: "立即执行任务", Required: []string{"id"}},
			{Name: "run-due", Description: "执行到期任务"},
		},
		Examples: []map[string]string{{"action": "list"}, {"action": "plan", "text": "每天上午 9 点巡检生产服务器"}},
	},
	"tsecbench": {
		Args: []ArgSpec{
			{Name: "action", Type: "string", Description: "平台动作", Required: true, Enum: []string{"list", "status", "check", "start", "hint", "submit", "close", "probe"}},
			{Name: "unique_code", Type: "string", Description: "题目标识；start/hint/submit/close 必填"},
			{Name: "flag", Type: "string", Description: "submit 提交的 flag"},
			{Name: "addr", Type: "string", Description: "probe 的 URL 或 host:port"},
			{Name: "probe", Type: "boolean", Description: "start 后是否自动探活"},
			{Name: "timeout", Type: "integer", Description: "探活超时秒数"},
			{Name: "limit", Type: "integer", Description: "list 返回条数"},
			{Name: "status", Type: "string", Description: "list 状态过滤"},
			{Name: "difficulty", Type: "string", Description: "list 难度过滤"},
			{Name: "raw", Type: "boolean", Description: "是否返回原始响应"},
			{Name: "base_url", Type: "string", Description: "可选平台地址；通常使用配置值"},
			{Name: "token", Type: "string", Description: "可选平台令牌；通常使用配置值"},
		},
		Actions: []ActionSpec{
			{Name: "list", Description: "列出题目"}, {Name: "status", Description: "列出题目状态"}, {Name: "check", Description: "检查题目"},
			{Name: "start", Description: "启动题目容器", Required: []string{"unique_code"}},
			{Name: "hint", Description: "获取提示，可能扣分", Required: []string{"unique_code"}},
			{Name: "submit", Description: "提交 flag", Required: []string{"unique_code", "flag"}},
			{Name: "close", Description: "关闭题目容器", Required: []string{"unique_code"}},
			{Name: "probe", Description: "探测题目地址", Required: []string{"addr"}},
		},
		Examples: []map[string]string{{"action": "list", "limit": "20"}, {"action": "start", "unique_code": "demo-01", "probe": "true"}},
	},
}

func archiveContract(description string) ToolContract {
	return ToolContract{
		Args: []ArgSpec{
			{Name: "format", Type: "string", Description: "归档格式；留空时按扩展名推断", Enum: []string{"zip", "tar", "tar.gz", "rar", "7z"}},
			{Name: "source", Type: "string", Description: "源路径", Required: true},
			{Name: "dest", Type: "string", Description: "目标路径", Required: true},
		},
		Examples: []map[string]string{{"format": "tar.gz", "source": "/var/log", "dest": "/tmp/logs.tar.gz"}},
	}
}

// Contract returns the canonical contract for any enabled built-in tool.
// Explicit contracts cover workflow-heavy tools; simple tools are derived from
// their compact ArgsHint so every built-in still gets a native schema.
func Contract(name string) (ToolContract, bool) {
	if c, ok := explicitContracts[name]; ok {
		return cloneContract(c), true
	}
	t, ok := Get(name)
	if !ok {
		return ToolContract{}, false
	}
	args := inferArgs(t.ArgsHint)
	return ToolContract{Args: args, Examples: []map[string]string{exampleForArgs(args)}}, true
}

func cloneContract(in ToolContract) ToolContract {
	out := in
	out.Args = append([]ArgSpec(nil), in.Args...)
	out.Actions = append([]ActionSpec(nil), in.Actions...)
	for i := range out.Actions {
		out.Actions[i].Required = append([]string(nil), in.Actions[i].Required...)
		out.Actions[i].AnyOf = cloneStringGroups(in.Actions[i].AnyOf)
		out.Actions[i].Example = cloneStringMap(in.Actions[i].Example)
	}
	out.Examples = append([]map[string]string(nil), in.Examples...)
	for i := range out.Examples {
		out.Examples[i] = cloneStringMap(in.Examples[i])
	}
	out.AnyOf = cloneStringGroups(in.AnyOf)
	return out
}

func cloneStringGroups(in [][]string) [][]string {
	out := make([][]string, len(in))
	for i := range in {
		out[i] = append([]string(nil), in[i]...)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// JSONSchema returns an OpenAI-compatible function parameter schema.
func JSONSchema(name string) map[string]interface{} {
	contract, ok := Contract(name)
	if !ok {
		return map[string]interface{}{"type": "object", "additionalProperties": true}
	}
	props := make(map[string]interface{}, len(contract.Args))
	required := make([]string, 0)
	for _, arg := range contract.Args {
		p := map[string]interface{}{
			"type":        valueOr(arg.Type, "string"),
			"description": arg.Description,
		}
		if len(arg.Enum) > 0 {
			p["enum"] = arg.Enum
		}
		props[arg.Name] = p
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]interface{}{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": contract.AllowUnknownArgs,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	if len(contract.AnyOf) > 0 {
		groups := make([]interface{}, 0, len(contract.AnyOf))
		for _, group := range contract.AnyOf {
			anyOf := make([]interface{}, 0, len(group))
			for _, key := range group {
				anyOf = append(anyOf, map[string]interface{}{"required": []string{key}})
			}
			groups = append(groups, map[string]interface{}{"anyOf": anyOf})
		}
		if len(groups) == 1 {
			schema["anyOf"] = groups[0].(map[string]interface{})["anyOf"]
		} else {
			schema["allOf"] = groups
		}
	}
	return schema
}

// ValidateCall catches hallucinated/omitted parameters before a tool mutates
// state. It deliberately validates canonical names; legacy aliases remain an
// implementation compatibility detail rather than something taught to models.
func ValidateCall(name string, args map[string]string) error {
	contract, ok := Contract(name)
	if !ok {
		return fmt.Errorf("未知工具 %q%s", name, suggestionSuffix(name, ListNames()))
	}
	known := make(map[string]ArgSpec, len(contract.Args))
	for _, spec := range contract.Args {
		known[spec.Name] = spec
		if spec.Required && strings.TrimSpace(args[spec.Name]) == "" {
			return fmt.Errorf("工具 %s 缺少必填参数 %s\n%s", name, spec.Name, FormatToolHelp(name))
		}
	}
	for key, spec := range known {
		value := strings.TrimSpace(args[key])
		if value == "" {
			continue
		}
		if err := validateArgType(spec, value); err != nil {
			return fmt.Errorf("工具 %s 参数 %s=%q 无效: %v\n%s", name, key, value, err, FormatToolHelp(name))
		}
		if len(spec.Enum) > 0 {
			matched := false
			for _, allowed := range spec.Enum {
				if strings.EqualFold(value, allowed) {
					matched = true
					break
				}
			}
			if !matched {
				correction := suggestionSuffix(value, spec.Enum)
				if name == "config_manage" && key == "action" {
					switch value {
					case "ssh_add_target", "create_target", "update_target", "upsert_target":
						correction = "；添加或更新目标请使用 action=add_target"
					case "list_targets", "get_targets":
						correction = "；列出目标请调用 fleet_inventory，而不是 config_manage"
					case "help", "describe":
						correction = "；查看用法请调用 tool_catalog 并传 name=config_manage"
					}
				}
				return fmt.Errorf("工具 %s 参数 %s=%q 无效；可选值: %s%s\n%s", name, key, value, strings.Join(spec.Enum, "|"), correction, FormatToolHelp(name))
			}
		}
	}
	if !contract.AllowUnknownArgs {
		var unknown []string
		for key := range args {
			if _, ok := known[key]; !ok {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf("工具 %s 收到未知参数: %s。请只使用文档中的规范参数\n%s", name, strings.Join(unknown, ", "), FormatToolHelp(name))
		}
	}
	if missing := missingAnyOf(args, contract.AnyOf); missing != "" {
		return fmt.Errorf("工具 %s 至少需要提供以下参数之一: %s\n%s", name, missing, FormatToolHelp(name))
	}
	if action := strings.TrimSpace(args["action"]); action != "" && len(contract.Actions) > 0 {
		for _, spec := range contract.Actions {
			if spec.Name != action {
				continue
			}
			for _, key := range spec.Required {
				if strings.TrimSpace(args[key]) == "" {
					return fmt.Errorf("工具 %s action=%s 缺少必填参数 %s\n%s", name, action, key, FormatToolHelp(name))
				}
			}
			if missing := missingAnyOf(args, spec.AnyOf); missing != "" {
				return fmt.Errorf("工具 %s action=%s 至少需要提供以下参数之一: %s\n%s", name, action, missing, FormatToolHelp(name))
			}
			return nil
		}
	}
	return nil
}

func validateArgType(spec ArgSpec, value string) error {
	switch valueOr(spec.Type, "string") {
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("必须是整数")
		}
	case "number":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("必须是数字")
		}
	case "boolean":
		switch strings.ToLower(value) {
		case "true", "false", "1", "0", "yes", "no", "y", "n", "on", "off":
		default:
			return fmt.Errorf("必须是 true/false")
		}
	}
	return nil
}

func missingAnyOf(args map[string]string, groups [][]string) string {
	for _, group := range groups {
		found := false
		for _, key := range group {
			if strings.TrimSpace(args[key]) != "" {
				found = true
				break
			}
		}
		if !found {
			return strings.Join(group, " | ")
		}
	}
	return ""
}

func FormatToolHelp(name string) string {
	contract, ok := Contract(name)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("用法 ")
	b.WriteString(name)
	b.WriteString(":\n")
	for _, arg := range contract.Args {
		b.WriteString("- ")
		b.WriteString(arg.Name)
		b.WriteString(" (")
		b.WriteString(valueOr(arg.Type, "string"))
		if arg.Required {
			b.WriteString(", 必填")
		}
		if len(arg.Enum) > 0 {
			b.WriteString(", ")
			b.WriteString(strings.Join(arg.Enum, "|"))
		}
		b.WriteString("): ")
		b.WriteString(arg.Description)
		b.WriteString("\n")
	}
	if len(contract.Actions) > 0 {
		b.WriteString("动作:\n")
		for _, action := range contract.Actions {
			b.WriteString("- ")
			b.WriteString(action.Name)
			b.WriteString(": ")
			b.WriteString(action.Description)
			if len(action.Required) > 0 {
				b.WriteString("；需要 ")
				b.WriteString(strings.Join(action.Required, ", "))
			}
			for _, group := range action.AnyOf {
				b.WriteString("；至少一个 ")
				b.WriteString(strings.Join(group, " | "))
			}
			b.WriteString("\n")
		}
	}
	for _, group := range contract.AnyOf {
		b.WriteString("约束: 至少提供 ")
		b.WriteString(strings.Join(group, " | "))
		b.WriteString(" 之一\n")
	}
	for _, example := range contract.Examples {
		b.WriteString("示例: {\"action\":\"tool\",\"tool_name\":\"")
		b.WriteString(name)
		b.WriteString("\",\"tool_args\":{")
		keys := make([]string, 0, len(example))
		for key := range example {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for i, key := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(fmt.Sprintf("%q:%q", key, example[key]))
		}
		b.WriteString("}}\n")
	}
	return strings.TrimSpace(b.String())
}

func inferArgs(hint string) []ArgSpec {
	hint = strings.TrimSpace(strings.SplitN(hint, "；", 2)[0])
	if hint == "" || hint == "无参数" {
		return nil
	}
	parts := splitTopLevel(hint)
	args := make([]ArgSpec, 0, len(parts))
	seen := map[string]bool{}
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// A few hints use "content 或 path" to express alternatives.
		alternatives := []string{raw}
		if strings.Contains(raw, " 或 ") {
			alternatives = strings.Split(raw, " 或 ")
		}
		for _, alternative := range alternatives {
			spec := inferArg(strings.TrimSpace(alternative))
			if spec.Name == "" || seen[spec.Name] {
				continue
			}
			seen[spec.Name] = true
			args = append(args, spec)
		}
	}
	return args
}

func inferArg(raw string) ArgSpec {
	name := raw
	detail := ""
	if idx := strings.Index(raw, "("); idx >= 0 {
		name = strings.TrimSpace(raw[:idx])
		detail = strings.TrimSuffix(strings.TrimSpace(raw[idx+1:]), ")")
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, "秒"))
	if slash := strings.Index(name, "/"); slash > 0 {
		name = name[:slash]
	}
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ArgSpec{}
	}
	name = fields[0]
	if !validArgName(name) {
		return ArgSpec{}
	}
	spec := ArgSpec{Name: name, Type: inferArgType(name), Description: detail}
	if strings.Contains(detail, "必填") || name == "action" {
		spec.Required = true
	}
	if strings.Contains(detail, "|") && !strings.Contains(detail, "默认") {
		for _, item := range strings.Split(detail, "|") {
			item = strings.TrimSpace(strings.Trim(item, "()"))
			if item != "" && validEnumValue(item) {
				spec.Enum = append(spec.Enum, item)
			}
		}
	}
	if len(spec.Enum) == 0 && strings.Contains(detail, "/") && !strings.Contains(detail, "默认") {
		parts := strings.Split(detail, "/")
		valid := len(parts) > 1
		for _, item := range parts {
			item = strings.TrimSpace(item)
			if !validEnumValue(item) {
				valid = false
				break
			}
		}
		if valid {
			spec.Enum = append(spec.Enum, parts...)
		}
	}
	return spec
}

func exampleForArgs(args []ArgSpec) map[string]string {
	example := map[string]string{}
	for _, arg := range args {
		if !arg.Required {
			continue
		}
		value := arg.Example
		if value == "" && len(arg.Enum) > 0 {
			value = arg.Enum[0]
		}
		if value == "" {
			switch arg.Type {
			case "integer":
				value = "1"
			case "boolean":
				value = "true"
			default:
				value = "<" + arg.Name + ">"
			}
		}
		example[arg.Name] = value
	}
	return example
}

func splitTopLevel(s string) []string {
	var out []string
	start, depth := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',', '，':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + len(string(r))
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func inferArgType(name string) string {
	for _, token := range []string{"count", "limit", "timeout", "port", "lines", "interval", "max_", "chunk_size", "concurrency", "wait_ms"} {
		if name == token || strings.HasPrefix(name, token) || strings.HasSuffix(name, "_"+token) {
			return "integer"
		}
	}
	for _, token := range []string{"allow_lan", "allow_batch", "confirm_unattended", "probe", "raw"} {
		if name == token {
			return "boolean"
		}
	}
	return "string"
}

func validArgName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}

func validEnumValue(value string) bool {
	if value == "" || strings.ContainsAny(value, " 空，,。:：/") {
		return false
	}
	return true
}

func suggestionSuffix(got string, candidates []string) string {
	best, bestDistance := "", 4
	for _, candidate := range candidates {
		if d := editDistance(strings.ToLower(got), strings.ToLower(candidate)); d < bestDistance {
			best, bestDistance = candidate, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("；你是否想用 %q？", best)
}

func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ra := range ar {
		cur := make([]int, len(br)+1)
		cur[0] = i + 1
		for j, rb := range br {
			cost := 0
			if ra != rb {
				cost = 1
			}
			cur[j+1] = min3(cur[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	if a < b && a < c {
		return a
	}
	if b < c {
		return b
	}
	return c
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
