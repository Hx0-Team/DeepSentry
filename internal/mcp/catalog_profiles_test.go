package mcp

import "testing"

func TestCatalogProfilesPreserveSameServerTrustAndKnownNames(t *testing.T) {
	all := []ExternalTool{
		{Server: "browser-alias", OriginalName: "hawkeye_capture_start"},
		{Server: "browser-alias", OriginalName: "hawkeye_capture_stop"},
		{Server: "browser-alias", OriginalName: "hawkeye_capture_state"},
		{Server: "browser-alias", OriginalName: "browser_snapshot"},
		{Server: "unrelated", OriginalName: "browser_snapshot"},
		{Server: "browser-alias", OriginalName: "browser_unknown"},
		{Server: "assets-alias", OriginalName: "fofa_account"},
		{Server: "assets-alias", OriginalName: "fofa_search"},
		{Server: "assets-alias", OriginalName: "fofa_fields"},
		{Server: "assets-alias", OriginalName: "nuclei_plan"},
		{Server: "unrelated", OriginalName: "nuclei_plan"},
	}
	p := ProfilesForCatalog(all)
	for i := range all {
		if p.IsHawkEye(&all[i]) != IsHawkEyeTool(&all[i], all) || p.IsFofaMap(&all[i]) != IsFofaMapTool(&all[i], all) {
			t.Fatalf("changed capability ownership: %#v", all[i])
		}
	}
	if p.IsHawkEye(&all[4]) || p.IsHawkEye(&all[5]) || p.IsFofaMap(&all[10]) {
		t.Fatal("trusted unrelated server or unknown tool")
	}
}
