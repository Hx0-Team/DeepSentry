package config

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

type BalanceQueryConfig struct {
	Enabled        *bool             `mapstructure:"enabled" json:"enabled,omitempty" yaml:"enabled,omitempty"`
	IntervalSec    int               `mapstructure:"interval_sec" json:"interval_sec,omitempty" yaml:"interval_sec,omitempty"`
	Path           string            `mapstructure:"path" json:"path,omitempty" yaml:"path,omitempty"`
	RemainingField string            `mapstructure:"remaining_field" json:"remaining_field,omitempty" yaml:"remaining_field,omitempty"`
	CurrencyField  string            `mapstructure:"currency_field" json:"currency_field,omitempty" yaml:"currency_field,omitempty"`
	Unit           string            `mapstructure:"unit" json:"unit,omitempty" yaml:"unit,omitempty"`
	Scale          float64           `mapstructure:"scale" json:"scale,omitempty" yaml:"scale,omitempty"`
	Headers        map[string]string `mapstructure:"headers" json:"headers,omitempty" yaml:"headers,omitempty"`
}

// Capture the proxy route before asynchronous work. Do not read GlobalConfig
// from the transport's goroutines while the UI may switch configuration.
func HTTPClientForProxy(raw string, timeout time.Duration) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	raw = strings.TrimSpace(raw)
	if raw != "" {
		u, err := ParseControllerProxy(raw)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			tr.Proxy = http.ProxyURL(u)
		case "socks5", "socks5h":
			tr.Proxy = nil
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return ControllerDialContextForProxy(ctx, network, addr, raw)
			}
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}
