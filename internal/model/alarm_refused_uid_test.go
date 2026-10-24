package model

import (
	"errors"
	"slices"
	"testing"
)

func TestWithoutRefusedAlarmUIDs(t *testing.T) {
	in := []Alarm{{UID: "free"}, {UID: "held"}, {UID: "free"}, {UID: ""}}
	var asked []string
	held := func(uids []string) (map[string]bool, error) {
		asked = uids
		return map[string]bool{"held": true}, nil
	}
	got, err := WithoutRefusedAlarmUIDs(in, held)
	if err != nil {
		t.Fatalf("WithoutRefusedAlarmUIDs: %v", err)
	}
	uids := make([]string, 0, len(got))
	for _, a := range got {
		uids = append(uids, a.UID)
	}
	if want := []string{"free", "", "", ""}; !slices.Equal(uids, want) {
		t.Errorf("UIDs = %q, want %q", uids, want)
	}
	if want := []string{"free", "held", "free"}; !slices.Equal(asked, want) {
		t.Errorf("asked = %q, want %q", asked, want)
	}
	if in[1].UID != "held" {
		t.Errorf("input changed: %q", in[1].UID)
	}

	boom := errors.New("boom")
	if _, err := WithoutRefusedAlarmUIDs(in, func([]string) (map[string]bool, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
	if _, err := WithoutRefusedAlarmUIDs([]Alarm{{}}, func([]string) (map[string]bool, error) { return nil, boom }); err != nil {
		t.Errorf("no UIDs: error = %v, want no lookup", err)
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
			name:     "different UIDs never pair",
			stored:   []Alarm{alarmAt("a", "-PT15M")},
			incoming: []Alarm{alarmAt("b", "-PT15M")},
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
