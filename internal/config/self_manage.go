package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// Serialize the complete read/validate/backup/write/reload transaction, including
// tool batches. This lock does not replace the application's config ownership.
var manageConfigMu sync.Mutex

func managedFieldType(key string) (reflect.Type, bool) {
	parts := strings.Split(key, ".")
	if len(parts) == 1 && !allowedScalarConfigKey(key) {
		return nil, false
	}
	if len(parts) > 1 && parts[0] != "execution_budget" && parts[0] != "balance_query" && parts[0] != "search" {
		return nil, false
	}
	typ := reflect.TypeOf(Config{})
	for _, part := range parts {
		if typ.Kind() != reflect.Struct {
			return nil, false
		}
		found := false
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Tag.Get("mapstructure") == part {
				typ, found = field.Type, true
				if typ.Kind() == reflect.Pointer {
					typ = typ.Elem()
				}
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return typ, true
}

func managedValueNode(key string, value any) (*yaml.Node, error) {
	typ, ok := managedFieldType(key)
	if !ok {
		return nil, fmt.Errorf("不支持配置键 %s；用 action=describe 查询允许的键，MCP/Skill/目标使用专用动作", key)
	}
	if value == nil {
		return nil, fmt.Errorf("%s 不接受 null；清空字符串用空字符串，自动值用 0", key)
	}
	// set retains string arguments for compatibility. patch accepts actual JSON
	// types too, but never silently coerces an object into a string.
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case json.Number:
		raw = v.String()
	case bool:
		raw = strconv.FormatBool(v)
	default:
		if typ.Kind() != reflect.Map {
			return nil, fmt.Errorf("%s 值类型错误，需要 %s", key, typ.Kind())
		}
	}
	var normalized any
	switch typ.Kind() {
	case reflect.String:
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("%s 需要字符串", key)
		}
		normalized = raw
	case reflect.Bool:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "yes", "on":
			normalized = true
		case "false", "0", "no", "off":
			normalized = false
		default:
			return nil, fmt.Errorf("%s 必须是 true|false", key)
		}
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, typ.Bits())
		if err != nil {
			return nil, fmt.Errorf("%s 需要整数", key)
		}
		normalized = n
	case reflect.Float64:
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%s 需要有限数字", key)
		}
		normalized = n
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s 需要 JSON 字符串映射", key)
		}
		for _, v := range obj {
			if _, ok := v.(string); !ok {
				return nil, fmt.Errorf("%s 所有值必须是字符串", key)
			}
		}
		normalized = obj
	default:
		return nil, fmt.Errorf("%s 需要专用配置动作", key)
	}
	if err := validateManagedScalar(key, raw); err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := node.Encode(normalized); err != nil {
		return nil, fmt.Errorf("编码 %s 失败", key)
	}
	return &node, nil
}

func configNodeAt(root *yaml.Node, key string) *yaml.Node {
	node := root
	for _, part := range strings.Split(key, ".") {
		node = findMapValue(node, part)
	}
	return node
}

func setConfigNodeAt(root *yaml.Node, key string, value *yaml.Node) error {
	parts := strings.Split(key, ".")
	for _, part := range parts[:len(parts)-1] {
		next := findMapValue(root, part)
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content = append(root.Content, scalarNode(part), next)
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("%s 父节点必须是对象，未写入", key)
		}
		root = next
	}
	leaf := parts[len(parts)-1]
	if old := findMapValue(root, leaf); old != nil {
		value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
		*old = *value
	} else {
		root.Content = append(root.Content, scalarNode(leaf), value)
	}
	return nil
}

