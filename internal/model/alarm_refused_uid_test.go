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

func TestPairAlarms(t *testing.T) {
	tests := []struct {
		name     string
		stored   []Alarm
		incoming []Alarm
		want     []AlarmPair
	}{
		{
			name:     "content first, then UID",
			stored:   []Alarm{alarmAt("a", "-PT15M"), alarmAt("b", "-PT5M")},
			incoming: []Alarm{alarmAt("b", "-PT10M"), alarmAt("a", "-PT15M")},
			want:     []AlarmPair{{Stored: 1}, {Stored: 0, ByContent: true}},
		},
		{
			name:     "different UIDs pair by content in the third pass",
			stored:   []Alarm{alarmAt("minted", "-PT15M")},
			incoming: []Alarm{alarmAt("file", "-PT15M")},
			want:     []AlarmPair{{Stored: 0, ByContent: true}},
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
			name:     "different content never pairs across UIDs",
			stored:   []Alarm{alarmAt("a", "-PT15M")},
			incoming: []Alarm{alarmAt("b", "-PT5M")},
			want:     []AlarmPair{{Stored: -1}},
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
			if got := PairAlarms(tt.stored, tt.incoming); !slices.Equal(got, tt.want) {
				t.Errorf("PairAlarms = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPairedAlarmUID(t *testing.T) {
	tests := []struct {
		name      string
		a, ex     Alarm
		wantUID   string
		wantWrite bool
	}{
		{"same UID", alarmAt("a", "-PT15M"), alarmAt("a", "-PT15M"), "", false},
		{"stored row with no UID", alarmAt("a", "-PT15M"), alarmAt("", "-PT15M"), "a", true},
		{"third pass takes the incoming UID", alarmAt("file", "-PT15M"), alarmAt("minted", "-PT15M"), "file", true},
		{"incoming with no UID keeps the stored UID", alarmAt("", "-PT15M"), alarmAt("minted", "-PT15M"), "", false},
		{"foreign alarm keeps no UID", Alarm{Action: "X-APPLE-SOUND"}, Alarm{Action: "X-APPLE-SOUND"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, write := PairedAlarmUID(tt.a, tt.ex)
			if write != tt.wantWrite || (write && uid != tt.wantUID) {
				t.Errorf("PairedAlarmUID = %q, %v; want %q, %v", uid, write, tt.wantUID, tt.wantWrite)
			}
		})
	}
	if uid, write := PairedAlarmUID(alarmAt("", "-PT15M"), alarmAt("", "-PT15M")); !write || uid == "" {
		t.Errorf("fireable alarm with no UID on both sides = %q, %v; want a minted UID", uid, write)
	}
}
