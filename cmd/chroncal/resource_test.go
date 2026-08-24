package main

import "testing"

// TestResourceVerbsWired guards the shared builders: a nil function field
// panics on the first real invocation (newPurgeCmd calls purgeByID after
// confirm). Each domain resource must assign every hook the builders use.
func TestResourceVerbsWired(t *testing.T) {
	t.Parallel()
	for _, r := range []resource{eventResource, todoResource, journalResource} {
		if r.name == "" {
			t.Error("resource name is empty")
		}
		if r.resolve == nil {
			t.Errorf("%s.resolve is nil", r.name)
		}
		if r.del == nil {
			t.Errorf("%s.del is nil", r.name)
		}
		if r.delSeries == nil {
			t.Errorf("%s.delSeries is nil", r.name)
		}
		if r.restoreByID == nil {
			t.Errorf("%s.restoreByID is nil", r.name)
		}
		if r.restoreByUID == nil {
			t.Errorf("%s.restoreByUID is nil", r.name)
		}
		if r.purgeCandidate == nil {
			t.Errorf("%s.purgeCandidate is nil", r.name)
		}
		if r.purgeByID == nil {
			t.Errorf("%s.purgeByID is nil", r.name)
		}
		if r.purgeDeleted == nil {
			t.Errorf("%s.purgeDeleted is nil", r.name)
		}
		if r.errNotDeleted == nil {
			t.Errorf("%s.errNotDeleted is nil", r.name)
		}
	}
}
