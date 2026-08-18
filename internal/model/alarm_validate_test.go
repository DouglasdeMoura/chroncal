package model

import (
	"errors"
	"strings"
	"testing"
)

// TestAlarmActionsMatchPredicate guards AlarmActions and
// FireableAlarmAction together. The predicate must accept every listed
// value and only those. AlarmActions must also return a fresh slice:
// a caller-side change must not widen the set.
func TestAlarmActionsMatchPredicate(t *testing.T) {
	for _, a := range AlarmActions() {
		if !FireableAlarmAction(a) {
			t.Errorf("FireableAlarmAction(%q) = false, want true", a)
		}
	}
	for _, a := range AlarmRelatedValues() {
		if !ValidAlarmRelated(a) {
			t.Errorf("ValidAlarmRelated(%q) = false, want true", a)
		}
	}
	// A preserved foreign action is storable but never fireable.
	for _, a := range []string{"", "NONE", "PROCEDURE", "display", "audio"} {
		if FireableAlarmAction(a) {
			t.Errorf("FireableAlarmAction(%q) = true, want false", a)
		}
	}
	leaked := AlarmActions()
	leaked[0] = "PROCEDURE"
	if FireableAlarmAction("PROCEDURE") {
		t.Error("a change to the returned slice widened the set")
	}
}

func TestPrepareAlarmsForWrite_FillsDefaults(t *testing.T) {
	in := []Alarm{{TriggerValue: "-PT15M"}}
	out, err := PrepareAlarmsForWrite(in)
	if err != nil {
		t.Fatalf("PrepareAlarmsForWrite error: %v", err)
	}
	if out[0].Action != DefaultAlarmAction {
		t.Errorf("Action = %q, want %q", out[0].Action, DefaultAlarmAction)
	}
	if out[0].Related != DefaultAlarmRelated {
		t.Errorf("Related = %q, want %q", out[0].Related, DefaultAlarmRelated)
	}
	if in[0].Action != "" || in[0].Related != "" {
		t.Errorf("the caller's slice changed: %+v", in[0])
	}
}

func TestPrepareAlarmsForWrite_KeepsValidValues(t *testing.T) {
	out, err := PrepareAlarmsForWrite([]Alarm{{Action: "EMAIL", Related: "END", TriggerValue: "-PT15M"}})
	if err != nil {
		t.Fatalf("PrepareAlarmsForWrite error: %v", err)
	}
	if out[0].Action != "EMAIL" || out[0].Related != "END" {
		t.Errorf("valid values changed: %+v", out[0])
	}
}

// Migration 043 widened the action constraint, so the write boundary
// accepts a preserved foreign action (issue #579). Only an empty action
// fails, and prepareAlarmForWrite fills that with the default first, so
// the reject path needs a caller that clears the field after the fill.
func TestPrepareAlarmsForWrite_KeepsPreservedAction(t *testing.T) {
	out, err := PrepareAlarmsForWrite([]Alarm{
		{Action: "NONE", TriggerValue: "-PT30M"},
		{Action: "X-Apple-Sound", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatalf("PrepareAlarmsForWrite error: %v", err)
	}
	if out[0].Action != "NONE" || out[1].Action != "X-Apple-Sound" {
		t.Errorf("actions = %q/%q, want the preserved values", out[0].Action, out[1].Action)
	}
}

// The related field keeps the narrow rule, so it still reports the index,
// the field, and the value.
func TestPrepareAlarmsForWrite_ReportsTheAlarmPosition(t *testing.T) {
	_, err := PrepareAlarmsForWrite([]Alarm{
		{TriggerValue: "-PT30M"},
		{Action: "DISPLAY", Related: "MIDDLE", TriggerValue: "-PT15M"},
	})
	if !errors.Is(err, ErrInvalidAlarm) {
		t.Fatalf("err = %v, want ErrInvalidAlarm", err)
	}
	// The position is 1-based: the second element is "alarm 2".
	for _, want := range []string{"MIDDLE", "related", "alarm 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

func TestPrepareAlarmsForWrite_RejectsInvalidRelated(t *testing.T) {
	_, err := PrepareAlarmsForWrite([]Alarm{{Action: "DISPLAY", Related: "MIDDLE", TriggerValue: "-PT15M"}})
	if !errors.Is(err, ErrInvalidAlarm) {
		t.Fatalf("err = %v, want ErrInvalidAlarm", err)
	}
	if !strings.Contains(err.Error(), "MIDDLE") || !strings.Contains(err.Error(), "related") {
		t.Errorf("err = %q, want the field name and the value", err)
	}
}
