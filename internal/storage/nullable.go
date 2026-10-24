package storage

import (
	"database/sql"
	"errors"
)

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

// Found converts the error of a single-row lookup to a found flag.
// sql.ErrNoRows gives false and no error.
func Found(err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// HeldUIDSet returns the set of the UIDs that a query of the held alarm
// UIDs returns (ListEventAlarmUIDsHeldByOtherEvents,
// ListTodoAlarmUIDsHeldByOtherTodos).
func HeldUIDSet(rows []*string) map[string]bool {
	set := make(map[string]bool, len(rows))
	for _, uid := range rows {
		if uid != nil {
			set[*uid] = true
		}
	}
	return set
}
