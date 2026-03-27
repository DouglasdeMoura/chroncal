package model

type Attachment struct {
	ID       int64
	URI      string // non-empty for URI attachments
	FmtType  string // MIME type from FMTTYPE param
	Data     []byte // non-nil for inline binary attachments
	Filename string // original filename for blob attachments
}
