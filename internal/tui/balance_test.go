package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/balance"
	"ai-edr/internal/config"
)

func balanceTestConfig(t *testing.T) {
	t.Helper()
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig = config.Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com", ApiKey: "test-only-key", ModelName: "deepseek-flash"}
}

func TestBalancePulsePersistsStateAndNeverOverlaps(t *testing.T) {
	balanceTestConfig(t)
	now := time.Now()
	model, cmd := (AgentModel{}).Update(balancePulseMsg(now))
	m := model.(AgentModel)
	if cmd == nil || !m.balance.inFlight || m.balance.id == "" || m.balance.result.Status != balance.Pending {
		t.Fatal("startup query state was lost")
	}
	t.Cleanup(func() { m.balance.cancel() })
	if m.refreshBalance(now.Add(time.Minute), true) != nil {
		t.Fatal("overlapping query")
	}
	m.applyBalanceResult(balanceResultMsg{m.balance.id, m.balance.generation, balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 1.16, Unit: "CNY"}}, At: now}}, now)
	if m.balance.inFlight || m.balance.next != now.Add(time.Minute) || !strings.Contains(m.balanceLabel(), "1.16 CNY") {
		t.Fatal("query result not shown")
	}
	if m.refreshBalance(now.Add(59*time.Second), false) != nil {
		t.Fatal("queried before interval")
	}
	if m.refreshBalance(now.Add(time.Minute), false) == nil {
		t.Fatal("did not refresh automatically")
	}
}

func TestBalanceFailuresKeepLastGoodValueAndBackOff(t *testing.T) {
	balanceTestConfig(t)
	now := time.Now()
	p := balance.PlanForConfig(configuredBalanceAccount())
	m := AgentModel{balance: balanceView{id: p.ID, generation: 1, inFlight: true, result: balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 345.22, Unit: "CNY"}}, At: now.Add(-time.Minute)}}}
	m.applyBalanceResult(balanceResultMsg{p.ID, 1, balance.Result{Status: balance.Unavailable, Message: "network failure", At: now}}, now)
	if m.balance.next != now.Add(2*time.Minute) || !strings.Contains(m.balanceLabel(), "345.22 CNY · 待更新") || m.balance.result.At != now.Add(-time.Minute) {
		t.Fatal("failure erased balance or timestamp")
	}
	for i := 0; i < 12; i++ {
		m.applyBalanceResult(balanceResultMsg{p.ID, 1, balance.Result{Status: balance.Unavailable}}, now)
	}
	if m.balance.next != now.Add(15*time.Minute) {
		t.Fatal("failure retries flood server")
	}
	m.applyBalanceResult(balanceResultMsg{p.ID, 1, balance.Result{Status: balance.Unavailable, RetryAfter: time.Hour}}, now)
	if m.balance.next != now.Add(time.Hour) {
		t.Fatal("ignored retry-after")
	}
	m.applyBalanceResult(balanceResultMsg{p.ID, 1, balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 0, Unit: "CNY"}}, At: now}}, now)
	if m.balance.failures != 0 || strings.Contains(m.balanceLabel(), "待更新") || !strings.Contains(m.balanceLabel(), "0.00") {
		t.Fatal("recovery failed")
	}
}

func TestSwitchingAccountsCancelsAndDiscardsOldBalance(t *testing.T) {
	balanceTestConfig(t)
	now := time.Now()
	m := AgentModel{}
	_ = m.refreshBalance(now, false) // commands are deliberately not executed in UI state tests
	old := balanceResultMsg{m.balance.id, m.balance.generation, balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 100, Unit: "CNY"}}}}
	config.GlobalConfig.ApiKey = "other-test-key"
	if cmd := m.applyBalanceResult(old, now); cmd == nil || m.balance.id == old.id || len(m.balance.result.Entries) != 0 || !m.balance.inFlight {
		t.Fatal("old account response accepted / new query not scheduled")
	}
	t.Cleanup(func() {
		if m.balance.cancel != nil {
			m.balance.cancel()
		}
	})
	if m.applyBalanceResult(old, now) != nil || len(m.balance.result.Entries) != 0 {
		t.Fatal("stale generation resurrected balance")
	}
	config.GlobalConfig.Provider = "lmstudio"
	if cmd := m.refreshBalance(now, false); cmd != nil || m.balance.inFlight || m.balanceLabel() != "" {
		t.Fatal("local model queried")
	}
	if m.applyBalanceResult(old, now) != nil || m.balanceLabel() != "" {
		t.Fatal("old balance appeared on local model")
	}
}

func TestBalanceManualRefreshAndShutdown(t *testing.T) {
	balanceTestConfig(t)
	now := time.Now()
	p := balance.PlanForConfig(configuredBalanceAccount())
	m := AgentModel{balance: balanceView{id: p.ID, lastAttempt: now, next: now.Add(time.Hour), result: balance.Result{Status: balance.Ready}}}
	if m.refreshBalance(now.Add(time.Second), true) != nil {
		t.Fatal("manual refresh has no rate guard")
	}
	if m.refreshBalance(now.Add(11*time.Second), true) == nil {
		t.Fatal("manual refresh did not bypass interval")
	}
	m.balance.cancel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m = AgentModel{balanceCtx: ctx}
	if m.refreshBalance(now, false) != nil {
		t.Fatal("query scheduled after shutdown")
	}
	m.quitting = true
	_, cmd := m.Update(balancePulseMsg(now))
	if cmd != nil {
		t.Fatal("refresh loop persisted after quit")
	}
}

func TestBalanceHeaderWidthsAndHistoryIsolation(t *testing.T) {
	balanceTestConfig(t)
	history := []analyzer.Message{{Role: "user", Content: "test"}}
	ctrl := newSessionController(SessionConfig{History: &history})
	m := NewAgentModel(ctrl, "deepseek / deepseek-flash", "local", 30, true, false, StartupInfo{})
	p := balance.PlanForConfig(configuredBalanceAccount())
	now := time.Now()
	m.balance = balanceView{id: p.ID, next: now.Add(time.Hour), lastAttempt: now, result: balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 1.16, Unit: "CNY"}}, At: now}}
	for _, width := range []int{32, 80, 120, 168} {
		text := m.headerStatsText(width)
		if displayWidth(text) > width || !strings.Contains(text, "1.16 CNY") {
			t.Fatalf("bad header width %d: %s", width, text)
		}
	}
	for _, width := range []int{80, 120, 168} {
		header := stripANSIForTest(m.renderHeader(TerminalRenderWidth(width)))
		for _, line := range strings.Split(header, "\n") {
			if displayWidth(line) >= width {
				t.Fatalf("header row overflowed at %d: %q", width, line)
			}
		}
		if !strings.Contains(header, "1.16 CNY") {
			t.Fatalf("rendered header lost balance or overflowed at %d: %q", width, header)
		}
	}
	m.width = 168
	m.height = 36
	if cmd := m.handleSlashCommand("/balance"); cmd != nil {
		t.Fatal("unnecessary query for fresh balance")
	}
	if len(history) != 1 || m.tokenUsage.TotalTokens != 0 || len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1].content, "1.16 CNY") {
		t.Fatal("balance request leaked into conversation/model usage")
	}
	m.balance.result.Status = balance.Hidden
	if strings.Contains(m.headerStatsText(168), "余额") {
		t.Fatal("local/disabled header shows balance")
	}
}
