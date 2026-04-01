package storage

// StringToNullable converts an empty string to nil, non-empty to a pointer.
func StringToNullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NullableToString converts a nil pointer to "", non-nil to the pointed string.
func NullableToString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
