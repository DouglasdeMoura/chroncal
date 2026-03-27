package model

type Alarm struct {
	ID           int64
	EventID      int64
	Action       string // DISPLAY, EMAIL, AUDIO
	TriggerValue string // e.g. "-PT15M" or absolute RFC 3339
	Description  string
}
