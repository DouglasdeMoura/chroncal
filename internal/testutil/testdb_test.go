package testutil

import (
	"context"
	"testing"
)

// TestNewTestDBCopiesMatchAFreshDatabase confirms each template copy gets its
// own credential namespace and records only its own location. A copy that
// kept the template namespace could share a credential scope with a second
// copy. A copy that kept the template location looks like a moved database.
func TestNewTestDBCopiesMatchAFreshDatabase(t *testing.T) {
	ctx := context.Background()
	namespaces := map[string]bool{}
	for range 2 {
		db, _ := NewTestDB(t)
		var namespace, current string
		if err := db.QueryRowContext(ctx,
			`SELECT namespace, current_location FROM credential_namespace WHERE id = 1`,
		).Scan(&namespace, &current); err != nil {
			t.Fatalf("read credential namespace: %v", err)
		}
		if namespaces[namespace] {
			t.Errorf("two copies share the credential namespace %q", namespace)
		}
		namespaces[namespace] = true

		var locations int
		var other int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*), COUNT(*) FILTER (WHERE location <> ?) FROM credential_locations`, current,
		).Scan(&locations, &other); err != nil {
			t.Fatalf("count credential locations: %v", err)
		}
		if locations != 1 || other != 0 {
			t.Errorf("credential_locations has %d rows, %d of them for another file; want only this copy", locations, other)
		}
	}
}
