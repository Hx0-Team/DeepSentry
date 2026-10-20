package builtin

import (
	"ai-edr/internal/config"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setSearchTestConfig(t *testing.T, baseURL string, needContent bool) {
	t.Helper()
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	config.GlobalConfig = config.Config{
		Provider:  "deepseek",
		ApiURL:    "https://api.deepseek.com",
		ApiKey:    "sk-test",
		ModelName: "deepseek-flash",
		Search: config.SearchConfig{
			Enabled:     true,
			BaseURL:     baseURL,
			Count:       3,
			NeedContent: needContent,
			TimeoutSec:  5,
		},
	}
}

func TestWebSearchCallsDeepSeekSearchAndRendersSources(t *testing.T) {
	var got deepSeekSearchRequest
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"rerank_score":2.4,"title":"DeepSeek V4.1 Flash","url":"https://api-docs.deepseek.com/news/news261017","snippet":"新版发布","summary":"DeepSeek 发布 V4.1 Flash 1017 版本，并开放 Search / Fetch API。","content":"完整正文","site_name":"DeepSeek Docs","site_icon":null,"date":"2026-10-19T05:41:50Z","authority":4}]}`)
	}))
	defer server.Close()
	setSearchTestConfig(t, server.URL, false)

	out, err := WebSearch(Runtime{}, map[string]string{
		"query":        "DeepSeek V4.1 Flash 发布 site:api-docs.deepseek.com",
		"ori_question": "DeepSeek 最新版本有什么变化？",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if got.Query != "DeepSeek V4.1 Flash 发布 site:api-docs.deepseek.com" || got.Count != 3 || got.NeedContent {
		t.Fatalf("unexpected request: %#v", got)
	}
	if got.OriQuestion != "DeepSeek 最新版本有什么变化？" {
		t.Fatalf("ori_question missing: %#v", got)
	}
	for _, want := range []string{"[1] DeepSeek V4.1 Flash", "https://api-docs.deepseek.com/news/news261017", "摘要:", "权威=4", "站点=DeepSeek Docs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "正文:") {
		t.Fatalf("content should not be requested by default:\n%s", out)
	}
}

func TestWebSearchNeedContentCanBeEnabledPerCall(t *testing.T) {
	var got deepSeekSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"results":[{"rerank_score":1.7,"title":"T","url":"https://example.com/a","snippet":"s","summary":"sum","content":"page body","site_name":"Example","site_icon":null,"date":null,"authority":2}]}`)
	}))
	defer server.Close()
	setSearchTestConfig(t, server.URL, false)

	out, err := WebSearch(Runtime{}, map[string]string{"query": "example", "need_content": "true", "count": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedContent || got.Count != 1 {
		t.Fatalf("per-call overrides were ignored: %#v", got)
	}
	if !strings.Contains(out, "正文:") || !strings.Contains(out, "page body") {
		t.Fatalf("content missing:\n%s", out)
	}
}

func TestWebSearchErrorsAreActionable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key","type":"authentication_error","param":null,"code":"invalid_api_key"}}`)
	}))
	defer server.Close()
	setSearchTestConfig(t, server.URL, false)

	_, err := WebSearch(Runtime{}, map[string]string{"query": "example"})
	if err == nil || !strings.Contains(err.Error(), "API Key") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebSearchDisabledAndNoResults(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig = config.Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com", ApiKey: "sk-test", ModelName: "deepseek-flash"}
	if _, err := WebSearch(Runtime{}, map[string]string{"query": "x"}); err == nil || !strings.Contains(err.Error(), "/search on") {
		t.Fatalf("disabled search should explain the switch: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer server.Close()
	setSearchTestConfig(t, server.URL, false)
	out, err := WebSearch(Runtime{}, map[string]string{"query": "nothing"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未返回结果") {
		t.Fatalf("empty result not explained: %s", out)
	}
}

func TestWebSearchRejectsOversizedQuery(t *testing.T) {
	setSearchTestConfig(t, "https://api.deepseek.com", false)
	if _, err := WebSearch(Runtime{}, map[string]string{"query": strings.Repeat("安", 129)}); err == nil {
		t.Fatal("oversized query should be rejected")
	}
}
