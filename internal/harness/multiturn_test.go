package harness

import (
	"ai-edr/internal/analyzer"
	"testing"
)

func TestCountUserTurns(t *testing.T) {
	h := []analyzer.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "Output:\ncommand result"},
		{Role: "user", Content: "系统警告: retry"},
		{Role: "user", Content: "【系统】本轮已结束。"},
		{Role: "user", Content: "c"},
	}
	if n := CountUserTurns(h); n != 2 {
		t.Fatalf("expected 2 user turns, got %d", n)
	}
}

func TestCommitFinishDoesNotCreateSyntheticUserTurn(t *testing.T) {
	h := []analyzer.Message{{Role: "user", Content: "真实需求"}}
	CommitFinishToHistory(&h, AgentAction{Type: ActionFinish, FinalReport: "完成"}, "完成")
	if got := CountUserTurns(h); got != 1 {
		t.Fatalf("finish summary must not inflate user turns: got %d history=%#v", got, h)
	}
	if got := h[len(h)-1].Role; got != "system" {
		t.Fatalf("finish summary role=%q, want system", got)
	}
}

func TestMultiTurnExtraPrompt(t *testing.T) {
	h := []analyzer.Message{{Role: "user", Content: "only one"}}
	if p := MultiTurnExtraPrompt(true, &h); p != "" {
		t.Fatal("single turn should not inject follow-up prompt")
	}
	h = append(h, analyzer.Message{Role: "user", Content: "follow up"})
	if p := MultiTurnExtraPrompt(true, &h); p == "" {
		t.Fatal("second user turn should inject follow-up prompt")
	}
}
