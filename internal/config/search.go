package config

import (
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultSearchCount keeps the default result set small enough for the
	// model context while still giving the agent several independent sources.
	DefaultSearchCount = 8
	// DefaultSearchTimeoutSec covers a cold DeepSeek Search request without
	// leaving an agent step blocked for the full LLM timeout.
	DefaultSearchTimeoutSec = 20

	deepSeekSearchBaseURL = "https://api.deepseek.com"
)

// SearchConfig configures the DeepSeek Search API. Search is an independent
// controller-side service: enabling it does not change the chat model, and it
// remains optional for users who do not want external queries.
type SearchConfig struct {
	Enabled      bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	BaseURL      string `mapstructure:"base_url" json:"base_url,omitempty" yaml:"base_url,omitempty"`
	APIKey       string `mapstructure:"api_key" json:"api_key,omitempty" yaml:"api_key,omitempty"`
	Count        int    `mapstructure:"count" json:"count,omitempty" yaml:"count,omitempty"`
	NeedContent  bool   `mapstructure:"need_content" json:"need_content,omitempty" yaml:"need_content,omitempty"`
	TimeoutSec   int    `mapstructure:"timeout_sec" json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	Localization string `mapstructure:"localization" json:"localization,omitempty" yaml:"localization,omitempty"`
}

// NormalizeSearchURL converts a base URL or a copied chat endpoint into the
// documented POST /v1/tools/search endpoint without duplicating /v1.
func NormalizeSearchURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/completions"} {
		if strings.HasSuffix(raw, suffix) {
			raw = strings.TrimSuffix(raw, suffix)
			break
		}
	}
	if strings.HasSuffix(raw, "/tools/search") {
		return raw
	}
	if strings.HasSuffix(raw, "/v1") {
		return raw + "/tools/search"
	}
	return raw + "/v1/tools/search"
}

func (s SearchConfig) primaryModel(c Config) ModelConfig {
	models := c.EffectiveModels()
	if len(models) > 0 {
		return models[0]
	}
	return ModelConfig{Provider: c.Provider, APIURL: c.ApiURL, APIKey: c.ApiKey}
}

// EffectiveBaseURL resolves the Search service endpoint. An explicit
// search.base_url always wins. Otherwise only the official DeepSeek host (as
// the primary model endpoint) is reused, so unrelated OpenAI-compatible
// providers never receive search traffic by accident.
func (s SearchConfig) EffectiveBaseURL(c Config) string {
	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		if key := strings.TrimSpace(s.APIKey); key != "" && !strings.EqualFold(key, "none") {
			// An explicit Search key always targets the official DeepSeek
			// service, even when the chat model comes from another provider.
			base = deepSeekSearchBaseURL
		} else {
			primary := s.primaryModel(c)
			if strings.EqualFold(strings.TrimSpace(primary.Provider), string(ProviderDeepSeek)) ||
				deepSeekHost(primary.APIURL) {
				base = deepSeekSearchBaseURL
			} else {
				return ""
			}
		}
	}
	return NormalizeSearchURL(base)
}

// EffectiveAPIKey prefers search.api_key. The primary model key is reused only
// for DeepSeek so a Qwen/OpenAI key is never sent to api.deepseek.com.
func (s SearchConfig) EffectiveAPIKey(c Config) string {
	if key := strings.TrimSpace(s.APIKey); key != "" && !strings.EqualFold(key, "none") {
		return key
	}
	if len(c.Models) > 0 {
		// In a model chain, an omitted per-model key inherits the top-level
		// key. Only inherit it when the top-level provider is also DeepSeek;
		// otherwise a Qwen/OpenAI key could be sent to api.deepseek.com.
		primary, ok := primaryModelEntry(c)
		if !ok {
			return ""
		}
		provider := firstNonEmptyConfig(primary.Provider, c.Provider)
		apiURL := firstNonEmptyConfig(primary.APIURL, c.ApiURL)
		if !strings.EqualFold(strings.TrimSpace(provider), string(ProviderDeepSeek)) && !deepSeekHost(apiURL) {
			return ""
		}
		if key := strings.TrimSpace(primary.APIKey); key != "" && !strings.EqualFold(key, "none") {
			return key
		}
		if strings.EqualFold(strings.TrimSpace(c.Provider), string(ProviderDeepSeek)) || deepSeekHost(c.ApiURL) {
			if key := strings.TrimSpace(c.ApiKey); key != "" && !strings.EqualFold(key, "none") {
				return key
			}
		}
		return ""
	}
	primary := s.primaryModel(c)
	if strings.EqualFold(strings.TrimSpace(primary.Provider), string(ProviderDeepSeek)) ||
		deepSeekHost(primary.APIURL) {
		key := strings.TrimSpace(primary.APIKey)
		if !strings.EqualFold(key, "none") {
			return key
		}
	}
	return ""
}

func primaryModelEntry(c Config) (ModelConfig, bool) {
	if len(c.Models) == 0 {
		return ModelConfig{}, false
	}
	for _, model := range c.Models {
		if strings.EqualFold(strings.TrimSpace(model.Role), "primary") {
			return model, true
		}
	}
	for _, model := range c.Models {
		if strings.TrimSpace(model.Role) == "" {
			return model, true
		}
	}
	return c.Models[0], true
}

func deepSeekHost(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.deepseek.com")
}

// Configured reports whether the Search service has a usable endpoint and
// credential. Enabled is intentionally excluded so callers can explain both
// "switched off" and "switched on but not configured".
func (s SearchConfig) Configured(c Config) bool {
	key := s.EffectiveAPIKey(c)
	endpoint := s.EffectiveBaseURL(c)
	if key == "" || endpoint == "" {
		return false
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// EffectiveEnabled is the single runtime gate used by the tool registry. It
// combines the user's switch with credential/endpoint availability.
func (s SearchConfig) EffectiveEnabled(c Config) bool {
	return s.Enabled && s.Configured(c)
}

func (s SearchConfig) EffectiveCount() int {
	n := s.Count
	if n <= 0 {
		return DefaultSearchCount
	}
	if n > 50 {
		return 50
	}
	return n
}

func (s SearchConfig) EffectiveTimeout() time.Duration {
	sec := s.TimeoutSec
	if sec <= 0 {
		sec = DefaultSearchTimeoutSec
	}
	if sec > 120 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}
