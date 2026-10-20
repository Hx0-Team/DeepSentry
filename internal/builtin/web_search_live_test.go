package builtin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestWebSearchLiveDeepSeek(t *testing.T) {
	if os.Getenv("DEEPSENTRY_LIVE_SEARCH") != "1" {
		t.Skip("set DEEPSENTRY_LIVE_SEARCH=1 to call the real (billable) DeepSeek Search API")
	}
	if _, err := os.Stat("../../config.yaml"); err != nil {
		t.Skip("config.yaml is not available in this checkout")
	}
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	if err := config.InitConfig("../../config.yaml"); err != nil {
		t.Fatal(err)
	}
	if !config.GlobalConfig.Search.Configured(config.GlobalConfig) {
		t.Fatal("config.yaml has no usable DeepSeek Search credential")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := WebSearch(Runtime{Context: ctx}, map[string]string{
		"query": "DeepSeek V4.1 Flash 1017 发布 site:api-docs.deepseek.com",
		"count": "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "URL: https://api-docs.deepseek.com") {
		t.Fatalf("live search returned no expected source:\n%s", out)
	}
	t.Logf("live search output head:\n%s", truncateRunes(out, 600))
}
