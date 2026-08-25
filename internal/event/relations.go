package event

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Category CRUD

func (s *Service) ListCategories(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListCategoriesByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Category
	}
	return out, nil
}

func (s *Service) ListAllCategories(ctx context.Context) ([]string, error) {
	return s.q.ListAllEventCategories(ctx)
}

func (s *Service) ReplaceCategories(ctx context.Context, eventID int64, categories []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := replaceCategoriesTx(ctx, qtx, eventID, categories); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return fmt.Errorf("commit replace categories: %w", err)
	}
	return nil
}

// replaceCategoriesTx replaces an event's categories using a tx-bound Queries.
// It does not open or commit a transaction, so callers can compose it with
// other writes inside a single transaction.
func replaceCategoriesTx(ctx context.Context, qtx *storage.Queries, eventID int64, categories []string) error {
	if err := qtx.DeleteCategoriesByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete categories: %w", err)
	}
	for i, c := range categories {
		_, err := qtx.CreateEventCategory(ctx, storage.CreateEventCategoryParams{
			EventID:  eventID,
			Category: c,
			Position: int64(i),
		})
		if err != nil {
			return fmt.Errorf("create category: %w", err)
		}
	}
	return nil
}

// Attachment CRUD

func (s *Service) ListAttachments(ctx context.Context, eventID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListEventAttachmentsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: storage.NullableToString(r.Uri), FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, eventID int64, attachments []model.Attachment) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteEventAttachmentsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateEventAttachment(ctx, storage.CreateEventAttachmentParams{
			EventID: eventID, Uri: storage.StringToNullable(a.URI), Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}

// Comment CRUD

func (s *Service) ListComments(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListEventCommentsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceComments(ctx context.Context, eventID int64, comments []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteEventCommentsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete comments: %w", err)
	}
	for _, c := range comments {
		_, err := qtx.CreateEventComment(ctx, storage.CreateEventCommentParams{
			EventID: eventID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create comment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}

// Contact CRUD

func (s *Service) ListContacts(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListEventContactsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceContacts(ctx context.Context, eventID int64, contacts []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteEventContactsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete contacts: %w", err)
	}
	for _, c := range contacts {
		_, err := qtx.CreateEventContact(ctx, storage.CreateEventContactParams{
			EventID: eventID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create contact: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}

// Resource CRUD

func (s *Service) ListResources(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListEventResourcesByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceResources(ctx context.Context, eventID int64, resources []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteEventResourcesByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete resources: %w", err)
	}
	for _, r := range resources {
		_, err := qtx.CreateEventResource(ctx, storage.CreateEventResourceParams{
			EventID: eventID, Text: r,
		})
		if err != nil {
			return fmt.Errorf("create resource: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}

// Relation CRUD

func (s *Service) ListRelations(ctx context.Context, eventID int64) ([]model.Relation, error) {
	rows, err := s.q.ListEventRelationsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Relation, len(rows))
	for i, r := range rows {
		out[i] = model.Relation{ID: r.ID, RelType: r.RelType, RelUID: r.RelUid}
	}
	return out, nil
}

func (s *Service) ReplaceRelations(ctx context.Context, eventID int64, relations []model.Relation) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteEventRelationsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete relations: %w", err)
	}
	for _, r := range relations {
		_, err := qtx.CreateEventRelation(ctx, storage.CreateEventRelationParams{
			EventID: eventID, RelType: r.RelType, RelUid: r.RelUID,
		})
		if err != nil {
			return fmt.Errorf("create relation: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}

// X-Property CRUD

func (s *Service) ListXProperties(ctx context.Context, eventID int64) ([]model.XProperty, error) {
	rows, err := s.q.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: eventID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.XProperty, len(rows))
	for i, r := range rows {
		out[i] = model.XProperty{
			ID: r.ID, OwnerType: r.OwnerType, OwnerID: r.OwnerID,
			Name: r.Name, Value: r.Value, Params: r.Params,
		}
	}
	return out, nil
}

func (s *Service) ReplaceXProperties(ctx context.Context, eventID int64, xprops []model.XProperty) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: eventID,
	}); err != nil {
		return fmt.Errorf("delete x-properties: %w", err)
	}
	for _, xp := range xprops {
		if err := qtx.InsertXProperty(ctx, storage.InsertXPropertyParams{
			OwnerType: "event", OwnerID: eventID,
			Name: xp.Name, Value: xp.Value, Params: xp.Params,
		}); err != nil {
			return fmt.Errorf("insert x-property: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, eventID)
}
