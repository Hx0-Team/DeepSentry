package analyzer

import (
	"ai-edr/internal/security"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIError keeps transport facts separate from untrusted provider text.
// Error classifiers and routers must never infer HTTP status from the body.
type APIError struct {
	StatusCode int
	Message    string
	RequestID  string
	RetryAfter time.Duration
	Code       string
	Type       string
}

func (e *APIError) Error() string                  { return fmt.Sprintf("API Error %d: %s", e.StatusCode, e.Message) }
func (e *APIError) HTTPStatusCode() int            { return e.StatusCode }
func (e *APIError) RetryAfterDelay() time.Duration { return e.RetryAfter }

func (e *APIError) IsQuotaExhausted() bool {
	if e.StatusCode == 402 {
		return true
	}
	if e.StatusCode != 429 {
		return false
	}
	switch e.Code {
	case "credit_balance_exhausted", "organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded", "insufficient_quota":
		return true
	case "slow_down", "rate_limit_exceeded":
		return false
	}
	return e.Type == "insufficient_quota"
}

func apiResponseError(status int, headers http.Header, body []byte) *APIError {
	var detail struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &detail)
	requestID := headers.Get("X-Request-Id")
	if requestID == "" {
		requestID = headers.Get("Request-Id")
	}
	return &APIError{StatusCode: status, Code: security.RedactSensitiveText(truncateStr(detail.Error.Code, 128)), Type: security.RedactSensitiveText(truncateStr(detail.Error.Type, 128)),
		Message:   security.RedactSensitiveText(truncateStr(string(body), 500)),
		RequestID: security.RedactSensitiveText(truncateStr(requestID, 128)), RetryAfter: retryAfterHeaders(headers, time.Now())}
}

func retryAfterHeaders(headers http.Header, now time.Time) time.Duration {
	for _, item := range []struct {
		name string
		unit time.Duration
	}{{"Retry-After-Ms", time.Millisecond}, {"Retry-After", time.Second}} {
		value := strings.TrimSpace(headers.Get(item.name))
		if value == "" {
			continue
		}
		if n, err := strconv.ParseFloat(value, 64); err == nil && n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) {
			// Bound conversion, not the provider's minimum delay. Long hints
			// make the router fail over or stop rather than retry too early.
			return time.Duration(min(n*float64(item.unit), float64(24*time.Hour)))
		}
		if item.name == "Retry-After" {
			if until, err := http.ParseTime(value); err == nil && until.After(now) {
				return min(until.Sub(now), 24*time.Hour)
			}
		}
	}
	return 0
}

const maxLLMRetryWait = 30 * time.Second

func retryDelayForError(attempt int, err error) time.Duration {
	delay := min(llmRetryDelay(attempt), maxLLMRetryWait)
	var hint interface{ RetryAfterDelay() time.Duration }
	if errors.As(err, &hint) {
		delay = max(delay, hint.RetryAfterDelay())
	}
	return delay
}
