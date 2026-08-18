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

// Migration 044 widened the action constraint, so the write boundary
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

// The write rule and the token shape must agree on every candidate. The
// import parser calls ValidAlarmActionToken and the services call
// StorableAlarmAction. A value the parser stores must therefore also pass
// a write (issue #595).
func TestStorableAlarmActionTokenShape(t *testing.T) {
	cases := []struct {
		action string
		want   bool
		why    string
	}{
		{"AUDIO", true, "a fireable action"},
		{"DISPLAY", true, "a fireable action"},
		{"NONE", true, "the Google sentinel"},
		{"PROCEDURE", true, "a legacy iana-token"},
		{"X-APPLE-SOUND", true, "an x-name with a hyphen"},
		{"X-VENDOR2", true, "an x-name with a digit"},
		{"display", true, "a token shape, whatever the case"},
		{"", false, "an empty action"},
		{" ", false, "a whitespace-only action"},
		{"\t", false, "a tab-only action"},
		{"NO NE", false, "an embedded space"},
		{"X-A;B", false, "a parameter separator"},
		{"X-A:B", false, "a value separator"},
		{"NEW\nLINE", false, "a line break"},
	}
	for _, c := range cases {
		if got := StorableAlarmAction(c.action); got != c.want {
			t.Errorf("StorableAlarmAction(%q) = %v, want %v (%s)", c.action, got, c.want, c.why)
		}
		if got := ValidAlarmActionToken(c.action); got != c.want {
			t.Errorf("ValidAlarmActionToken(%q) = %v, want %v (%s)", c.action, got, c.want, c.why)
		}
	}
}

// A write refuses a malformed action with the typed error. A caller can
// then tell the refusal apart from a storage failure.
func TestPrepareAlarmsForWrite_RejectsMalformedAction(t *testing.T) {
	for _, action := range []string{" ", "\t", "NO NE"} {
		_, err := PrepareAlarmsForWrite([]Alarm{{Action: action, TriggerValue: "-PT15M"}})
		if !errors.Is(err, ErrInvalidAlarm) {
			t.Errorf("PrepareAlarmsForWrite(action %q) error = %v, want ErrInvalidAlarm", action, err)
			continue
		}
		// The defaults replace an empty action before this check, so the
		// message must name the real cause. A user reads this text, and
		// the sync engine repeats it through InvalidAlarm.Err.
		if strings.Contains(err.Error(), "is empty") {
			t.Errorf("PrepareAlarmsForWrite(action %q) error = %v, want it to name the token shape, not emptiness", action, err)
		}
		if !strings.Contains(err.Error(), "iana-token") {
			t.Errorf("PrepareAlarmsForWrite(action %q) error = %v, want it to name the accepted shape", action, err)
		}
	}
}

// A stored row an older build wrote can hold a malformed action. The
// carry-over must leave it behind: feeding it back through the write rule
// would fail every --alarm update on it (issue #595).
func TestKeepSyncOnlyAlarms_DropsAnUnwritableStoredRow(t *testing.T) {
	stored := []Alarm{
		{Action: "X-APPLE-SOUND", TriggerValue: "-PT5M"},
		{Action: " ", TriggerValue: "-PT10M"},
	}
	kept := KeepSyncOnlyAlarms(stored, []Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}})

	if _, err := PrepareAlarmsForWrite(kept); err != nil {
		t.Fatalf("the carry-over must stay writable: %v", err)
	}
	for _, a := range kept {
		if a.Action == " " {
			t.Error("the carry-over kept a row the write rule refuses")
		}
	}
	var foundForeign bool
	for _, a := range kept {
		if a.Action == "X-APPLE-SOUND" {
			foundForeign = true
		}
	}
	if !foundForeign {
		t.Error("the carry-over dropped a valid preserved alarm")
	}
}
