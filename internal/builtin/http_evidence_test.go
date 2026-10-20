package builtin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPEvidenceReturnsNonHTMLBodyWithoutBrowser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "HEAD" {
			_, _ = w.Write([]byte(`{"debug":true,"message":"中文接口证据"}`))
		}
	}))
	defer server.Close()
	for _, tool := range []string{"probe", "snapshot"} {
		var output string
		var err error
		if tool == "probe" {
			output, err = HTTPProbe(Runtime{}, server.URL, "GET")
		} else {
			output, err = WebSnapshot(Runtime{}, server.URL, 4096)
		}
		if err != nil || !strings.Contains(output, "200 OK") || !strings.Contains(output, `"debug":true`) || !strings.Contains(output, "中文接口证据") {
			t.Fatalf("%s omitted API evidence: %s (%v)", tool, output, err)
		}
	}
	head, err := HTTPProbe(Runtime{}, server.URL, "HEAD")
	if err != nil || strings.Contains(head, "Body preview") {
		t.Fatalf("HEAD should remain header-only: %s (%v)", head, err)
	}
}

func TestHTTPProbeGETBoundsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("A", 9000) + "UNREAD_TAIL"))
	}))
	defer server.Close()
	output, err := HTTPProbe(Runtime{}, server.URL, "GET")
	if err != nil || !strings.Contains(output, "8192 bytes, truncated=true") || strings.Contains(output, "UNREAD_TAIL") {
		t.Fatalf("preview wasn't bounded: %d bytes (%v)", len(output), err)
	}
}
