package tools

import (
	"strings"
	"testing"
)

func TestWebSearchFeatureGateHidesToolEverywhere(t *testing.T) {
	ConfigureEnabled(nil, nil)
	t.Cleanup(func() {
		SetWebSearchEnabled(true)
		ConfigureEnabled(nil, nil)
	})

	SetWebSearchEnabled(false)
	if _, ok := Get("web_search"); ok {
		t.Fatal("disabled web_search was still resolvable")
	}
	for _, name := range ListNames() {
		if name == "web_search" {
			t.Fatal("disabled web_search remained in ListNames")
		}
	}
	if strings.Contains(FormatCatalogDetail("all", ""), "web_search") {
		t.Fatal("disabled web_search remained in the catalog")
	}

	SetWebSearchEnabled(true)
	if _, ok := Get("web_search"); !ok {
		t.Fatal("enabled web_search was not resolvable")
	}
	if !strings.Contains(FormatCatalogDetail("联网搜索", ""), "web_search") {
		t.Fatal("enabled web_search was missing from its category")
	}
}
