package runtimev3

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type httpFault struct {
	status int
	delay  time.Duration
	quota  bool
}

func (e httpFault) Error() string                  { return "misleading error: 400 429 500 unsupported tools" }
func (e httpFault) HTTPStatusCode() int            { return e.status }
func (e httpFault) RetryAfterDelay() time.Duration { return e.delay }
func (e httpFault) IsQuotaExhausted() bool         { return e.quota }

func TestTypedHTTPFaultRouting(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   ErrorKind
	}{{401, ErrorAuthentication}, {403, ErrorAuthentication}, {429, ErrorRateLimit}, {503, ErrorServer}, {400, ErrorInvalidRequest}} {
		err := fmt.Errorf("wrapped: %w", httpFault{status: tc.status})
		if got := ClassifyError(err); got != tc.kind {
			t.Fatalf("status %d: %s", tc.status, got)
		}
	}
	if ClassifyError(httpFault{status: 429, quota: true}) != ErrorQuotaExhausted {
		t.Fatal("quota retried as rate limit")
	}
	if ClassifyError(&ClassifiedError{Kind: ErrorServer, Err: context.Canceled}) != ErrorCanceled {
		t.Fatal("cancellation hidden by wrapper")
	}
}

func TestRouterHonorsLongServerDelayWithoutRetry(t *testing.T) {
	primary := &fakeModel{err: httpFault{status: 429, delay: 120 * time.Second}}
	backup := &fakeModel{response: ModelResponse{Message: Message{ID: "done"}}}
	router := Router{Endpoints: []Endpoint{{ID: "busy", Client: primary, MaxRetries: 3}, {ID: "healthy", Client: backup}}, Backoff: func(int) time.Duration { return 0 }}
	response, err := router.Generate(context.Background(), ModelRequest{})
	if err != nil || response.ModelID != "healthy" || primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("incorrect routing: primary=%d backup=%d response=%#v err=%v", primary.calls, backup.calls, response, err)
	}
}

func TestRouterNilContextAndEmptyClientsReturnUsefulErrors(t *testing.T) {
	router := Router{Endpoints: []Endpoint{{ID: "missing"}}}
	_, err := router.Generate(context.Background(), ModelRequest{})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("empty model clients must be rejected explicitly")
	}
}
