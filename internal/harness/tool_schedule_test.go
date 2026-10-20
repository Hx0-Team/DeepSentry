package harness

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolScheduleReadWavesAndWriteBarrier(t *testing.T) {
	var running, peak atomic.Int32
	var mu sync.Mutex
	order := []int{}
	classes := []batchExecutionClass{{parallel: true}, {parallel: true}, {}, {parallel: true}}
	scheduleToolBatch(context.Background(), classes, func(i int) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		if i == 2 && n != 1 {
			t.Error("write overlapped a read")
		}
		if i < 2 {
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		order = append(order, i)
		mu.Unlock()
	}, func(int, error) { t.Error("unexpected cancellation") })
	if peak.Load() != 2 || len(order) != 4 || order[2] != 2 || order[3] != 3 {
		t.Fatalf("incorrect read/write dependencies: peak=%d order=%v", peak.Load(), order)
	}
}

func TestToolScheduleSameResourceOrderAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ran atomic.Int32
	scheduleToolBatch(ctx, []batchExecutionClass{{parallel: true, key: "target"}, {parallel: true, key: "target"}, {parallel: true, key: "target"}}, func(i int) {
		if i != 0 {
			t.Error("queued operation executed after cancellation")
		}
		ran.Add(1)
		cancel()
	}, func(i int, err error) {
		if i == 0 || !errors.Is(err, context.Canceled) {
			t.Errorf("bad cancellation: %d %v", i, err)
		}
	})
	if ran.Load() != 1 {
		t.Fatal("resource ordering or cancellation broken")
	}
}

func TestToolBatchIndependentHTTPReadsReachServerTogether(t *testing.T) {
	var running, peak atomic.Int32
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	agent := &DeepAgent{State: NewAgentState(""), Middleware: []Middleware{NewToolsMiddleware()}}
	calls := []ToolCallAction{}
	for i := 0; i < 3; i++ {
		calls = append(calls, ToolCallAction{ID: fmt.Sprint(i), Name: "http_fetch", Args: map[string]string{"url": fmt.Sprintf("%s/%d", server.URL, i)}})
	}
	done := make(chan error, 1)
	go func() {
		result, err := agent.HandleAction(&StepContext{State: agent.State}, &AgentAction{Type: ActionToolBatch, ToolCalls: calls})
		if err == nil && len(result.ToolResults) != 3 {
			err = fmt.Errorf("missing results")
		}
		done <- err
	}()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-timer.C:
			close(release)
			t.Fatal("HTTP evidence reads were serialized")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 3 {
		t.Fatalf("peak=%d", peak.Load())
	}
}

func BenchmarkHTTPReadBatch(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	agent := &DeepAgent{State: NewAgentState(""), Middleware: []Middleware{NewToolsMiddleware()}}
	ctx := &StepContext{State: agent.State}
	calls := []ToolCallAction{}
	for i := 0; i < 3; i++ {
		calls = append(calls, ToolCallAction{ID: fmt.Sprint(i), Name: "http_fetch", Args: map[string]string{"url": fmt.Sprintf("%s/%d", server.URL, i)}})
	}
	for _, batch := range []bool{false, true} {
		name := "Sequential"
		if batch {
			name = "IndependentReadBatch"
		}
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if batch {
					_, err := agent.HandleAction(ctx, &AgentAction{Type: ActionToolBatch, ToolCalls: calls})
					if err != nil {
						b.Fatal(err)
					}
				} else {
					for _, call := range calls {
						_, err := agent.HandleAction(ctx, &AgentAction{Type: ActionTool, ToolName: call.Name, ToolArgs: call.Args})
						if err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
