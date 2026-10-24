package model

import (
	"slices"
	"testing"
)

func TestWithoutRefusedAlarmUIDs(t *testing.T) {
	in := []Alarm{{UID: "a"}, {UID: "b"}, {UID: "a"}, {UID: ""}}
	got := WithoutRefusedAlarmUIDs(in)
	uids := make([]string, 0, len(got))
	for _, a := range got {
		uids = append(uids, a.UID)
	}
	if want := []string{"a", "b", "", ""}; !slices.Equal(uids, want) {
		t.Errorf("UIDs = %q, want %q", uids, want)
	}
	if in[2].UID != "a" {
		t.Errorf("input changed: %q", in[2].UID)
	}
}

func alarmAt(uid, trigger string) Alarm {
	return Alarm{UID: uid, Action: "DISPLAY", TriggerValue: trigger, Related: "START"}
}

// unverified returns a with the mark of migration 063.
func unverified(a Alarm) Alarm {
	a.UIDUnverified = true
	return a
}

func TestPairAlarms(t *testing.T) {
	tests := []struct {
		name     string
		stored   []Alarm
		incoming []Alarm
		held     map[string]bool
		want     []AlarmPair
	}{
		{
			name:     "content first, then UID",
			stored:   []Alarm{alarmAt("a", "-PT15M"), alarmAt("b", "-PT5M")},
			incoming: []Alarm{alarmAt("b", "-PT10M"), alarmAt("a", "-PT15M")},
			want:     []AlarmPair{{Stored: 1}, {Stored: 0, ByContent: true}},
		},
		{
			name:     "different UIDs never pair",
			stored:   []Alarm{alarmAt("minted", "-PT15M")},
			incoming: []Alarm{alarmAt("file", "-PT15M")},
			want:     []AlarmPair{{Stored: -1}},
		},
		{
			// Another item holds the UID of the file, and a build before
			// migration 063 wrote the stored UID: the store minted it.
			name:     "a UID that another item holds takes a minted row",
			stored:   []Alarm{unverified(alarmAt("minted", "-PT15M"))},
			incoming: []Alarm{alarmAt("file", "-PT15M")},
			held:     map[string]bool{"file": true},
			want:     []AlarmPair{{Stored: 0, ByContent: true, TakeUID: true}},
		},
		{
			// A build from migration 063 on wrote the stored UID, so a
			// client authored it. A change to a UID that another item
			// holds is a new alarm.
			name:     "the evidence needs the mark of migration 063",
			stored:   []Alarm{alarmAt("authored", "-PT15M")},
			incoming: []Alarm{alarmAt("file", "-PT15M")},
			held:     map[string]bool{"file": true},
			want:     []AlarmPair{{Stored: -1}},
		},
		{
			name:     "the evidence needs equal content",
			stored:   []Alarm{unverified(alarmAt("minted", "-PT15M"))},
			incoming: []Alarm{alarmAt("file", "-PT5M")},
			held:     map[string]bool{"file": true},
			want:     []AlarmPair{{Stored: -1}},
		},
		{
			name:     "the evidence skips a row that another alarm claims",
			stored:   []Alarm{unverified(alarmAt("a", "-PT15M"))},
			incoming: []Alarm{alarmAt("file", "-PT15M"), alarmAt("a", "-PT5M")},
			held:     map[string]bool{"file": true},
			want:     []AlarmPair{{Stored: -1}, {Stored: 0}},
		},
		{
			// Alarm a must not take the row with no UID: the backfill
			// would put a on a second row of the item.
			name:     "a UID that a stored row has skips rows with no UID",
			stored:   []Alarm{alarmAt("a", "-PT15M"), alarmAt("", "-PT5M")},
			incoming: []Alarm{alarmAt("a", "-PT5M"), alarmAt("b", "-PT15M")},
			want:     []AlarmPair{{Stored: 0}, {Stored: -1}},
		},
		{
			name:     "no UID pairs by content",
			stored:   []Alarm{alarmAt("minted", "-PT15M")},
			incoming: []Alarm{alarmAt("", "-PT15M")},
			want:     []AlarmPair{{Stored: 0, ByContent: true}},
		},
		{
			// A refused UID is cleared before the pair. That alarm must
			// not take the row of the edited alarm with UID a.
			name:     "no UID skips a row that another alarm claims",
			stored:   []Alarm{alarmAt("a", "-PT15M")},
			incoming: []Alarm{alarmAt("", "-PT15M"), alarmAt("a", "-PT5M")},
			want:     []AlarmPair{{Stored: -1}, {Stored: 0}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PairAlarms(tt.stored, tt.incoming, tt.held); !slices.Equal(got, tt.want) {
				t.Errorf("PairAlarms = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPairedAlarmUID(t *testing.T) {
	tests := []struct {
		name      string
		p         AlarmPair
		a, ex     Alarm
		wantUID   string
		wantWrite bool
	}{
		{"same UID", AlarmPair{}, alarmAt("a", "-PT15M"), alarmAt("a", "-PT15M"), "", false},
		{"stored row with no UID", AlarmPair{}, alarmAt("a", "-PT15M"), alarmAt("", "-PT15M"), "a", true},
		{"stored UID stays", AlarmPair{}, alarmAt("file", "-PT15M"), alarmAt("minted", "-PT15M"), "", false},
		{"third pass takes the file UID", AlarmPair{TakeUID: true}, alarmAt("file", "-PT15M"), alarmAt("minted", "-PT15M"), "file", true},
		{"incoming with no UID keeps the stored UID", AlarmPair{}, alarmAt("", "-PT15M"), alarmAt("minted", "-PT15M"), "", false},
		{"foreign alarm keeps no UID", AlarmPair{}, Alarm{Action: "X-APPLE-SOUND"}, Alarm{Action: "X-APPLE-SOUND"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, write := PairedAlarmUID(tt.p, tt.a, tt.ex)
			if write != tt.wantWrite || (write && uid != tt.wantUID) {
				t.Errorf("PairedAlarmUID = %q, %v; want %q, %v", uid, write, tt.wantUID, tt.wantWrite)
			}
		})
	}
	if uid, write := PairedAlarmUID(AlarmPair{}, alarmAt("", "-PT15M"), alarmAt("", "-PT15M")); !write || uid == "" {
		t.Errorf("fireable alarm with no UID on both sides = %q, %v; want a minted UID", uid, write)
	}
}
