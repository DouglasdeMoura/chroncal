package todo

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Category CRUD

func (s *Service) ListCategories(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListCategoriesByTodoID(ctx, todoID)
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
	return s.q.ListAllTodoCategories(ctx)
}

func (s *Service) ReplaceCategories(ctx context.Context, todoID int64, categories []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := replaceCategoriesTx(ctx, qtx, todoID, categories); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return fmt.Errorf("commit replace categories: %w", err)
	}
	return nil
}

// replaceCategoriesTx wipes and rewrites a todo's categories using a tx-bound
// Queries. It opens no transaction and does not commit, so callers can compose
// it with the parent todo write inside one transaction (issue #222).
func replaceCategoriesTx(ctx context.Context, qtx *storage.Queries, todoID int64, categories []string) error {
	if err := qtx.DeleteCategoriesByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete categories: %w", err)
	}
	for i, c := range categories {
		if _, err := qtx.CreateTodoCategory(ctx, storage.CreateTodoCategoryParams{
			TodoID:   todoID,
			Category: c,
			Position: int64(i),
		}); err != nil {
			return fmt.Errorf("create category: %w", err)
		}
	}
	return nil
}

// Attachment CRUD

func (s *Service) ListAttachments(ctx context.Context, todoID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListTodoAttachmentsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: storage.NullableToString(r.Uri), FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, todoID int64, attachments []model.Attachment) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteTodoAttachmentsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateTodoAttachment(ctx, storage.CreateTodoAttachmentParams{
			TodoID: todoID, Uri: storage.StringToNullable(a.URI), Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// Comment CRUD

func (s *Service) ListComments(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListTodoCommentsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceComments(ctx context.Context, todoID int64, comments []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteTodoCommentsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete comments: %w", err)
	}
	for _, c := range comments {
		_, err := qtx.CreateTodoComment(ctx, storage.CreateTodoCommentParams{
			TodoID: todoID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create comment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// Contact CRUD

func (s *Service) ListContacts(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListTodoContactsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceContacts(ctx context.Context, todoID int64, contacts []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteTodoContactsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete contacts: %w", err)
	}
	for _, c := range contacts {
		_, err := qtx.CreateTodoContact(ctx, storage.CreateTodoContactParams{
			TodoID: todoID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create contact: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// Resource CRUD

func (s *Service) ListResources(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListTodoResourcesByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceResources(ctx context.Context, todoID int64, resources []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteTodoResourcesByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete resources: %w", err)
	}
	for _, r := range resources {
		_, err := qtx.CreateTodoResource(ctx, storage.CreateTodoResourceParams{
			TodoID: todoID, Text: r,
		})
		if err != nil {
			return fmt.Errorf("create resource: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// Relation CRUD

func (s *Service) ListRelations(ctx context.Context, todoID int64) ([]model.Relation, error) {
	rows, err := s.q.ListTodoRelationsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Relation, len(rows))
	for i, r := range rows {
		out[i] = model.Relation{ID: r.ID, RelType: r.RelType, RelUID: r.RelUid}
	}
	return out, nil
}

func (s *Service) ReplaceRelations(ctx context.Context, todoID int64, relations []model.Relation) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteTodoRelationsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete relations: %w", err)
	}
	for _, r := range relations {
		_, err := qtx.CreateTodoRelation(ctx, storage.CreateTodoRelationParams{
			TodoID: todoID, RelType: r.RelType, RelUid: r.RelUID,
		})
		if err != nil {
			return fmt.Errorf("create relation: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// X-Property CRUD

func (s *Service) ListXProperties(ctx context.Context, todoID int64) ([]model.XProperty, error) {
	rows, err := s.q.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "todo", OwnerID: todoID,
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

func (s *Service) ReplaceXProperties(ctx context.Context, todoID int64, xprops []model.XProperty) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{
		OwnerType: "todo", OwnerID: todoID,
	}); err != nil {
		return fmt.Errorf("delete x-properties: %w", err)
	}
	for _, xp := range xprops {
		if err := qtx.InsertXProperty(ctx, storage.InsertXPropertyParams{
			OwnerType: "todo", OwnerID: todoID,
			Name: xp.Name, Value: xp.Value, Params: xp.Params,
		}); err != nil {
			return fmt.Errorf("insert x-property: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}
