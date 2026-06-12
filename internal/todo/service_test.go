package todo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q)
}

func createTodo(t *testing.T, svc *Service) Todo {
	t.Helper()
	td, err := svc.Create(context.Background(), CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	return td
}

func TestTodoService_Create(t *testing.T) {
	svc := newTestService(t)
	td := createTodo(t, svc)

	if td.ID == 0 {
		t.Error("ID is 0")
	}
	if td.UID == "" {
		t.Error("UID is empty")
	}
	if td.Summary != "Test Todo" {
		t.Errorf("Summary = %q, want %q", td.Summary, "Test Todo")
	}
	if td.Status != "NEEDS-ACTION" {
		t.Errorf("Status = %q, want %q", td.Status, "NEEDS-ACTION")
	}
	if td.Class != "PUBLIC" {
		t.Errorf("Class = %q, want %q", td.Class, "PUBLIC")
	}
}

func TestTodoService_Get(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createTodo(t, svc)

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Summary != created.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, created.Summary)
	}

	_, err = svc.Get(ctx, 999)
	if err == nil {
		t.Error("Get(999) expected error")
	}
}

func TestTodoService_List(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createTodo(t, svc)

	// Create a completed todo
	td2, _ := svc.Create(ctx, CreateParams{CalendarID: 1, Summary: "Done"})
	svc.Complete(ctx, td2.ID)

	todos, _ := svc.List(ctx)
	if len(todos) != 1 {
		t.Errorf("List (incomplete) = %d, want 1", len(todos))
	}
}

func TestTodoService_ListAll(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createTodo(t, svc)

	td2, _ := svc.Create(ctx, CreateParams{CalendarID: 1, Summary: "Done"})
	svc.Complete(ctx, td2.ID)

	todos, _ := svc.ListAll(ctx)
	if len(todos) != 2 {
		t.Errorf("ListAll = %d, want 2", len(todos))
	}
}

func TestTodoService_ListByCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createTodo(t, svc)

	todos, _ := svc.ListByCalendar(ctx, 1)
	if len(todos) != 1 {
		t.Errorf("calendar 1: %d, want 1", len(todos))
	}

	todos2, _ := svc.ListByCalendar(ctx, 999)
	if len(todos2) != 0 {
		t.Errorf("calendar 999: %d, want 0", len(todos2))
	}
}

func TestTodoService_ListByStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createTodo(t, svc)

	todos, _ := svc.ListByStatus(ctx, "NEEDS-ACTION")
	if len(todos) != 1 {
		t.Errorf("NEEDS-ACTION: %d, want 1", len(todos))
	}

	todos2, _ := svc.ListByStatus(ctx, "COMPLETED")
	if len(todos2) != 0 {
		t.Errorf("COMPLETED: %d, want 0", len(todos2))
	}
}

func TestTodoService_ListByDueDateRange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createTodo(t, svc) // due April 1

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	todos, _ := svc.ListByDueDateRange(ctx, from, to)
	if len(todos) != 1 {
		t.Errorf("in-range: %d, want 1", len(todos))
	}

	from2 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	todos2, _ := svc.ListByDueDateRange(ctx, from2, to2)
	if len(todos2) != 0 {
		t.Errorf("out-of-range: %d, want 0", len(todos2))
	}
}

func TestTodoService_Update(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createTodo(t, svc)

	updated, err := svc.Update(ctx, created.ID, UpdateParams{
		Summary:         "Updated Summary",
		DueDate:         created.DueDate,
		Status:          "IN-PROCESS",
		CalendarID:      1,
		Class:           "PRIVATE",
		PercentComplete: 50,
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Summary != "Updated Summary" {
		t.Errorf("Summary = %q", updated.Summary)
	}
	if updated.Status != "IN-PROCESS" {
		t.Errorf("Status = %q", updated.Status)
	}
	if updated.PercentComplete != 50 {
		t.Errorf("PercentComplete = %d", updated.PercentComplete)
	}
	if updated.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", updated.Sequence)
	}
}

func TestTodoService_Complete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createTodo(t, svc)

	completed, err := svc.Complete(ctx, created.ID)
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if completed.Status != "COMPLETED" {
		t.Errorf("Status = %q, want COMPLETED", completed.Status)
	}
	if completed.PercentComplete != 100 {
		t.Errorf("PercentComplete = %d, want 100", completed.PercentComplete)
	}
	if completed.CompletedAt == "" {
		t.Error("CompletedAt is empty")
	}
}

