package ical

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestRadicale_VTODO_WithComments(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:       "rad-vtodo-comments",
		Summary:   "Todo with comments",
		DueDate:   "2026-04-15",
		Status:    "NEEDS-ACTION",
		Comments:  []string{"First update", "Progress note"},
		Contacts:  []string{"Alice <alice@example.com>"},
		Resources: []string{"Laptop", "Whiteboard"},
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-todo-uid"},
		},
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "comments-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	if len(got.Comments) != 2 {
		t.Errorf("expected 2 comments, got %d: %v", len(got.Comments), got.Comments)
	}
	if len(got.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(got.Resources), got.Resources)
	}
	if len(got.Relations) != 1 {
		t.Errorf("expected 1 relation, got %d", len(got.Relations))
	}
}
