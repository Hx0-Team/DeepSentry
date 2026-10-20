package tools

import "sync/atomic"

// FeatureWebSearch marks tools backed by the optional DeepSeek Search service.
const FeatureWebSearch = "web_search"

var webSearchAvailable atomic.Bool

func init() {
	webSearchAvailable.Store(true)
}

// SetWebSearchEnabled is the runtime switch used by TUI /search and by the
// startup configuration. Disabled tools disappear from the catalog and cannot
// be invoked through either native schemas or agent_action compatibility calls.
func SetWebSearchEnabled(enabled bool) {
	if webSearchAvailable.Swap(enabled) == enabled {
		return
	}
	registryMu.Lock()
	registryRevision++
	registryMu.Unlock()
}

func WebSearchEnabled() bool {
	return webSearchAvailable.Load()
}

func toolFeatureAvailable(t *Tool) bool {
	if t == nil {
		return false
	}
	if t.Feature == FeatureWebSearch {
		return webSearchAvailable.Load()
	}
	return true
}
