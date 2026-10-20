package analyzer

import (
	"ai-edr/internal/config"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPStatusOverridesMisleadingBody(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 422, 429, 503} {
		err := fmt.Errorf("wrapped: %w", &APIError{StatusCode: status, Message: "tools stream_options max_tokens unsupported; numbers 429 500 503"})
		if isRetryable(err) != (status == 429 || status == 503) {
			t.Fatalf("status=%d inferred from body", status)
		}
		if (status == 401 || status == 403 || status == 429 || status == 503) && (isToolsUnsupported(err) || isStreamUnsupported(err) || isMaxTokensUnsupported(err)) {
			t.Fatalf("status=%d treated as unsupported request feature", status)
		}
	}
	if isRetryable(errors.Join(&APIError{StatusCode: 503}, context.Canceled)) {
		t.Fatal("cancellation was retried")
	}
}

func TestRetryAfterHeaderParsing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		header http.Header
		want   time.Duration
	}{
		{http.Header{"Retry-After": {"2"}}, 2 * time.Second},
		{http.Header{"Retry-After-Ms": {"50"}, "Retry-After": {"2"}}, 50 * time.Millisecond},
		{http.Header{"Retry-After": {now.Add(7 * time.Second).Format(http.TimeFormat)}}, 7 * time.Second},
		{http.Header{"Retry-After": {"-1"}}, 0},
		{http.Header{"Retry-After": {"NaN"}}, 0},
		{http.Header{"Retry-After": {"Inf"}}, 0},
		{http.Header{"Retry-After": {"1e100"}}, 24 * time.Hour},
	} {
		if got := retryAfterHeaders(tc.header, now); got != tc.want {
			t.Fatalf("%v: got=%s want=%s", tc.header, got, tc.want)
		}
	}
}

func TestQuota429StopsWhileTransient429RemainsRetryable(t *testing.T) {
	for _, code := range []string{"credit_balance_exhausted", "organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded", "insufficient_quota"} {
		err := apiResponseError(429, nil, []byte(`{"error":{"code":"`+code+`","type":"insufficient_quota"}}`))
		if isRetryable(err) || classifyLLMError(err) != "quota_exhausted" || shouldFailover(config.Config{}, err) {
			t.Fatalf("quota was treated as retryable rate limit: %s", code)
		}
	}
	for _, code := range []string{"slow_down", "rate_limit_exceeded"} {
		err := apiResponseError(429, nil, []byte(`{"error":{"code":"`+code+`","type":"rate_limit_error"}}`))
		if !isRetryable(err) || classifyLLMError(err) != "rate_limit" {
			t.Fatal("transient rate limit was suppressed")
		}
	}
}

func TestHTTPAuthenticationDoesNotProbeAlternateFeatures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Request-Id", "test-request")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"tools stream_options max_tokens unsupported; upstream 500/429"}`))
	}))
	defer server.Close()
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	config.GlobalConfig = config.Config{Provider: "custom", ModelName: "fault-auth", ApiURL: server.URL, LLMRetries: 3}
	_, err := CallLLMWithRetryContext(context.Background(), []Message{{Role: "user", Content: "check"}}, true, func(string) {})
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 401 || api.RequestID != "test-request" || requests.Load() != 1 {
		t.Fatalf("authentication caused redundant requests: count=%d err=%v", requests.Load(), err)
	}
}

func TestLongRetryAfterFailsOverWithoutEarlyRetry(t *testing.T) {
	var primary, backup atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/primary/v1/chat/completions" {
			primary.Add(1)
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		backup.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
	}))
	defer server.Close()
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	config.GlobalConfig = config.Config{Provider: "custom", ModelName: "fault-route", Models: []config.ModelConfig{
		{ID: "primary", Role: "primary", Provider: "custom", ModelName: "busy", APIURL: server.URL + "/primary/v1", MaxRetries: 3},
		{ID: "backup", Role: "fallback", Provider: "custom", ModelName: "healthy", APIURL: server.URL + "/backup/v1"},
	}}
	started := time.Now()
	result, err := CallLLMWithRetryContext(context.Background(), []Message{{Role: "user", Content: "check"}}, false, nil)
	if err != nil || result.ModelID != "backup" || primary.Load() != 1 || backup.Load() != 1 || time.Since(started) > time.Second {
		t.Fatalf("long delay not honored via failover: primary=%d backup=%d result=%#v err=%v", primary.Load(), backup.Load(), result, err)
	}
}