func configPatchTarget(root *yaml.Node, args map[string]string) (*yaml.Node, string, error) {
	id := strings.TrimSpace(args["model_id"])
	if id == "" {
		return root, "", nil
	}
	models := findMapValue(root, "models")
	if models == nil || models.Kind != yaml.SequenceNode || len(models.Content) == 0 {
		if id == "primary" {
			return root, "", nil
		}
		return nil, "", fmt.Errorf("未配置 models；当前单模型使用 model_id=primary")
	}
	for i, model := range models.Content {
		generatedID := fmt.Sprintf("model_%d_%s", i+1, valueOr(readScalar(model, "model_name"), readScalar(root, "model_name")))
		if (id == "primary" && (strings.EqualFold(readScalar(model, "role"), "primary") || (i == 0 && readScalar(model, "role") == ""))) || (id != "primary" && valueOr(readScalar(model, "id"), generatedID) == id) {
			return model, "models[" + id + "].", nil
		}
	}
	return nil, "", fmt.Errorf("未找到 model_id=%s；先 status 查看实际主模型与模型 ID", id)
}

func managePatch(root *yaml.Node, args map[string]string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(args["changes"]))
	decoder.UseNumber()
	var changes map[string]any
	if len(args["changes"]) > 64*1024 {
		return "", fmt.Errorf("changes 不能超过 64KiB")
	}
	if err := decoder.Decode(&changes); err != nil || len(changes) == 0 {
		return "", fmt.Errorf("changes 必须是非空 JSON 对象，例如 {\"history_budget_tokens\":24000}")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("changes 只能包含一个 JSON 对象")
	}
	target, prefix, err := configPatchTarget(root, args)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var diff []string
	for _, key := range keys {
		if args["model_id"] == "" && findMapValue(root, "models") != nil {
			switch key {
			case "provider", "api_protocol", "api_url", "api_key", "model_name":
				return "", fmt.Errorf("models 已配置：修改实际模型必须传 model_id=primary 或具体 ID，避免顶层字段被覆盖")
			}
		}
		if prefix != "" && !managedModelKey(key) {
			return "", fmt.Errorf("model_id 仅支持模型字段，%s 请单独修改顶层配置", key)
		}
		node, err := managedValueNode(key, changes[key])
		if err != nil {
			return "", err
		}
		before := configNodeAt(target, key)
		diff = append(diff, fmt.Sprintf("%s%s: %s → %s", prefix, key, safeConfigNode(key, before), safeConfigNode(key, node)))
		if err := setConfigNodeAt(target, key, node); err != nil {
			return "", err
		}
	}
	return strings.Join(diff, "\n"), nil
}

func managedModelKey(key string) bool {
	switch key {
	case "provider", "api_protocol", "api_url", "api_key", "model_name", "temperature", "model_profile", "context_window_tokens", "reserved_output_tokens", "native_tool_limit", "vision_mode", "balance_query.enabled", "balance_query.interval_sec", "balance_query.path", "balance_query.remaining_field", "balance_query.currency_field", "balance_query.unit", "balance_query.scale", "balance_query.headers":
		return true
	}
	return false
}

func safeConfigNode(key string, node *yaml.Node) string {
	if node == nil {
		return "(未配置)"
	}
	if node.Kind == yaml.ScalarNode {
		return redactSensitiveText(redactConfigValue(key, node.Value))
	}
	if strings.HasSuffix(key, "headers") {
		return "***"
	}
	return "(结构化值)"
}

