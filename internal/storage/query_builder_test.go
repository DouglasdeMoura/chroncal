package storage

import (
	"strings"
	"testing"
)

func TestExpandInPlaceholders(t *testing.T) {
	tests := []struct {
		name         string
		ids          []int64
		wantPlace    string
		wantArgsLen  int
		wantArgsVals []int64
	}{
		{name: "single", ids: []int64{7}, wantPlace: "?", wantArgsLen: 1, wantArgsVals: []int64{7}},
		{name: "multiple", ids: []int64{1, 2, 3}, wantPlace: "?,?,?", wantArgsLen: 3, wantArgsVals: []int64{1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			place, args := expandInPlaceholders(tt.ids)
			if place != tt.wantPlace {
				t.Errorf("placeholders = %q, want %q", place, tt.wantPlace)
			}
			if strings.HasSuffix(place, ",") {
				t.Errorf("placeholders %q has trailing comma", place)
			}
			if len(args) != tt.wantArgsLen {
				t.Fatalf("len(args) = %d, want %d", len(args), tt.wantArgsLen)
			}
			for i, want := range tt.wantArgsVals {
				got, ok := args[i].(int64)
				if !ok || got != want {
					t.Errorf("args[%d] = %v, want %d", i, args[i], want)
				}
			}
		})
	}
}

func TestAddSoftDeleteFilter(t *testing.T) {
	tests := []struct {
		name           string
		includeDeleted bool
		deletedOnly    bool
		want           string
	}{
		{name: "default hides deleted", want: "deleted_at IS NULL"},
		{name: "include deleted adds no clause", includeDeleted: true, want: ""},
		{name: "deleted only inverts", deletedOnly: true, want: "deleted_at IS NOT NULL"},
		{name: "deleted only overrides include", includeDeleted: true, deletedOnly: true, want: "deleted_at IS NOT NULL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w whereBuilder
			w.addSoftDeleteFilter(tt.includeDeleted, tt.deletedOnly)
			where, _ := w.build()
			if tt.want == "" {
				if where != "" {
					t.Errorf("where = %q, want empty", where)
				}
				return
			}
			if !strings.Contains(where, tt.want) {
				t.Errorf("where = %q, want it to contain %q", where, tt.want)
			}
		})
	}
}
