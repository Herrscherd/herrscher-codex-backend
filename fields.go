package codex

func stringField(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return value
}

func mapField(fields map[string]any, key string) map[string]any {
	value, ok := fields[key].(map[string]any)
	if !ok {
		return nil
	}
	return value
}