func TestTodoService_CreateCompleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	td, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Summary:    "Already done",
		Status:     "COMPLETED",
	})
	if err != nil {
		t.Fatalf("Create COMPLETED: %v", err)
	}
	if td.Status != "COMPLETED" {
		t.Errorf("Status = %q, want COMPLETED", td.Status)
	}
	if td.PercentComplete != 100 {
		t.Errorf("PercentComplete = %d, want 100", td.PercentComplete)
	}
	if td.CompletedAt == "" {
		t.Error("CompletedAt should be auto-set when status=COMPLETED")
	}
	if _, err := time.Parse(time.RFC3339, td.CompletedAt); err != nil {
		t.Errorf("CompletedAt not valid RFC3339: %q", td.CompletedAt)
	}
}

func TestTodoService_UpdateToCompleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createTodo(t, svc)

	updated, err := svc.Update(ctx, created.ID, UpdateParams{
		Summary:    created.Summary,
		DueDate:    created.DueDate,
		CalendarID: 1,
		Status:     "COMPLETED",
	})
	if err != nil {
		t.Fatalf("Update to COMPLETED: %v", err)
	}
	if updated.PercentComplete != 100 {
		t.Errorf("PercentComplete = %d, want 100", updated.PercentComplete)
	}
	if updated.CompletedAt == "" {
		t.Error("CompletedAt should be auto-set when updating to COMPLETED")
	}
}

func TestTodoService_UpsertByUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	p := UpsertParams{
		UID:        "test-upsert-uid",
		CalendarID: 1,
		Summary:    "Original",
		DueDate:    "2026-04-01T23:59:59Z",
	}

	first, _ := svc.UpsertByUID(ctx, p)
	p.Summary = "Updated"
	second, _ := svc.UpsertByUID(ctx, p)

	if second.ID != first.ID {
		t.Errorf("upsert created new row: ID %d != %d", second.ID, first.ID)
	}
	if second.Summary != "Updated" {
		t.Errorf("Summary = %q, want %q", second.Summary, "Updated")
	}
}

func TestTodoService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createTodo(t, svc)

	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	_, err := svc.Get(ctx, created.ID)
	if err == nil {
		t.Error("Get after Delete expected error")
	}
}

func TestDelete_MasterWithOverridesRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-master", CalendarID: 1, Summary: "Weekly Review",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-master", CalendarID: 1, Summary: "Weekly Review (moved)",
		DueDate:      time.Date(2026, 4, 8, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: "2026-04-08T23:59:59Z",
	})

	master, _ := svc.GetByUID(ctx, "del-master")
	err := svc.Delete(ctx, master.ID)
	if !errors.Is(err, ErrHasOverrides) {
		t.Fatalf("Delete master with overrides: got %v, want ErrHasOverrides", err)
	}
}

func TestDelete_MasterNoOverridesSucceeds(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-solo", CalendarID: 1, Summary: "Solo Recurring",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY",
	})

	master, _ := svc.GetByUID(ctx, "del-solo")
	if err := svc.Delete(ctx, master.ID); err != nil {
		t.Fatalf("Delete solo master: %v", err)
	}
	_, err := svc.Get(ctx, master.ID)
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestDelete_OverrideAddsEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "exd-test", CalendarID: 1, Summary: "Weekly",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "exd-test", CalendarID: 1, Summary: "Weekly (moved)",
		DueDate:      time.Date(2026, 4, 8, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: "2026-04-08T23:59:59Z",
	})

	override, _ := svc.GetByUIDAndRecurrenceID(ctx, "exd-test", "2026-04-08T23:59:59Z")
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}

	master, _ := svc.GetByUID(ctx, "exd-test")
	exdates := master.ParseExDates()
	if len(exdates) != 1 {
		t.Fatalf("exdates count = %d, want 1", len(exdates))
	}
}

func TestDelete_OverridePreservesExistingEXDATEs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "pre-exd", CalendarID: 1, Summary: "Weekly",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY",
		ExDates:        "2026-04-15T23:59:59Z",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "pre-exd", CalendarID: 1, Summary: "Weekly (moved)",
		DueDate:      time.Date(2026, 4, 8, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: "2026-04-08T23:59:59Z",
	})

	override, _ := svc.GetByUIDAndRecurrenceID(ctx, "pre-exd", "2026-04-08T23:59:59Z")
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}

	master, _ := svc.GetByUID(ctx, "pre-exd")
	exdates := master.ParseExDates()
	if len(exdates) != 2 {
		t.Fatalf("exdates count = %d, want 2", len(exdates))
	}
}