func configDescribe(args map[string]string) (string, error) {
	if key := firstConfigArg(args, "key", "name"); key != "" {
		if key == "ssh" {
			return "SSH 保存值可用 get key=ssh 查看（不暴露凭据）。单目标局部修改 set_ssh，可仅传 port/user/prompt/device_type；同主机未指定参数保留。原始字段为 ssh_host/ssh_user/ssh_password/ssh_key_path/ssh_key_passphrase/ssh_device_type/ssh_prompt/ssh_enable_password。要测试认证用 probe_ssh，不会切换当前连接。", nil
		}
		groups := map[string]reflect.Type{"execution_budget": reflect.TypeOf(ExecutionBudgetConfig{}), "balance_query": reflect.TypeOf(BalanceQueryConfig{}), "search": reflect.TypeOf(SearchConfig{}), "models": reflect.TypeOf(ModelConfig{})}
		if group, ok := groups[key]; ok {
			var fields []string
			for i := 0; i < group.NumField(); i++ {
				f := group.Field(i)
				fields = append(fields, f.Tag.Get("mapstructure")+":"+f.Type.Kind().String())
			}
			return key + " 字段：" + strings.Join(fields, ", ") + "\nexecution_budget/balance_query 用点分键 patch；models 用 model_id=primary 或具体 ID 逐项 set/patch，不覆盖整份 models。", nil
		}
		typ, ok := managedFieldType(key)
		if !ok {
			return "", fmt.Errorf("不支持的配置键 %s", key)
		}
		return fmt.Sprintf("%s: type=%s；set 或 patch 支持此键。模型字段在 models 存在时需 model_id=primary 或具体 ID。dry_run=true 只校验和预览。", key, typ.Kind()), nil
	}
	var keys []string
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		key := field.Tag.Get("mapstructure")
		if allowedScalarConfigKey(key) {
			keys = append(keys, key+":"+field.Type.Kind().String())
		}
	}
	for _, group := range []string{"execution_budget", "balance_query", "search"} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Tag.Get("mapstructure") != group {
				continue
			}
			for j := 0; j < field.Type.NumField(); j++ {
				sub := field.Type.Field(j)
				keys = append(keys, group+"."+sub.Tag.Get("mapstructure")+":"+sub.Type.Kind().String())
			}
		}
	}
	sort.Strings(keys)
	return "可修改配置键（名称:类型）:\n" + strings.Join(keys, ", ") + "\n关联字段用 action=patch, changes={键:值,...} 一次提交并整体验证；set 只改一项。保留其他字段/注释。模型链用 model_id=primary 或具体 ID。MCP/Skill/目标使用专用动作。replace_yaml 仅用于损坏配置修复，不作为常规更新。dry_run=true 不写入、不备份、不重载。", nil
}

func sshConfigSummary(root *yaml.Node) string {
	return fmt.Sprintf("SSH 保存值（不代表当前连接）：ssh_host=%s ssh_user=%s ssh_device_type=%s ssh_prompt=%s ssh_key_path=%s；密码已配置=%t，私钥口令已配置=%t，enable 密码已配置=%t", readScalar(root, "ssh_host"), valueOr(readScalar(root, "ssh_user"), "root"), valueOr(readScalar(root, "ssh_device_type"), "auto"), readScalar(root, "ssh_prompt"), readScalar(root, "ssh_key_path"), readScalar(root, "ssh_password") != "", readScalar(root, "ssh_key_passphrase") != "", readScalar(root, "ssh_enable_password") != "")
}

// Same-directory rename prevents a crash or concurrent reader from seeing a
// truncated YAML file. Refuse a symlink instead of silently replacing its link.
func writeManagedBytes(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("配置路径为符号链接；请使用实际文件路径")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".deepsentry-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func commitManagedConfig(path string, doc *yaml.Node, existed bool, changed string) (string, error) {
	if err := validateConfigNode(doc); err != nil {
		return "", fmt.Errorf("变更后的配置无效，未写入: %w", err)
	}
	data, err := encodeConfigNode(doc)
	if err != nil {
		return "", err
	}
	old, readErr := os.ReadFile(path)
	if existed && readErr != nil {
		return "", readErr
	}
	if existed && bytes.Equal(old, data) {
		return "[OK] 配置未变化，无需重复备份或重载\n" + configEffectReport(doc), nil
	}
	backup := "(新建配置，无旧文件)"
	if existed {
		backup, err = backupConfig(path)
		if err != nil {
			return "", err
		}
	}
	previous := GlobalConfig
	previousPath := viper.ConfigFileUsed()
	if err = writeManagedBytes(path, data); err != nil {
		return "", err
	}
	if err = reloadManagedConfig(path); err != nil {
		var rollback error
		if existed {
			rollback = writeManagedBytes(path, old)
		} else {
			rollback = os.Remove(path)
		}
		GlobalConfig = previous
		viper.SetConfigFile(previousPath)
		if previousPath != "" {
			_ = viper.ReadInConfig()
		}
		if rollback != nil {
			return "", fmt.Errorf("重载失败，自动恢复失败；请从备份 %s 恢复", backup)
		}
		return "", fmt.Errorf("重载失败，已恢复原配置与运行时；检查环境变量或启动覆盖项，备份: %s", backup)
	}
	readback, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(data, readback) {
		return "", fmt.Errorf("写入后回读不一致；请核验外部修改，备份: %s", backup)
	}
	return fmt.Sprintf("[OK] 已更新配置: %s\n[OK] 备份: %s\n[OK] 变更:\n%s\n[OK] 已回读核对；敏感值不回显\n%s%s", path, backup, changed, configEffectReport(doc), managedEnvironmentNotice()), nil
}

