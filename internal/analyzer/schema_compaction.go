package analyzer

// Bound repeated prose only. Preserve every executable schema constraint and
// copy the input so validation and catalog discovery remain authoritative.
func compactToolSchema(schema map[string]interface{}) map[string]interface{} {
	return compactSchemaValue(schema).(map[string]interface{})
}

func compactSchemaValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			if description, ok := child.(string); key == "description" && ok {
				out[key] = truncateToolDescription(description, 240)
			} else {
				out[key] = compactSchemaValue(child)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, child := range v {
			out[i] = compactSchemaValue(child)
		}
		return out
	default:
		return value
	}
}
