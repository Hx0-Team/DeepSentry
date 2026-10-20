// tokenbench exercises the production agent against a local read-only lab.
// Provider billing is recorded by the accompanying HTTP proxy, not estimated.
package main

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type sink struct{ Events []harness.UIEvent }

func (s *sink) Emit(event harness.UIEvent) { s.Events = append(s.Events, event) }

func main() {
	cfg := flag.String("c", "", "private benchmark config")
	root := flag.String("lab", "", "absolute lab directory")
	task := flag.String("task", "", "read-only lab task")
	out := flag.String("out", "", "result JSON")
	export := flag.String("export-provider", "", "write authorized provider settings to a new private JSON file; no model call")
	flag.Parse()
	if *export != "" {
		if err := config.InitConfig(*cfg); err != nil {
			panic(err)
		}
		c := config.GlobalConfig
		if c.EffectiveAPIProtocol() != config.ProtocolOpenAIChat {
			panic("tokenbench proxy requires openai_chat")
		}
		raw, err := json.Marshal(map[string]any{"provider": c.Provider, "model_name": c.ModelName,
			"api_url": config.NormalizeChatURL(c.ApiURL), "api_key": c.ApiKey,
			"controller_proxy": c.ControllerProxy, "model_profile": c.ModelProfile,
			"context_window_tokens": c.ContextWindowTokens, "reserved_output_tokens": c.ReservedOutputTokens,
			"native_tool_limit": c.NativeToolLimit})
		if err != nil {
			panic(err)
		}
		file, err := os.OpenFile(*export, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			panic(err)
		}
		_, err = file.Write(raw)
		closeErr := file.Close()
		if err != nil {
			panic(err)
		}
		if closeErr != nil {
			panic(closeErr)
		}
		fmt.Println("provider settings exported privately")
		return
	}
	if !filepath.IsAbs(*root) || *out == "" || *task == "" {
		panic("lab/out/task required")
	}
	if err := config.InitConfig(*cfg); err != nil {
		panic(err)
	}
	if err := executor.Init(config.GlobalConfig); err != nil {
		panic(err)
	}
	defer executor.Current.Close()
	// Do not send the developer's unrelated personal memories to a test model.
	// Both releases use the identical production stack with these two stores off.
	agent, err := harness.NewDeepAgent(harness.Config{WorkspaceDir: filepath.Join(*root, "workspace"), SkillsDisabled: true, UseNativeTools: true},
		harness.WithMiddleware(harness.NewMemoryMiddleware(nil)),
		harness.WithMiddleware(harness.NewFilesystemMiddleware(nil)))
	if err != nil {
		panic(err)
	}
	history := []analyzer.Message{{Role: "user", Content: *task}}
	ui := &sink{}
	result := agent.RunLoop(harness.RunLoopConfig{SysCtx: collector.GetSystemContext(), History: &history, MaxSteps: 10, BatchMode: true, NonInteractive: true, UI: ui,
		ConfirmFn: func(action *harness.AgentAction) bool { return false }})
	raw, err := json.MarshalIndent(map[string]any{"result": result, "history": history, "events": ui.Events}, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(*out, raw, 0600); err != nil {
		panic(err)
	}
	fmt.Printf("status=%s steps=%d result=%s\n", result.Status, result.Step, *out)
}
