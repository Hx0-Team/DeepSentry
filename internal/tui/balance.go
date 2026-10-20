package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"ai-edr/internal/balance"
	"ai-edr/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

type balancePulseMsg time.Time
type balanceResultMsg struct {
	id         string
	generation uint64
	result     balance.Result
}
type balanceView struct {
	id                string
	generation        uint64
	result            balance.Result
	inFlight          bool
	lastAttempt, next time.Time
	failures          int
	cancel            context.CancelFunc
}

func balancePulseCmd(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(t time.Time) tea.Msg { return balancePulseMsg(t) })
}

func configuredBalanceAccount() config.Config {
	c := config.GlobalConfig
	models := c.EffectiveModels()
	if len(models) > 0 {
		c = c.ConfigForModel(models[0])
	}
	return c
}

// Only the UI goroutine owns this state; the query command captures an immutable
// plan and sends back data without credentials. No balance enters chat history.
func (m *AgentModel) refreshBalance(now time.Time, force bool) tea.Cmd {
	p := balance.PlanForConfig(configuredBalanceAccount())
	if p.ID != m.balance.id {
		if m.balance.cancel != nil {
			m.balance.cancel()
		}
		m.balance = balanceView{id: p.ID, generation: m.balance.generation + 1, result: balance.Result{Status: p.Status, Message: p.Message}}
	}
	if p.Status != balance.Pending {
		m.balance.result = balance.Result{Status: p.Status, Message: p.Message}
		return nil
	}
	if m.balance.inFlight || now.Before(m.balance.lastAttempt.Add(10*time.Second)) || (!force && now.Before(m.balance.next)) {
		return nil
	}
	ctx := m.balanceCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	m.balance.cancel = cancel
	m.balance.inFlight = true
	m.balance.lastAttempt = now
	m.balance.generation++
	generation := m.balance.generation
	return func() tea.Msg {
		defer cancel()
		return balanceResultMsg{id: p.ID, generation: generation, result: balance.Query(ctx, p)}
	}
}

func (m *AgentModel) applyBalanceResult(msg balanceResultMsg, now time.Time) tea.Cmd {
	if msg.id != m.balance.id || msg.generation != m.balance.generation {
		return nil
	}
	p := balance.PlanForConfig(configuredBalanceAccount())
	if p.ID != msg.id {
		return m.refreshBalance(now, false)
	}
	m.balance.inFlight = false
	m.balance.cancel = nil
	r := msg.result
	wait := p.Interval
	if r.Status == balance.Ready {
		m.balance.failures = 0
	} else {
		m.balance.failures++
		ceiling := 15 * time.Minute
		if p.Interval > ceiling {
			ceiling = p.Interval
		}
		wait = p.Interval * time.Duration(1<<min(m.balance.failures, 8))
		if wait > ceiling {
			wait = ceiling
		}
		if r.RetryAfter > wait {
			wait = r.RetryAfter
		}
		// Preserve the timestamp of the last successful response on failure.
		if len(m.balance.result.Entries) > 0 {
			r.Entries = m.balance.result.Entries
			r.At = m.balance.result.At
		}
	}
	m.balance.result = r
	m.balance.next = now.Add(wait)
	return nil
}

func balanceAmount(n float64) string {
	if n != 0 && math.Abs(n) < 0.01 {
		return fmt.Sprintf("%.6g", n)
	}
	return fmt.Sprintf("%.2f", n)
}

func (m AgentModel) balanceLabel() string {
	r := m.balance.result
	if r.Status == balance.Hidden || r.Status == "" {
		return ""
	}
	if len(r.Entries) > 0 {
		parts := make([]string, 0, len(r.Entries))
		label := "余额 "
		for _, entry := range r.Entries {
			if entry.Unit == "额度" || entry.Unit == "次" || strings.EqualFold(entry.Unit, "tokens") {
				label = "额度 "
			}
			parts = append(parts, balanceAmount(entry.Amount)+" "+entry.Unit)
		}
		text := label + strings.Join(parts, " / ")
		if r.Status != balance.Ready {
			text += " · 待更新"
		}
		return text
	}
	switch r.Status {
	case balance.Pending:
		return "余额 查询中"
	case balance.Unsupported:
		return "余额 暂不支持"
	case balance.Unauthorized:
		return "余额 无权限"
	default:
		return "余额 查询失败"
	}
}

func (m AgentModel) balanceDetails() string {
	if m.balance.result.Status == balance.Hidden {
		return "本地模型或余额查询已关闭，不查询余额。"
	}
	text := m.balanceLabel()
	if text == "" {
		text = "余额等待查询"
	}
	if !m.balance.result.At.IsZero() {
		text += "\n最后返回: " + m.balance.result.At.Local().Format("2006-01-02 15:04:05")
	}
	if m.balance.result.Message != "" {
		text += "\n" + m.balance.result.Message
	}
	if m.balance.inFlight {
		text += "\n正在后台刷新"
	}
	return text + "\n来自当前配置主模型账户的余额接口；/balance refresh 手动刷新，不调用模型。"
}
