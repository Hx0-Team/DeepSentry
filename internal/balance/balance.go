// Package balance performs account metadata queries, never model inference.
package balance

import (
	"ai-edr/internal/config"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Status string

const (
	Hidden       Status = "hidden"
	Pending      Status = "pending"
	Ready        Status = "ready"
	Unsupported  Status = "unsupported"
	Unavailable  Status = "unavailable"
	Unauthorized Status = "unauthorized"
)

type Entry struct {
	Amount float64 `json:"amount"`
	Unit   string  `json:"unit"`
}
type Result struct {
	Status     Status        `json:"status"`
	Entries    []Entry       `json:"entries,omitempty"`
	Message    string        `json:"message,omitempty"`
	At         time.Time     `json:"at"`
	RetryAfter time.Duration `json:"-"`
}

// Sensitive request settings stay private and are not carried in UI messages.
type Plan struct {
	ID                                                     string
	Status                                                 Status
	Message                                                string
	Interval                                               time.Duration
	kind, endpoint, key, proxy, unit, field, currencyField string
	scale                                                  float64
	headers                                                map[string]string
}

func (p Plan) String() string { return fmt.Sprintf("balance plan %s (%s)", p.ID, p.Status) }

func PlanForConfig(c config.Config) Plan {
	q := c.BalanceQuery
	interval := q.IntervalSec
	if interval == 0 {
		interval = 60
	}
	p := Plan{Status: Pending, Interval: time.Duration(min(3600, max(30, interval))) * time.Second, key: strings.TrimSpace(c.ApiKey), proxy: c.ControllerProxy, headers: maps.Clone(q.Headers), scale: 1}
	identity, _ := json.Marshal([]any{c.Provider, c.ApiURL, c.ApiKey, c.ControllerProxy, q})
	p.ID = fmt.Sprintf("%x", sha256.Sum256(identity))
	if (q.Enabled != nil && !*q.Enabled) || c.IsLocalModelEndpoint() {
		p.Status = Hidden
		return p
	}
	u, err := url.Parse(c.ApiURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		p.Status = Unsupported
		p.Message = "余额查询需要有效的 HTTPS 服务地址"
		return p
	}
	path := ""
	if q.Path != "" {
		ref, e := url.Parse(q.Path)
		if e != nil || !strings.HasPrefix(q.Path, "/") || strings.HasPrefix(q.Path, "//") || ref.Host != "" || ref.Scheme != "" || ref.Fragment != "" || q.RemainingField == "" {
			p.Status = Unsupported
			p.Message = "请配置同源绝对路径和 remaining_field"
			return p
		}
		p.kind = "custom"
		p.field = q.RemainingField
		p.currencyField = q.CurrencyField
		p.unit = q.Unit
		if p.unit == "" {
			p.unit = "额度"
		}
		if q.Scale != 0 {
			p.scale = q.Scale
		}
		if math.IsNaN(p.scale) || math.IsInf(p.scale, 0) || p.scale <= 0 {
			p.Status = Unsupported
			p.Message = "余额换算比例无效"
			return p
		}
		path = q.Path
	} else {
		if u.Port() != "" && u.Port() != "443" {
			p.Status = Unsupported
			p.Message = "余额查询接口未配置"
			return p
		}
		switch strings.ToLower(u.Hostname()) {
		case "api.deepseek.com":
			p.kind = "deepseek"
			path = "/user/balance"
		case "api.stepfun.com", "api.stepfun.ai":
			p.kind = "stepfun"
			path = "/v1/accounts"
			p.unit = "CNY"
		case "api.siliconflow.cn":
			p.kind = "siliconflow"
			path = "/v1/user/info"
			p.unit = "CNY"
		case "api.siliconflow.com":
			p.kind = "siliconflow"
			path = "/v1/user/info"
			p.unit = "USD"
		case "openrouter.ai":
			p.kind = "openrouter"
			path = "/api/v1/credits"
			p.unit = "USD"
		case "api.novita.ai":
			p.kind = "novita"
			path = "/openapi/v1/billing/balance/detail"
			p.unit = "USD"
		default:
			p.Status = Unsupported
			p.Message = "当前服务暂不支持自动余额查询；可配置 balance_query"
			return p
		}
	}
	base := &url.URL{Scheme: u.Scheme, Host: u.Host}
	ref, _ := url.Parse(path)
	destination := base.ResolveReference(ref)
	if destination.Scheme != u.Scheme || destination.Host != u.Host {
		p.Status = Unsupported
		p.Message = "余额查询必须与模型服务同源"
		return p
	}
	p.endpoint = destination.String()
	if p.key == "" || strings.EqualFold(p.key, "none") {
		p.Status = Unavailable
		p.Message = "未配置查询密钥"
	}
	return p
}

func Query(ctx context.Context, p Plan) Result {
	if p.Status != Pending {
		return Result{Status: p.Status, Message: p.Message, At: time.Now()}
	}
	client, err := config.HTTPClientForProxy(p.proxy, 8*time.Second)
	if err != nil {
		return failed(Unavailable, "余额查询代理配置无效")
	}
	defer client.CloseIdleConnections()
	return queryWithClient(ctx, p, client)
}

func failed(status Status, message string) Result {
	return Result{Status: status, Message: message, At: time.Now()}
}

func queryWithClient(ctx context.Context, p Plan, client *http.Client) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint, nil)
	if err != nil {
		return failed(Unavailable, "余额查询地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for name, value := range p.headers {
		req.Header.Set(name, value)
	}
	// Never forward any credential to a redirect target, even a same-host one.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copyClient.Do(req)
	if err != nil {
		return failed(Unavailable, "余额查询网络失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return failed(Unauthorized, "密钥无效或无余额查询权限")
	}
	if response.StatusCode != 200 {
		r := failed(Unavailable, fmt.Sprintf("余额接口 HTTP %d", response.StatusCode))
		if n, e := strconv.ParseFloat(response.Header.Get("Retry-After"), 64); e == nil && n > 0 && !math.IsInf(n, 0) && !math.IsNaN(n) {
			r.RetryAfter = time.Duration(min(n, 86400) * float64(time.Second))
		} else if at, e := http.ParseTime(response.Header.Get("Retry-After")); e == nil {
			r.RetryAfter = min(24*time.Hour, max(time.Duration(0), time.Until(at)))
		}
		return r
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(body) > limit {
		return failed(Unavailable, "余额响应读取失败或过大")
	}
	entries, err := parse(p, body)
	if err != nil {
		return failed(Unavailable, "余额响应缺失或字段无效")
	}
	return Result{Status: Ready, Entries: entries, At: time.Now()}
}

func number(value any) (float64, bool) {
	var n float64
	var err error
	switch v := value.(type) {
	case json.Number:
		n, err = v.Float64()
	case string:
		n, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return 0, false
	}
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= 1e15
}

func field(value any, path string) any {
	for _, part := range strings.Split(path, ".") {
		switch v := value.(type) {
		case map[string]any:
			value = v[part]
		case []any:
			i, e := strconv.Atoi(part)
			if e != nil || i < 0 || i >= len(v) {
				return nil
			}
			value = v[i]
		default:
			return nil
		}
	}
	return value
}

func validUnit(unit string) bool {
	if unit == "额度" || unit == "次" || unit == "元" {
		return true
	}
	if len(unit) < 1 || len(unit) > 12 {
		return false
	}
	for _, r := range unit {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

func parse(p Plan, body []byte) ([]Entry, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	if success, ok := field(value, "success").(bool); ok && !success {
		return nil, fmt.Errorf("unsuccessful response")
	}
	var entries []Entry
	add := func(amount any, unit string, scale float64) bool {
		n, ok := number(amount)
		n *= scale
		if !ok || !validUnit(unit) || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e15 {
			return false
		}
		entries = append(entries, Entry{Amount: n, Unit: unit})
		return true
	}
	ok := false
	switch p.kind {
	case "deepseek":
		infos, _ := field(value, "balance_infos").([]any)
		if len(infos) > 0 && len(infos) <= 4 {
			ok = true
			for _, info := range infos {
				unit, _ := field(info, "currency").(string)
				if unit != "CNY" && unit != "USD" || !add(field(info, "total_balance"), unit, 1) {
					ok = false
					break
				}
			}
		}
	case "stepfun":
		ok = add(field(value, "balance"), p.unit, 1)
	case "siliconflow":
		ok = add(field(value, "data.totalBalance"), p.unit, 1)
	case "novita":
		ok = add(field(value, "availableBalance"), p.unit, 0.0001)
	case "openrouter":
		credits, a := number(field(value, "data.total_credits"))
		used, b := number(field(value, "data.total_usage"))
		if a && b {
			ok = add(json.Number(strconv.FormatFloat(credits-used, 'f', -1, 64)), p.unit, 1)
		}
	case "custom":
		unit := p.unit
		if p.currencyField != "" {
			unit, _ = field(value, p.currencyField).(string)
		}
		ok = add(field(value, p.field), unit, p.scale)
	}
	if !ok {
		return nil, fmt.Errorf("missing balance")
	}
	return entries, nil
}
