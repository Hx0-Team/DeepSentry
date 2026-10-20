package analyzer

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalHistoryPruneIsIdempotentAndDoesNotSummarize(t *testing.T) {
	history := historyWithPrunableOldToolOutputs(t)
	opts := ContextManageOptions{HistoryBudgetTokens: 48000, PruneBeforeLimit: true,
		Summarize: func(context.Context, []Message) (string, error) {
			t.Fatal("local cleanup invoked summarizer")
			return "", nil
		}}
	changed, err := ManageHistoryContextWithOptions(&history, opts)
	if err != nil || !changed {
		t.Fatalf("first cleanup: changed=%v err=%v", changed, err)
	}
	after := append([]Message(nil), history...)
	changed, err = ManageHistoryContextWithOptions(&history, opts)
	if err != nil || changed || !reflect.DeepEqual(history, after) {
		t.Fatal("identical history was processed again")
	}
}

func TestContextMaintenanceReportsActualSummaryOperations(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	var calls, summaries atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls.Add(1)
		content := `{"action":"finish","final_report":"verified","is_finished":true}`
		if strings.Contains(string(body), "上下文压缩器") {
			summaries.Add(1)
			content = "已核验的历史摘要"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	config.GlobalConfig = config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: server.URL, ModelName: "context-events-test", ContextWindowTokens: 1048576}
	for _, summary := range []bool{false, true} {
		history := historyWithPrunableOldToolOutputs(t)
		budget := 48000
		if summary {
			history = historyThatStillExceedsAfterPrune()
			budget = 18000
		}
		for i := range history {
			history[i].Attachments = nil
		}
		var events []ContextMaintenanceEvent
		calls.Store(0)
		summaries.Store(0)
		_, err := RunAgentStepWithOptions(StepOptions{History: &history, HistoryBudgetTokens: budget, OnContextMaintenance: func(e ContextMaintenanceEvent) { events = append(events, e) }})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("maintenance events=%d", len(events))
		}
		e := events[0]
		if summary {
			if e.Kind != ContextModelSummary || int64(e.SummaryOperations) != summaries.Load() || calls.Load() != summaries.Load()+1 {
				t.Fatalf("summary report=%+v calls=%d summaries=%d", e, calls.Load(), summaries.Load())
			}
		} else if e.Kind != ContextLocalPrune || e.SummaryOperations != 0 || summaries.Load() != 0 || calls.Load() != 1 {
			t.Fatalf("local cleanup caused extra model call: %+v calls=%d", e, calls.Load())
		}
		t.Logf("kind=%s history=%d->%d budget=%d summary_operations=%d HTTP_requests=%d", e.Kind, e.BeforeTokens, e.AfterTokens, e.BudgetTokens, e.SummaryOperations, calls.Load())
	}
}

func TestCancelledStepSummaryPreservesOriginalHistory(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary"}}]}`))
	}))
	defer server.Close()
	config.GlobalConfig = config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: server.URL, ModelName: "cancel-summary-test", ContextWindowTokens: 1048576}
	history := historyThatStillExceedsAfterPrune()
	original := append([]Message(nil), history...)
	_, err := RunAgentStepWithOptions(StepOptions{Context: ctx, History: &history, HistoryBudgetTokens: 18000,
		OnContextMaintenance: func(e ContextMaintenanceEvent) { t.Fatalf("cancellation triggered history rewrite: %+v", e) }})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(history, original) {
		t.Fatalf("cancelled summary changed history: err=%v", err)
	}
}
