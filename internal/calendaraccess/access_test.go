package calendaraccess

import (
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func TestEnsureWritableEnforcesAccessAndComponents(t *testing.T) {
	t.Parallel()

	db, q := testutil.NewTestDB(t)
	ctx := t.Context()

	testCases := []struct {
		name       string
		access     string
		components string
		component  string
		wantErr    error
	}{
		{name: "legacy metadata remains writable", access: "unknown", component: "VEVENT"},
		{name: "advertised component is writable", access: "write", components: "VEVENT,VTODO", component: "VTODO"},
		{name: "read-only collection", access: "read", components: "VEVENT", component: "VEVENT", wantErr: ErrReadOnly},
		{name: "unsupported component", access: "owner", components: "VTODO", component: "VEVENT", wantErr: ErrUnsupportedComponent},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx,
				"UPDATE calendars SET remote_access = ?, remote_components = ? WHERE id = 1",
				tc.access, tc.components,
			); err != nil {
				t.Fatalf("update calendar metadata: %v", err)
			}
			err := EnsureWritable(ctx, q, 1, tc.component)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("EnsureWritable error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
