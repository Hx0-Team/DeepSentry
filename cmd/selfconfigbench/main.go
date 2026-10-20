// selfconfigbench uses the production harness and an explicitly authorized
// provider against disposable config files. It never edits the source config.
package main

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness"
	"ai-edr/internal/tools"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type usageSink struct {
	Report                                  string
	Prompt, Completion, Total, Cache, Calls int
	Actions                                 []map[string]string
}

func (s *usageSink) Emit(e harness.UIEvent) {
	if e.Kind == harness.EventFinish {
		s.Report = e.Message
	}
	if e.Kind == harness.EventTokenUsage {
		s.Prompt += e.PromptTokens
		s.Completion += e.CompletionTokens
		s.Total += e.TotalTokens
		s.Cache += e.CacheReadTokens
	}
}

type scenario struct {
	name, prompt string
	chain        bool
	check        func(config.Config) bool
}

func main() {
	source := flag.String("source", "", "authorized provider config, read only")
	output := flag.String("out", "", "public result JSON, must not exist")
	live := flag.Bool("live", false, "explicitly allow real provider requests and billing")
	only := flag.String("only", "", "comma-separated scenario names; empty runs all")
	flag.Parse()
	if !*live || *source == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "requires --live --source <authorized config> --out <new JSON>")
		os.Exit(2)
	}
	if err := run(*source, *output, *only); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(source, output, only string) error {
	if _, err := os.Stat(output); err == nil {
		return errors.New("output already exists; choose a new file before making paid requests")
	} else if !os.IsNotExist(err) {
		return err
	}
	sourceData, err := os.ReadFile(source)
	if err != nil {
		return errors.New("cannot read authorized source config")
	}
	if err = config.InitConfig(source); err != nil {
		return errors.New("authorized source config is invalid")
	}
	base := config.GlobalConfig.ConfigForModel(config.GlobalConfig.EffectiveModels()[0])
	if base.ApiKey == "" {
		return errors.New("authorized source has no credential")
	}
	lab, err := os.MkdirTemp("", "deepsentry-selfconfig-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(lab)
	// Keep personal memory, sessions, AGENTS.md and Skills outside this test.
	previousHome := os.Getenv("HOME")
	defer os.Setenv("HOME", previousHome)
	if err = os.Setenv("HOME", lab); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(lab, "empty-skills"), 0700); err != nil {
		return err
	}
	var tunnelCount atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		upstream, err := config.ControllerDialContextForProxy(ctx, "tcp", r.Host, base.ControllerProxy)
		if err != nil {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		tunnelCount.Add(1)
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		go func() { defer client.Close(); defer upstream.Close(); _, _ = io.Copy(upstream, buffer) }()
		go func() { defer client.Close(); defer upstream.Close(); _, _ = io.Copy(client, upstream) }()
	}))
	defer proxy.Close()
	cases := []scenario{
		{"history-budget", "把你自己的主 Agent 历史预算改为24000 tokens，子 Agent历史预算改为8000 tokens。只修改这两项，核对后报告完成。不要读完整配置，不需要联网诊断。", false, func(c config.Config) bool {
			return c.HistoryBudgetTokens == 24000 && c.SubAgentHistoryBudgetTokens == 8000
		}},
		{"primary-model", "修改你自己的实际主模型温度为0.2。当前使用 models 模型链，顶层 temperature 和备用模型温度都必须保持原值。只改这一项，核对配置值后报告，不需要额外接口探活。", true, func(c config.Config) bool {
			return c.Temperature == 0 && len(c.Models) == 2 && c.Models[0].Temperature == 0.2 && c.Models[1].Temperature == 0.6
		}},
		{"nested-balance-budget", "调整你自身配置：余额查询关闭并将刷新间隔设为90秒；execution_budget.max_steps设为120、subagent_max_steps设为80、total_steps设为300。一次更新关联项，核对保存值，如实说明当前任务预算是否中途变更，不需要查询余额。", false, func(c config.Config) bool {
			return c.BalanceQuery.Enabled != nil && !*c.BalanceQuery.Enabled && c.BalanceQuery.IntervalSec == 90 && c.ExecutionBudget.MaxSteps == 120 && c.ExecutionBudget.SubagentMaxSteps == 80 && c.ExecutionBudget.TotalSteps == 300
		}},
		{"add-mcp", "给你添加一个仅作配置验收的 stdio MCP，名字 lab_diag，command=/usr/bin/true，不带参数。不要求现在连接，请保存后核对配置并说明何时能连接。不要运行该命令或另开进程。", false, func(c config.Config) bool {
			return len(c.MCPServerConfigs) == 1 && c.MCPServerConfigs[0].Name == "lab_diag" && c.MCPServerConfigs[0].Command == "/usr/bin/true"
		}},
		{"ssh-port", "把你自己配置的单目标 SSH 端口改为2222，原主机、用户名、密码、设备类型与提示符全部保持不变。只保存配置并核对，不要连接远程，不要切换当前执行器。", false, func(c config.Config) bool {
			return c.SSHHost == "192.0.2.5:2222" && c.SSHUser == "ops" && c.SSHPassword == "lab-ssh-fixture-secret" && c.SSHDeviceType == "huawei" && c.SSHPrompt == "<lab-device>"
		}},
		{"controller-proxy", "把你自己的控制端出站代理设为 " + proxy.URL + "。先测试这个候选代理到当前模型接口的路由，再保存并核对。不要修改 SSH 目标，不要启动另一套代理服务。HTTP回应不代表模型认证或额度已经验证。", false, func(c config.Config) bool { return c.ControllerProxy == proxy.URL }},
	}
	var runs []map[string]any
	allPass := true
	for i, sc := range cases {
		if only != "" && !strings.Contains(","+only+",", ","+sc.name+",") {
			continue
		}
		dir := filepath.Join(lab, fmt.Sprintf("case-%d", i+1))
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		path := filepath.Join(dir, "config.yaml")
		fixture := map[string]any{"provider": base.Provider, "api_protocol": base.APIProtocol, "api_url": base.ApiURL, "api_key": base.ApiKey, "model_name": base.ModelName, "controller_proxy": base.ControllerProxy,
			"temperature": 0, "context_window_tokens": base.ContextWindowTokens, "reserved_output_tokens": base.ReservedOutputTokens, "model_profile": base.ModelProfile, "agent_runtime": "v3", "trace_enabled": false, "target_protocol": "local", "use_native_tools": true,
			"skills_disabled": true, "skill_sources": []string{filepath.Join(lab, "empty-skills")}, "history_budget_tokens": 48000, "subagent_history_budget_tokens": 16000, "balance_query": map[string]any{"enabled": true, "interval_sec": 60}, "terminal_theme": "dark", "max_steps": 10}
		if sc.chain {
			fixture["models"] = []map[string]any{{"id": "main", "role": "primary", "temperature": 0}, {"id": "spare", "role": "fallback", "temperature": 0.6}}
		}
		if sc.name == "ssh-port" {
			fixture["ssh_host"] = "192.0.2.5:22"
			fixture["ssh_user"] = "ops"
			fixture["ssh_password"] = "lab-ssh-fixture-secret"
			fixture["ssh_device_type"] = "huawei"
			fixture["ssh_prompt"] = "<lab-device>"
		}
		raw, err := yaml.Marshal(fixture)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
		viper.Reset()
		if err = config.InitConfig(path); err != nil {
			return errors.New("lab config initialization failed")
		}
		if err = executor.Init(config.GlobalConfig); err != nil {
			return errors.New("lab executor initialization failed")
		}
		agent, err := harness.NewDeepAgent(harness.Config{WorkspaceDir: filepath.Join(dir, "workspace"), SkillsDisabled: true, UseNativeTools: true}, harness.WithMiddleware(harness.NewMemoryMiddleware(nil)), harness.WithMiddleware(harness.NewFilesystemMiddleware(nil)))
		if err != nil {
			return errors.New("lab agent initialization failed")
		}
		history := []analyzer.Message{{Role: "user", Content: sc.prompt}}
		sink := &usageSink{}
		var allowed func(*harness.AgentAction) bool
		allowed = func(a *harness.AgentAction) bool {
			if a.Type == harness.ActionToolBatch {
				for _, call := range a.ToolCalls {
					one := &harness.AgentAction{Type: harness.ActionTool, ToolName: call.Name, ToolArgs: call.Args}
					if !allowed(one) {
						return false
					}
				}
				return len(a.ToolCalls) > 0
			}
			if a.Type != harness.ActionTool || (a.ToolName != "config_manage" && a.ToolName != "tool_catalog") {
				return false
			}
			p := a.ToolArgs["config_path"]
			if p != "" && p != path {
				return false
			}
			if a.ToolArgs["action"] == "probe_ssh" || a.ToolArgs["action"] == "add_target" || a.ToolArgs["action"] == "replace_yaml" {
				return false
			}
			return a.ToolArgs["action"] != "set_ssh" || sc.name == "ssh-port"
		}
		start := time.Now()
		result := agent.RunLoop(harness.RunLoopConfig{SysCtx: collector.SystemContext{OS: "Darwin", Arch: "arm64", Hostname: "self-config-lab", Username: "lab", Shell: "/bin/zsh"}, History: &history, MaxSteps: 10, NonInteractive: true, UI: sink,
			ConfirmFn: allowed,
			ModelStep: func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
				sink.Calls++
				return analyzer.RunAgentStepWithOptions(opts)
			},
			ActionHandler: func(ctx *harness.StepContext, a *harness.AgentAction) (*harness.ActionResult, error) {
				if !allowed(a) {
					return &harness.ActionResult{Output: "lab guard: only config_manage and tool_catalog on the disposable config are allowed"}, nil
				}
				calls := []harness.ToolCallAction{{Name: a.ToolName, Args: a.ToolArgs}}
				if a.Type == harness.ActionToolBatch {
					calls = a.ToolCalls
				}
				for _, call := range calls {
					args := map[string]string{"tool": call.Name}
					for _, k := range []string{"action", "key", "model_id", "dry_run"} {
						if v := call.Args[k]; v != "" {
							args[k] = v
						}
					}
					sink.Actions = append(sink.Actions, args)
					if err := tools.ValidateCall(call.Name, call.Args); err != nil {
						return nil, err
					}
				}

				return agent.HandleAction(ctx, a)
			}})
		executor.Current.Close()
		afterData, _ := os.ReadFile(path)
		file := viper.New()
		file.SetConfigType("yaml")
		_ = file.ReadConfig(bytes.NewReader(afterData))
		var saved config.Config
		_ = file.Unmarshal(&saved)
		preserved := saved.ApiKey == base.ApiKey && saved.ApiURL == base.ApiURL && saved.ModelName == base.ModelName && saved.TerminalTheme == "dark" && (saved.ControllerProxy == base.ControllerProxy || sc.name == "controller-proxy")
		passed := result.Successful() && sc.check(saved) && preserved
		if sc.name == "controller-proxy" && tunnelCount.Load() < 2 {
			passed = false
		}
		allPass = allPass && passed
		report := sink.Report
		report = strings.ReplaceAll(report, lab, "LAB")
		report = strings.ReplaceAll(report, base.ApiKey, "***")
		report = strings.ReplaceAll(report, proxy.URL, "LAB_PROXY")
		runs = append(runs, map[string]any{"scenario": sc.name, "passed": passed, "requested_values_saved": sc.check(saved), "unrelated_config_preserved": preserved, "status": result.Status, "reason": result.Reason, "model_calls": sink.Calls, "prompt_tokens": sink.Prompt, "completion_tokens": sink.Completion, "total_tokens": sink.Total, "cache_read_tokens": sink.Cache, "seconds": time.Since(start).Seconds(), "actions": sink.Actions, "final_response": report})
		fmt.Printf("%s: passed=%t calls=%d provider_tokens=%d\n", sc.name, passed, sink.Calls, sink.Total)
	}
	if len(runs) == 0 {
		return errors.New("no selected scenarios")
	}
	afterSource, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(sourceData, afterSource) {
		return errors.New("source config changed during benchmark")
	}
	data, err := json.MarshalIndent(map[string]any{"version": "2.0.6", "timestamp": time.Now().UTC().Format(time.RFC3339), "real_provider": base.Provider, "real_model": base.ModelName, "simulated_model": false, "production_harness": true, "source_config_unchanged": true, "disposable_configs_deleted_on_exit": true, "all_pass": allPass, "runs": runs}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !allPass {
		return errors.New("some live scenarios failed; inspect the public result")
	}
	return nil
}
