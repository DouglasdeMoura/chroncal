package model

import (
	"errors"
	"slices"
	"testing"
)

func TestWithoutRefusedAlarmUIDs(t *testing.T) {
	in := []Alarm{{UID: "free"}, {UID: "held"}, {UID: "free"}, {UID: ""}}
	held := func(uid string) (bool, error) { return uid == "held", nil }
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
	if in[1].UID != "held" {
		t.Errorf("input changed: %q", in[1].UID)
	}

	boom := errors.New("boom")
	if _, err := WithoutRefusedAlarmUIDs(in, func(string) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
}
