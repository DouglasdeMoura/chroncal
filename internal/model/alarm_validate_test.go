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

// The carry-over keeps every stored row the engine cannot fire, and it
// applies no further test. A dropped row is a deleted row: the caller
// feeds the result to a replace, which removes what the list omits, and
// the next push then deletes the VALARM of another client (issue #579).
// Migration 045 repairs the malformed actions an older build stored, so
// the carry-over never meets one.
func TestKeepSyncOnlyAlarms_KeepsEveryNonFireableRow(t *testing.T) {
	stored := []Alarm{
		{Action: "X-APPLE-SOUND", TriggerValue: "-PT5M"},
		{Action: "NONE", TriggerValue: "-PT10M"},
		{Action: "DISPLAY", TriggerValue: "-PT20M"},
	}
	kept := KeepSyncOnlyAlarms(stored, []Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}})

	actions := make([]string, 0, len(kept))
	for _, a := range kept {
		actions = append(actions, a.Action)
	}
	if len(kept) != 3 {
		t.Fatalf("kept = %v, want the replacement plus both preserved rows", actions)
	}
	for _, want := range []string{"X-APPLE-SOUND", "NONE"} {
		var found bool
		for _, a := range kept {
			if a.Action == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the carry-over dropped %q, which a replace would then delete", want)
		}
	}
	// The stored fireable row is not carried: the replacement states it.
	var fireable int
	for _, a := range kept {
		if a.Action == "DISPLAY" {
			fireable++
		}
	}
	if fireable != 1 {
		t.Errorf("DISPLAY appears %d times, want only the replacement copy", fireable)
	}
}

// A row the read boundary normalized must stay in the carry-over. The
// services map a malformed stored action to UnsupportedAlarmAction, so
// that value is what the carry-over sees (issue #607). A drop here would
// delete the VALARM of another client on the next push.
func TestKeepSyncOnlyAlarms_KeepsTheNormalizedRow(t *testing.T) {
	stored := []Alarm{
		{Action: UnsupportedAlarmAction, TriggerValue: "-PT5M"},
		{Action: "X-APPLE-SOUND", TriggerValue: "-PT10M"},
	}
	kept := KeepSyncOnlyAlarms(stored, []Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}})

	if len(kept) != 3 {
		t.Fatalf("kept = %d rows, want the replacement plus both stored rows", len(kept))
	}
	for _, a := range kept[1:] {
		if !StorableAlarmAction(a.Action) {
			t.Errorf("carried action %q fails the write rule, so the edit fails", a.Action)
		}
		if FireableAlarmAction(a.Action) {
			t.Errorf("carried action %q is fireable, so the engine would fire the alarm of another client", a.Action)
		}
		if AlarmUIDForWrite(a) != "" {
			t.Errorf("a write mints a UID for %q, which the next push sends to the server", a.Action)
		}
	}
}

// The reserved token must satisfy every rule the repaired row meets later.
func TestUnsupportedAlarmActionKeepsForeignTreatment(t *testing.T) {
	if !ValidAlarmActionToken(UnsupportedAlarmAction) {
		t.Errorf("%q is not a valid token, so a write refuses the repaired row", UnsupportedAlarmAction)
	}
	if !StorableAlarmAction(UnsupportedAlarmAction) {
		t.Errorf("%q fails the write rule", UnsupportedAlarmAction)
	}
	if FireableAlarmAction(UnsupportedAlarmAction) {
		t.Errorf("%q is fireable, so the engine fires a repaired foreign alarm", UnsupportedAlarmAction)
	}
	if got := AlarmUIDForWrite(Alarm{Action: UnsupportedAlarmAction}); got != "" {
		t.Errorf("AlarmUIDForWrite = %q, want an empty UID for a preserved row", got)
	}
}

// NormalizeAlarmAction keeps every value a write already accepts.
func TestNormalizeAlarmAction(t *testing.T) {
	cases := map[string]string{
		"DISPLAY":       "DISPLAY",
		"X-APPLE-SOUND": "X-APPLE-SOUND",
		"x-apple-sound": "x-apple-sound",
		"NONE":          "NONE",
		"":              "",
		" ":             UnsupportedAlarmAction,
		"NO NE":         UnsupportedAlarmAction,
		"a.b":           UnsupportedAlarmAction,
	}
	for in, want := range cases {
		if got := NormalizeAlarmAction(in); got != want {
			t.Errorf("NormalizeAlarmAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// The read boundary normalizes a stored row, but the write rule stays
// strict. A caller that supplies a malformed action still fails, so a
// producer defect cannot pass in silence (issue #607).
func TestPrepareAlarmsForWrite_StillRefusesACallerValue(t *testing.T) {
	for _, action := range []string{" ", "NO NE", "a.b"} {
		_, err := PrepareAlarmsForWrite([]Alarm{{Action: action, TriggerValue: "-PT15M"}})
		if !errors.Is(err, ErrInvalidAlarm) {
			t.Errorf("PrepareAlarmsForWrite(%q) error = %v, want ErrInvalidAlarm", action, err)
		}
	}
}
