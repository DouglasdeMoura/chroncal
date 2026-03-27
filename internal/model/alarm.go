package model

type Alarm struct {
	ID           int64
	EventID      int64
	Action       string // DISPLAY, EMAIL, AUDIO
	TriggerValue string // e.g. "-PT15M" or absolute RFC 3339
	Description  string
	Repeat       int    // number of additional repetitions
	Duration     string // repeat interval (RFC 5545 duration, e.g. PT5M)
	Related      string // trigger anchor: START or END
	Attendees    []AlarmAttendee
}

type AlarmAttendee struct {
	ID    int64
	Email string
	Name  string
}
