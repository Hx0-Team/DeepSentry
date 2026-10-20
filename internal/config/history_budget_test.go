package config

import "testing"

func TestWorkingHistoryBudgetIndependentOfModelCapacity(t *testing.T) {
	c := Config{Provider: "custom", ModelName: "large", ContextWindowTokens: 1_000_000}
	if n := c.RequestHistoryBudgetTokens(1000); n != 48000 {
		t.Fatalf("default=%d", n)
	}
	c.HistoryBudgetTokens = 8000
	if n := c.RequestHistoryBudgetTokens(1000); n != 8000 {
		t.Fatalf("custom=%d", n)
	}
	c.HistoryBudgetTokens = -1
	if n := c.RequestHistoryBudgetTokens(1000); n < 700000 {
		t.Fatalf("capacity opt-in=%d", n)
	}
	c.ContextWindowTokens = 16384
	c.HistoryBudgetTokens = 48000
	if n := c.RequestHistoryBudgetTokens(5000); n >= 16384 {
		t.Fatalf("small context not protected: %d", n)
	}
}
