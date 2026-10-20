package analyzer

import (
	"ai-edr/internal/mcp"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Historical production selector retained only for the local algorithm A/B.
// It uses the current profile tables and scorer, so catalog/selection semantics
// stay identical. No model or billing is involved in this microbenchmark.
func legacySelectMCPToolsForBenchmark(tools []mcp.ExternalTool, limit int, contextText string, pinned []string) []mcp.ExternalTool {
	if limit <= 0 {
		return tools
	}
	maxMCP := limit / 2
	query := strings.ToLower(contextText)
	if hawkEyeTaskIntent(query) || fofaMapTaskIntent(query) {
		for i := range tools {
			if mcp.IsHawkEyeTool(&tools[i], tools) || mcp.IsFofaMapTool(&tools[i], tools) {
				// HawkEye and FofaMap workflows commonly need a 4-6 tool chain.
				// Give their exact MCP schemas more room while retaining at least
				// two DeepSentry built-ins.
				maxMCP = (limit * 2) / 3
				if maxMCP > limit-2 {
					maxMCP = limit - 2
				}
				break
			}
		}
	}
	if maxMCP < 1 {
		return nil
	}
	pinnedSet := make(map[string]bool, len(pinned))
	for _, name := range pinned {
		pinnedSet[strings.TrimSpace(name)] = true
	}
	type scoredTool struct {
		tool  mcp.ExternalTool
		score int
	}
	scored := make([]scoredTool, 0, len(tools))
	for _, tool := range tools {
		score := 0
		if pinnedSet[tool.Name] {
			score += 10000
		}
		original := strings.ToLower(strings.TrimSpace(tool.OriginalName))
		if original == "" {
			original = strings.ToLower(tool.Name)
		}
		if strings.Contains(query, strings.ToLower(tool.Name)) || strings.Contains(query, original) {
			score += 2000
		}
		if mcp.IsHawkEyeTool(&tool, tools) {
			score += hawkEyeMCPContextScore(original, query)
		} else if mcp.IsFofaMapTool(&tool, tools) {
			score += fofaMapMCPContextScore(original, query)
		} else {
			for _, token := range strings.FieldsFunc(strings.ReplaceAll(original, "_", " "), func(r rune) bool { return r == '-' || r == ' ' }) {
				if len(token) >= 4 && strings.Contains(query, token) {
					score += 40
				}
			}
		}
		if score > 0 {
			scored = append(scored, scoredTool{tool: tool, score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].tool.Name < scored[j].tool.Name
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > maxMCP {
		scored = scored[:maxMCP]
	}
	out := make([]mcp.ExternalTool, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.tool)
	}
	return out
}

func catalogBenchmarkFixture(servers int) []mcp.ExternalTool {
	var out []mcp.ExternalTool
	for i := 0; i < servers; i++ {
		server := fmt.Sprintf("browser-alias-%d", i)
		for _, name := range []string{"browser_snapshot", "browser_navigate", "browser_find", "browser_read_text", "browser_tabs", "hawkeye_capture_start", "hawkeye_capture_stop", "hawkeye_capture_state"} {
			out = append(out, mcp.ExternalTool{Name: server + "__" + name, Server: server, OriginalName: name})
		}
	}
	return out
}

func BenchmarkMCPCatalogSelection(b *testing.B) {
	for _, size := range []int{1, 10, 50} {
		all := catalogBenchmarkFixture(size)
		query := "浏览器打开网页并定位文字 browser snapshot"
		old := legacySelectMCPToolsForBenchmark(all, 16, query, nil)
		current := selectMCPToolsForContext(all, 16, query, nil)
		if !reflect.DeepEqual(old, current) {
			b.Fatal("optimized selection changed tool membership/order")
		}
		for _, legacy := range []bool{true, false} {
			name := fmt.Sprintf("%dtools/Indexed", len(all))
			if legacy {
				name = fmt.Sprintf("%dtools/Legacy", len(all))
			}
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if legacy {
						_ = legacySelectMCPToolsForBenchmark(all, 16, query, nil)
					} else {
						_ = selectMCPToolsForContext(all, 16, query, nil)
					}
				}
			})
		}
	}
}