func TestDeleteSeries_CascadesAll(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "series-del", CalendarID: 1, Summary: "Weekly",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	for _, recID := range []string{"2026-04-08T23:59:59Z", "2026-04-15T23:59:59Z"} {
		svc.UpsertByUID(ctx, UpsertParams{
			UID: "series-del", CalendarID: 1, Summary: "Override",
			DueDate:      time.Date(2026, 4, 8, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
			RecurrenceID: recID,
		})
	}

	if err := svc.DeleteSeries(ctx, "series-del"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}

	_, err := svc.GetByUID(ctx, "series-del")
	if err == nil {
		t.Error("master should be deleted")
	}
	overrides, _ := svc.ListOverridesByUID(ctx, "series-del")
	if len(overrides) != 0 {
		t.Errorf("overrides remaining = %d, want 0", len(overrides))
	}
}

func TestTodoService_Alarms(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	alarms, _ := svc.ListAlarms(ctx, td.ID)
	if len(alarms) != 0 {
		t.Fatalf("initial alarms = %d", len(alarms))
	}

	svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	alarms, _ = svc.ListAlarms(ctx, td.ID)
	if len(alarms) != 1 {
		t.Errorf("after replace: alarms = %d, want 1", len(alarms))
	}
}

func TestTodoService_Attendees(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	svc.ReplaceAttendees(ctx, td.ID, []model.Attendee{
		{Email: "user@example.com", Name: "User", RSVPStatus: "ACCEPTED", Role: "REQ-PARTICIPANT"},
	})
	attendees, _ := svc.ListAttendees(ctx, td.ID)
	if len(attendees) != 1 {
		t.Errorf("attendees = %d, want 1", len(attendees))
	}
	if attendees[0].Email != "user@example.com" {
		t.Errorf("email = %q", attendees[0].Email)
	}
}

func TestTodoService_ReplaceAlarms_UIDMatchPreservesRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	err := svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{UID: "todo-alarm-1", Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms error: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, td.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("ListAlarms: %v (n=%d)", err, len(alarms))
	}
	origID := alarms[0].ID

	// Same UID, changed trigger: must update the row in place so
	// todo_alarm_state rows keyed to its ID survive.
	err = svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{UID: "todo-alarm-1", Action: "DISPLAY", TriggerValue: "-PT30M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms (trigger change) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, td.ID)
	if len(alarms) != 1 || alarms[0].ID != origID {
		t.Errorf("UID-matched edit must update in place; got %+v want ID %d", alarms, origID)
	}
	if alarms[0].TriggerValue != "-PT30M" {
		t.Errorf("TriggerValue = %q, want -PT30M", alarms[0].TriggerValue)
	}

	// A different UID is a genuinely new alarm: row must be replaced.
	err = svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{UID: "todo-alarm-2", Action: "DISPLAY", TriggerValue: "-PT45M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms (new uid) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, td.ID)
	if len(alarms) != 1 || alarms[0].ID == origID {
		t.Errorf("different UID must create a new row; got %+v", alarms)
	}
}

func TestTodoService_ReplaceAlarms_XPropertiesRoundTrip(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	err := svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{{
		UID: "todo-alarm-xp", Action: "DISPLAY", TriggerValue: "-PT15M",
		XProperties: []model.XProperty{{Name: "X-TEST-PROP", Value: "v1"}},
	}})
	if err != nil {
		t.Fatalf("ReplaceAlarms error: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, td.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("ListAlarms: %v (n=%d)", err, len(alarms))
	}
	if len(alarms[0].XProperties) != 1 || alarms[0].XProperties[0].Value != "v1" {
		t.Fatalf("XProperties = %+v, want X-TEST-PROP=v1", alarms[0].XProperties)
	}

	// nil XProperties = caller has no X-prop knowledge: stored rows survive.
	err = svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{{
		UID: "todo-alarm-xp", Action: "DISPLAY", TriggerValue: "-PT15M",
	}})
	if err != nil {
		t.Fatalf("ReplaceAlarms (nil xprops) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, td.ID)
	if len(alarms) != 1 || len(alarms[0].XProperties) != 1 {
		t.Fatalf("nil XProperties must keep stored rows; got %+v", alarms)
	}

	// Empty non-nil slice = authoritative empty set: stored rows cleared.
	err = svc.ReplaceAlarms(ctx, td.ID, []model.Alarm{{
		UID: "todo-alarm-xp", Action: "DISPLAY", TriggerValue: "-PT15M",
		XProperties: []model.XProperty{},
	}})
	if err != nil {
		t.Fatalf("ReplaceAlarms (empty xprops) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, td.ID)
	if len(alarms) != 1 || len(alarms[0].XProperties) != 0 {
		t.Fatalf("empty XProperties must clear stored rows; got %+v", alarms[0].XProperties)
	}
}
