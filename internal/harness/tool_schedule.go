package harness

import (
	"ai-edr/internal/executor"
	"ai-edr/internal/mcp"
	"ai-edr/internal/tools"
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"sync"
)

type batchExecutionClass struct {
	parallel bool
	key      string
}

func classifyBatchExecution(ctx *StepContext, call ToolCallAction) batchExecutionClass {
	call.Args = maps.Clone(call.Args)
	risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: call.Name, ToolArgs: call.Args})
	if tool, known := tools.Get(call.Name); known {
		// Approval risk remains medium. Only GET/HEAD evidence reads may
		// overlap; identical URLs keep model order. This is not authorization.
		if (call.Name == "http_fetch" || call.Name == "http_probe") && risk != tools.RiskHigh {
			method := strings.ToUpper(strings.TrimSpace(call.Args["method"]))
			if method != "" && method != "GET" && method != "HEAD" {
				return batchExecutionClass{}
			}
			raw := strings.TrimSpace(call.Args["url"])
			if !strings.Contains(raw, "://") {
				raw = "http://" + raw
			}
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" {
				return batchExecutionClass{}
			}
			u.Scheme = strings.ToLower(u.Scheme)
			u.Host = strings.ToLower(u.Host)
			u.Fragment = ""
			if u.Path == "" {
				u.Path = "/"
			}
			return batchExecutionClass{parallel: true, key: fmt.Sprintf("http:%x", sha256.Sum256([]byte(u.String())))}
		}
		if risk != tools.RiskLow {
			return batchExecutionClass{}
		}
		if call.Name == "browser_browse" {
			switch call.Args["action"] {
			case "status", "list", "snapshot", "screenshot":
				return batchExecutionClass{parallel: true, key: "browser:" + call.Args["session_id"]}
			default:
				return batchExecutionClass{}
			}
		}
		if call.Name == "computer_use" {
			return batchExecutionClass{parallel: true, key: "desktop"}
		}
		ex := ctx.Executor
		if ex == nil {
			ex = executor.Current
		}
		if tool.Perspective == tools.PerspectiveTarget && ex != nil && ex.IsRemote() {
			return batchExecutionClass{parallel: true, key: "target-session"}
		}
		return batchExecutionClass{parallel: true}
	}
	if risk == tools.RiskLow {
		if tool, _, ok := mcp.Global().Get(strings.TrimPrefix(call.Name, "mcp:")); ok {
			// A known server may share one browser tab or capture session.
			return batchExecutionClass{parallel: true, key: "mcp:" + tool.Server}
		}
		if call.Name == "tool_catalog" {
			return batchExecutionClass{parallel: true}
		}
	}
	return batchExecutionClass{}
}

// Preserve barriers around every non-read operation. Within a read wave,
// resource keys serialize conflicts, while independent reads overlap.
func scheduleToolBatch(ctx context.Context, classes []batchExecutionClass, run func(int), canceled func(int, error)) {
	sem := make(chan struct{}, 4)
	tails := map[string]<-chan struct{}{}
	var wg sync.WaitGroup
	for i, class := range classes {
		if !class.parallel {
			wg.Wait()
			if err := ctx.Err(); err != nil {
				canceled(i, err)
			} else {
				run(i)
			}
			continue
		}
		previous := tails[class.key]
		var done chan struct{}
		if class.key != "" {
			done = make(chan struct{})
			tails[class.key] = done
		}
		wg.Add(1)
		go func(index int, prev <-chan struct{}, done chan struct{}) {
			defer wg.Done()
			if done != nil {
				defer close(done)
			}
			if prev != nil {
				select {
				case <-prev:
				case <-ctx.Done():
					canceled(index, ctx.Err())
					return
				}
			}
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				canceled(index, ctx.Err())
				return
			}
			if err := ctx.Err(); err != nil {
				canceled(index, err)
				return
			}
			run(index)
		}(i, previous, done)
	}
	wg.Wait()
}
