package balance

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestOfficialBalanceContractsOverHTTP(t *testing.T) {
	cases := []struct {
		host, path, body string
		entries          []Entry
	}{
		{"api.deepseek.com", "/user/balance", `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"1.16","granted_balance":"0.10"},{"currency":"USD","total_balance":"2.5"}]}`, []Entry{{1.16, "CNY"}, {2.5, "USD"}}},
		{"api.stepfun.com", "/v1/accounts", `{"object":"account","balance":0}`, []Entry{{0, "CNY"}}},
		{"api.siliconflow.cn", "/v1/user/info", `{"code":20000,"data":{"balance":"0.2","totalBalance":"12.50"}}`, []Entry{{12.5, "CNY"}}},
		{"api.siliconflow.com", "/v1/user/info", `{"data":{"totalBalance":"2"}}`, []Entry{{2, "USD"}}},
		{"openrouter.ai", "/api/v1/credits", `{"data":{"total_credits":100,"total_usage":23.5}}`, []Entry{{76.5, "USD"}}},
		{"api.novita.ai", "/openapi/v1/billing/balance/detail", `{"availableBalance":"-50000","cashBalance":"99999"}`, []Entry{{-5, "USD"}}},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			p := PlanForConfig(config.Config{ApiURL: "https://" + tc.host + "/v1/chat/completions", ApiKey: "test-key"})
			if p.Status != Pending || p.endpoint != "https://"+tc.host+tc.path {
				t.Fatalf("wrong plan: %s, endpoint %s", p, p.endpoint)
			}
			var calls atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("wrong metadata request")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			p.endpoint = s.URL + tc.path // only test transport replaces the official origin
			r := queryWithClient(context.Background(), p, s.Client())
			if r.Status != Ready || !reflect.DeepEqual(r.Entries, tc.entries) || calls.Load() != 1 {
				t.Fatalf("wrong result: %+v", r)
			}
		})
	}
}

func TestPlanSkipsLocalAndRejectsUnrelatedOrigins(t *testing.T) {
	disabled := false
	cases := []struct {
		c    config.Config
		want Status
	}{
		{config.Config{Provider: "lmstudio", ApiURL: "https://model.example"}, Hidden},
		{config.Config{Provider: "ollama", ApiURL: "http://10.0.0.5:11434"}, Hidden},
		{config.Config{ApiURL: "http://127.0.0.1:1234/v1"}, Hidden},
		{config.Config{ApiURL: "https://api.deepseek.com", BalanceQuery: config.BalanceQueryConfig{Enabled: &disabled}}, Hidden},
		{config.Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com.attacker.test"}, Unsupported},
		{config.Config{Provider: "deepseek", ApiURL: "https://gateway.example/deepseek"}, Unsupported},
		{config.Config{ApiURL: "https://api.deepseek.com:9443"}, Unsupported},
		{config.Config{ApiURL: "http://api.deepseek.com"}, Unsupported},
		{config.Config{ApiURL: "https://user:password@api.deepseek.com"}, Unsupported},
		{config.Config{ApiURL: "https://api.deepseek.com"}, Unavailable},
	}
	for _, tc := range cases {
		p := PlanForConfig(tc.c)
		if p.Status != tc.want {
			t.Fatalf("unexpected status: %s, want %s", p.Status, tc.want)
		}
		// These plans complete without making a metadata or inference request.
		if r := Query(context.Background(), p); r.Status != tc.want {
			t.Fatal(r.Status)
		}
	}
}

func TestCustomQueriesRemainSameOriginAndRespectScale(t *testing.T) {
	c := config.Config{ApiURL: "https://gateway.example:9443/v1", ApiKey: "test", BalanceQuery: config.BalanceQueryConfig{Path: "/account/balance", RemainingField: "data.0.remaining", CurrencyField: "data.0.currency", Scale: 0.0001, Headers: map[string]string{"X-Account": "one"}}}
	p := PlanForConfig(c)
	if p.Status != Pending || p.endpoint != "https://gateway.example:9443/account/balance" {
		t.Fatal(p)
	}
	c.BalanceQuery.Headers["X-Account"] = "two"
	if p.headers["X-Account"] != "one" {
		t.Fatal("query settings did not snapshot headers")
	}
	entries, err := parse(p, []byte(`{"data":[{"remaining":"10000","currency":"USD"}]}`))
	if err != nil || len(entries) != 1 || entries[0] != (Entry{1, "USD"}) {
		t.Fatal(entries, err)
	}
	for _, path := range []string{"//evil.example/balance", "https://evil.example/balance", "relative", "/balance#fragment"} {
		c.BalanceQuery.Path = path
		if PlanForConfig(c).Status != Unsupported {
			t.Fatal("accepted cross-origin/invalid custom path", path)
		}
	}
}

func TestInvalidBalancesNeverBecomeZero(t *testing.T) {
	cases := []struct{ kind, body string }{
		{"deepseek", `{}`}, {"deepseek", `{"balance_infos":[{"currency":"CNY","total_balance":null}]}`},
		{"deepseek", `{"balance_infos":[{"currency":"CNY","total_balance":"NaN"}]}`},
		{"deepseek", `{"balance_infos":[{"currency":"USD\u001b","total_balance":"2"}]}`},
		{"stepfun", `{"balance":"inf"}`}, {"siliconflow", `{"data":{"balance":123}}`},
		{"novita", `{"cashBalance":10000}`}, {"openrouter", `{"data":{"total_credits":100}}`},
		{"custom", `{"remaining":false}`}, {"custom", `{"remaining":"1e99"}`},
		{"custom", `{"success":false,"remaining":0}`},
		{"custom", `{"remaining":1}{"remaining":2}`}, {"custom", `{"remaining":1} trailing`},
	}
	for _, tc := range cases {
		if got, err := parse(Plan{kind: tc.kind, unit: "CNY", field: "remaining", scale: 1}, []byte(tc.body)); err == nil {
			t.Fatalf("accepted %s: %+v", tc.body, got)
		}
	}
	for _, n := range []string{"0", "-5", "0.001"} {
		got, err := parse(Plan{kind: "custom", unit: "USD", field: "remaining", scale: 1}, []byte(`{"remaining":`+n+`}`))
		if err != nil || len(got) != 1 {
			t.Fatal("zero and debt must be valid balances")
		}
	}
}

func TestQueryRejectsRedirectsAndDoesNotExposeSecrets(t *testing.T) {
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer destination.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer s.Close()
	p := Plan{Status: Pending, kind: "stepfun", endpoint: s.URL, key: "secret-must-stay-private"}
	r := queryWithClient(context.Background(), p, s.Client())
	if r.Status != Unavailable || forwarded.Load() != 0 {
		t.Fatal("forwarded credentials on redirect", r)
	}
	authServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, p.key) }))
	defer authServer.Close()
	p.endpoint = authServer.URL
	r = queryWithClient(context.Background(), p, authServer.Client())
	if r.Status != Unauthorized || strings.Contains(fmt.Sprint(r), p.key) {
		t.Fatal("error exposed response body or secret")
	}
}

