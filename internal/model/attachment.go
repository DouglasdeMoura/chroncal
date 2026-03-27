package model

type Attachment struct {
	ID      int64
	URI     string
	FmtType string // MIME type from FMTTYPE param
}