func configEffectReport(doc *yaml.Node) string {
	root := ensureConfigRoot(doc)
	msg := "生效说明：模型端点/上下文/代理配置供后续请求读取；配置加载成功不代表接口认证或连通性已通过。MCP 连接、当前 SSH/Telnet/FTP 会话、主题、use_native_tools、max_steps/subagent_max_steps、聊天服务、调度器和运行时初始化设置需重启应用；当前任务步数预算不会中途改变。execution_budget 供后续任务读取。search 配置供后续会话读取，当前 TUI 可用 /search on|off 立即切换。Skill 刷新以当前会话工具结果为准。"
	if models := findMapValue(root, "models"); models != nil && len(models.Content) > 0 {
		msg += "\n注意：models 模型链优先于顶层 provider/api_url/model_name；请用 model_id=primary 修改实际主模型。"
	}
	return msg
}

func managedEnvironmentNotice() string {
	var names []string
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(name, "DEEPSENTRY_") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return "\n环境覆盖：" + strings.Join(names, ", ") + "（仅列名称，不回显值）；文件值可能被环境配置覆盖。"
}

func hasConfigArg(args map[string]string, keys ...string) bool {
	for _, key := range keys {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

func rawConfigArg(args map[string]string, keys ...string) string {
	for _, key := range keys {
		if value, ok := args[key]; ok {
			return value
		}
	}
	return ""
}
func cloneConfigArgs(args map[string]string) map[string]string {
	copy := make(map[string]string, len(args))
	for k, v := range args {
		copy[k] = v
	}
	return copy
}

func ConfigPreview(args map[string]string) bool {
	value := strings.TrimSpace(args["dry_run"])
	return strings.EqualFold(value, "true") || value == "1"
}
func safeManagedURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Internal tool consumers may inspect credentials to test a connection, but
// this snapshot must never be serialized into tool output or model history.
func ManagedConnectionConfig(args map[string]string) (Config, error) {
	manageConfigMu.Lock()
	defer manageConfigMu.Unlock()
	if strings.TrimSpace(args["config_path"]) == "" {
		return GlobalConfig, nil
	}
	doc, exists, err := loadConfigNode(resolveConfigPath(args["config_path"]))
	if err != nil {
		return Config{}, err
	}
	if !exists {
		return Config{}, fmt.Errorf("配置文件不存在")
	}
	return decodeConfigNode(doc)
}

func validateBalanceQuery(q BalanceQueryConfig) error {
	if q.IntervalSec != 0 && (q.IntervalSec < 30 || q.IntervalSec > 3600) {
		return fmt.Errorf("balance_query.interval_sec 必须是 0 或 30-3600")
	}
	if math.IsNaN(q.Scale) || math.IsInf(q.Scale, 0) || q.Scale < 0 {
		return fmt.Errorf("balance_query.scale 必须是有限非负数（0=自动）")
	}
	if q.Path != "" && (!strings.HasPrefix(q.Path, "/") || strings.HasPrefix(q.Path, "//") || strings.Contains(q.Path, "://")) {
		return fmt.Errorf("balance_query.path 必须是同源绝对路径，例如 /user/balance")
	}
	return nil
}

func configuredModelList(doc *yaml.Node) []string {
	cfg, err := decodeConfigNode(doc)
	if err != nil {
		return nil
	}
	var out []string
	for _, model := range cfg.EffectiveModels() {
		out = append(out, fmt.Sprintf("model_id=%s role=%s provider=%s model_name=%s", model.ID, model.Role, model.Provider, model.ModelName))
	}
	return out
}
