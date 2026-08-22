package journal

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Attachment CRUD

func (s *Service) ListAttachments(ctx context.Context, journalID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListJournalAttachmentsByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: storage.NullableToString(r.Uri), FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, journalID int64, attachments []model.Attachment) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteJournalAttachmentsByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateJournalAttachment(ctx, storage.CreateJournalAttachmentParams{
			JournalID: journalID, Uri: storage.StringToNullable(a.URI), Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, journalID)
	return nil
}
