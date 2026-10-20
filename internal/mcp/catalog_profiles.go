package mcp

import "strings"

// CatalogProfiles evaluates same-server capability signatures once per
// immutable catalog snapshot, rather than once for every candidate tool.
type CatalogProfiles struct {
	hawkEye map[string]bool
	fofaMap map[string]bool
}

func ProfilesForCatalog(all []ExternalTool) CatalogProfiles {
	if len(all) == 0 {
		return CatalogProfiles{}
	}
	pointers := make([]*ExternalTool, len(all))
	for i := range all {
		pointers[i] = &all[i]
	}
	return CatalogProfiles{hawkEye: hawkEyeServerSignatures(pointers), fofaMap: fofaMapServerSignatures(pointers)}
}

func (p CatalogProfiles) IsHawkEye(tool *ExternalTool) bool {
	if tool == nil {
		return false
	}
	if _, known := HawkEyeToolProfileForName(hawkEyeOriginalName(tool)); !known {
		return false
	}
	return explicitHawkEyeIdentity(tool) || p.hawkEye[strings.ToLower(strings.TrimSpace(tool.Server))]
}

func (p CatalogProfiles) IsFofaMap(tool *ExternalTool) bool {
	if tool == nil {
		return false
	}
	if _, known := FofaMapToolProfileForName(fofaMapOriginalName(tool)); !known {
		return false
	}
	return explicitFofaMapIdentity(tool) || p.fofaMap[strings.ToLower(strings.TrimSpace(tool.Server))]
}
