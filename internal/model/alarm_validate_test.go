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

func TestPrepareAlarmsForWrite_RejectsInvalidAction(t *testing.T) {
	_, err := PrepareAlarmsForWrite([]Alarm{
		{TriggerValue: "-PT30M"},
		{Action: "PROCEDURE", TriggerValue: "-PT15M"},
	})
	if !errors.Is(err, ErrInvalidAlarm) {
		t.Fatalf("err = %v, want ErrInvalidAlarm", err)
	}
	// The position is 1-based: the second element is "alarm 2".
	for _, want := range []string{"PROCEDURE", "action", "alarm 2"} {
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
