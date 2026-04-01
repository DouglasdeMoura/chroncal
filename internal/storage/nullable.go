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

// BoolToInt converts a bool to int64 (1 for true, 0 for false).
func BoolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
