package analyzer

import (
	"ai-edr/internal/config"
	"ai-edr/internal/mcp"
	"ai-edr/internal/tools"
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// Scope reuse to a step/request and its retries. No cross-user response cache,
// no stale tool permissions after hot reload, no unbounded global cache.
type requestToolsCache struct {
	mu      sync.Mutex
	entries map[requestToolsKey]preparedNativeTools
	bytes   int
}
type requestToolsContextKey struct{}
type requestToolsKey struct {
	query                        [32]byte
	pinned                       string
	limit                        int
	builtinRevision, mcpRevision uint64
}
type preparedNativeTools struct {
	encoded       json.RawMessage // owned, read-only snapshot
	count, tokens int
}

func withRequestToolsCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(requestToolsContextKey{}).(*requestToolsCache); ok {
		return ctx
	}
	return context.WithValue(ctx, requestToolsContextKey{}, &requestToolsCache{})
}

func nativeToolSelection(cfg config.Config, messages []Message) (int, string, []string) {
	limit := cfg.EffectiveModelCapabilities().NativeToolLimit
	if limit <= 0 {
		limit = runtimeV3DeferredToolLimit
	}
	return limit, recentToolSelectionContext(messages, 12000), pinnedNativeToolNames(messages)
}

func canonicalNativeTools(definitions []ToolDefinition) []ToolDefinition {
	// Keep the three control functions in their fixed order; ranking chooses
	// membership, not wire order, so score changes do not churn the same set.
	if len(definitions) > alwaysVisibleNativeSchemaCount {
		tail := definitions[alwaysVisibleNativeSchemaCount:]
		slices.SortFunc(tail, func(a, b ToolDefinition) int { return strings.Compare(a.Function.Name, b.Function.Name) })
	}
	return definitions
}

func prepareNativeTools(ctx context.Context, cfg config.Config, messages []Message) (preparedNativeTools, error) {
	limit, query, pinned := nativeToolSelection(cfg, messages)
	key := requestToolsKey{query: sha256.Sum256([]byte(query)), pinned: strings.Join(pinned, "\x00"), limit: limit, builtinRevision: tools.Revision(), mcpRevision: mcp.Global().Revision()}
	var cache *requestToolsCache
	if ctx != nil {
		cache, _ = ctx.Value(requestToolsContextKey{}).(*requestToolsCache)
	}
	if cache != nil {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if value, ok := cache.entries[key]; ok {
			return value, nil
		}
	}
	definitions := canonicalNativeTools(AgentToolDefinitionsForContextWithPinned(limit, query, pinned))
	encoded, err := json.Marshal(definitions)
	if err != nil {
		return preparedNativeTools{}, err
	}
	prepared := preparedNativeTools{encoded: encoded, count: len(definitions), tokens: EstimateTextTokens(string(encoded))}
	if cache != nil && key.builtinRevision == tools.Revision() && key.mcpRevision == mcp.Global().Revision() && len(cache.entries) < 8 && cache.bytes+len(encoded) <= 1<<20 {
		if cache.entries == nil {
			cache.entries = make(map[requestToolsKey]preparedNativeTools)
		}
		cache.entries[key] = prepared
		cache.bytes += len(encoded)
	}
	return prepared, nil
}