func TestQueryCancellationRateLimitAndResponseBounds(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	p := Plan{Status: Pending, kind: "stepfun", endpoint: s.URL, key: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if r := queryWithClient(ctx, p, s.Client()); r.Status != Unavailable || time.Since(start) > time.Second {
		t.Fatal("uncancellable balance query")
	}
	limited := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
	defer limited.Close()
	p.endpoint = limited.URL
	if r := queryWithClient(context.Background(), p, limited.Client()); r.RetryAfter != 2*time.Minute {
		t.Fatal("rate limit ignored", r)
	}
	dated := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(3*time.Minute).UTC().Format(http.TimeFormat))
		w.WriteHeader(429)
	}))
	defer dated.Close()
	p.endpoint = dated.URL
	if r := queryWithClient(context.Background(), p, dated.Client()); r.RetryAfter < 179*time.Second || r.RetryAfter > 3*time.Minute {
		t.Fatal("HTTP date retry-after ignored", r)
	}
	oversized := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", (1<<20)+1)) }))
	defer oversized.Close()
	p.endpoint = oversized.URL
	if r := queryWithClient(context.Background(), p, oversized.Client()); r.Status != Unavailable {
		t.Fatal("unbounded response", r.Status)
	}
	p.proxy = "not-a-valid-proxy"
	if r := Query(context.Background(), p); r.Status != Unavailable || !strings.Contains(r.Message, "代理") {
		t.Fatal("bad proxy failed open")
	}
}

func TestAccountIdentityAndIntervals(t *testing.T) {
	c := config.Config{ApiURL: "https://api.deepseek.com", ApiKey: "one"}
	one := PlanForConfig(c)
	c.ApiKey = "two"
	two := PlanForConfig(c)
	if one.ID == two.ID || one.Interval != time.Minute || strings.Contains(fmt.Sprint(one), "one") {
		t.Fatal("account identity is unsafe")
	}
	c.BalanceQuery.IntervalSec = 1
	if PlanForConfig(c).Interval != 30*time.Second {
		t.Fatal("refresh flooding")
	}
	c.BalanceQuery.IntervalSec = 99999
	if PlanForConfig(c).Interval != time.Hour {
		t.Fatal("refresh overflow")
	}
	c.BalanceQuery.Path = "/balance"
	c.BalanceQuery.RemainingField = "balance"
	c.BalanceQuery.Scale = math.Inf(1)
	if PlanForConfig(c).Status != Unsupported {
		t.Fatal("invalid scale")
	}
}
