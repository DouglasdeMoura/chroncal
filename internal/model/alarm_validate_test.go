package model

import (
	"errors"
	"strings"
	"testing"
)

// TestAlarmActionsMatchPredicate guards AlarmActions and
// ValidAlarmAction together. The predicate must accept every listed
// value and only those. AlarmActions must also return a fresh slice:
// a caller-side change must not widen the accepted set.
func TestAlarmActionsMatchPredicate(t *testing.T) {
	for _, a := range AlarmActions() {
		if !ValidAlarmAction(a) {
			t.Errorf("ValidAlarmAction(%q) = false, want true", a)
		}
	}
	for _, a := range AlarmRelatedValues() {
		if !ValidAlarmRelated(a) {
			t.Errorf("ValidAlarmRelated(%q) = false, want true", a)
		}
	}
	for _, a := range []string{"", "NONE", "PROCEDURE", "display", "audio"} {
		if ValidAlarmAction(a) {
			t.Errorf("ValidAlarmAction(%q) = true, want false", a)
		}
	}
	leaked := AlarmActions()
	leaked[0] = "PROCEDURE"
	if ValidAlarmAction("PROCEDURE") {
		t.Error("a change to the returned slice widened the accepted set")
	}
}

func TestPrepareAlarmForWrite_FillsDefaults(t *testing.T) {
	a := Alarm{TriggerValue: "-PT15M"}
	if err := PrepareAlarmForWrite(&a); err != nil {
		t.Fatalf("PrepareAlarmForWrite error: %v", err)
	}
	if a.Action != DefaultAlarmAction {
		t.Errorf("Action = %q, want %q", a.Action, DefaultAlarmAction)
	}
	if a.Related != DefaultAlarmRelated {
		t.Errorf("Related = %q, want %q", a.Related, DefaultAlarmRelated)
	}
}

func TestPrepareAlarmForWrite_KeepsValidValues(t *testing.T) {
	a := Alarm{Action: "EMAIL", Related: "END", TriggerValue: "-PT15M"}
	if err := PrepareAlarmForWrite(&a); err != nil {
		t.Fatalf("PrepareAlarmForWrite error: %v", err)
	}
	if a.Action != "EMAIL" || a.Related != "END" {
		t.Errorf("valid values changed: %+v", a)
	}
}

func TestPrepareAlarmForWrite_RejectsInvalidAction(t *testing.T) {
	a := Alarm{Action: "PROCEDURE", TriggerValue: "-PT15M"}
	err := PrepareAlarmForWrite(&a)
	if !errors.Is(err, ErrInvalidAlarm) {
		t.Fatalf("err = %v, want ErrInvalidAlarm", err)
	}
	if !strings.Contains(err.Error(), "PROCEDURE") || !strings.Contains(err.Error(), "action") {
		t.Errorf("err = %q, want the field name and the value", err)
	}
}

func TestPrepareAlarmForWrite_RejectsInvalidRelated(t *testing.T) {
	a := Alarm{Action: "DISPLAY", Related: "MIDDLE", TriggerValue: "-PT15M"}
	err := PrepareAlarmForWrite(&a)
	if !errors.Is(err, ErrInvalidAlarm) {
		t.Fatalf("err = %v, want ErrInvalidAlarm", err)
	}
	if !strings.Contains(err.Error(), "MIDDLE") || !strings.Contains(err.Error(), "related") {
		t.Errorf("err = %q, want the field name and the value", err)
	}
}
