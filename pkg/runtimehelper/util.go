package runtimehelper

// FirstNonEmpty returns the first non-empty string among values, or "" if all are empty.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
