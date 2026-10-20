package builtin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type isolatedReadExecutor struct {
	owner   string
	entered chan<- string
	release <-chan struct{}
}

func (e *isolatedReadExecutor) Run(string) (string, error) { return e.owner, nil }
func (e *isolatedReadExecutor) ReadTargetFile(string) ([]byte, error) {
	e.entered <- e.owner
	<-e.release
	return []byte(e.owner + " evidence line\n"), nil
}
func (e *isolatedReadExecutor) ListTargetDir(string) ([]string, error) { return nil, nil }
func (e *isolatedReadExecutor) IsRemote() bool                         { return true }
func (e *isolatedReadExecutor) Close()                                 {}

func TestBuiltinRunsOverlapWithoutCrossTargetExecutorLeak(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	results := make(chan string, 2)
	for _, owner := range []string{"alpha", "beta"} {
		ex := &isolatedReadExecutor{owner: owner, entered: entered, release: release}
		go func(owner string) {
			out, err := Run("read_log", map[string]string{"path": "/tmp/evidence.log"}, Runtime{Exec: ex, IsRemote: true})
			if err != nil {
				results <- err.Error()
				return
			}
			results <- out
		}(owner)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-timer.C:
			close(release)
			t.Fatal("independent targets still blocked by a global executor lock")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		out := <-results
		if strings.Contains(out, "alpha") == strings.Contains(out, "beta") {
			t.Fatalf("mixed/missing target evidence: %s", out)
		}
	}
}

func TestHTTPToolsAbortInFlightBodyRead(t *testing.T) {
	for _, name := range []string{"http_probe", "http_fetch", "web_snapshot"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"partial":`))
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := Run(name, map[string]string{"url": server.URL, "method": "GET"}, Runtime{Context: ctx})
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request never reached test service")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("partial body accepted after cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("tool ignored caller cancellation")
			}
		})
	}
}
